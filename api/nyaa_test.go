package handler

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const nyaaTestHash = "f40936eaeb6dd40559b9a547cafa34eef4a3d537"

func TestParseNyaaRSSAndConvertCandidate(t *testing.T) {
	var feed nyaaRSS
	if err := xml.Unmarshal([]byte(nyaaTestRSS(nyaaTestHash, "Example.Show.S02E07.1080p.WEB-DL", 321)), &feed); err != nil {
		t.Fatalf("xml.Unmarshal() error = %v", err)
	}
	if len(feed.Channel.Items) != 1 {
		t.Fatalf("RSS items = %d, want 1", len(feed.Channel.Items))
	}
	item, ok := nyaaItemAsAnimeBRSearchItem(feed.Channel.Items[0])
	if !ok {
		t.Fatal("valid Nyaa anime item was rejected")
	}
	if item.InfoHash != nyaaTestHash || item.NyaaID != 2148182 {
		t.Fatalf("converted identity = %+v", item)
	}
	if item.Seeders != 321 || item.Leechers != 12 || item.TotalSize != 1503238554 {
		t.Fatalf("converted swarm/size = %+v", item)
	}
	wantTimestamp := time.Date(2026, time.August, 18, 19, 50, 26, 0, time.UTC).Unix()
	if item.Timestamp != wantTimestamp {
		t.Fatalf("timestamp = %d, want %d", item.Timestamp, wantTimestamp)
	}

	invalid := feed.Channel.Items[0]
	invalid.InfoHash = "not-a-btih"
	if _, ok := nyaaItemAsAnimeBRSearchItem(invalid); ok {
		t.Fatal("invalid infohash was accepted")
	}
	invalid = feed.Channel.Items[0]
	invalid.CategoryID = "2_1"
	if _, ok := nyaaItemAsAnimeBRSearchItem(invalid); ok {
		t.Fatal("non-anime category was accepted")
	}
}

func TestParseNyaaSize(t *testing.T) {
	tests := map[string]int64{
		"594.0 MiB": 622854144,
		"1.4 GiB":   1503238554,
		"900 MB":    900000000,
		"invalid":   0,
	}
	for raw, want := range tests {
		if got := parseNyaaSize(raw); got != want {
			t.Errorf("parseNyaaSize(%q) = %d, want %d", raw, got, want)
		}
	}
}

func TestDeriveNyaaAliasQueryFromAnimeToshoTitles(t *testing.T) {
	items := []animeToshoSearchItem{
		{Title: "[AnoZu] Skeleton.Knight.in.Another.World.2022.S02E07.1080p"},
		{Title: "[ToonsHub] Skeleton Knight in Another World S02E07 1080p"},
		{Title: "Skeleton.Knight.in.Another.World.S02E07.1080p-VARYG"},
	}
	request := AnimeBRSearchRequest{
		Query: "Gaikotsu Kishi sama Tadaima Isekai e Odekake chuu II", Season: 2, Episode: 7,
	}
	if got, want := deriveNyaaAliasQuery(items, request), "Skeleton Knight in Another World S02E07"; got != want {
		t.Fatalf("deriveNyaaAliasQuery() = %q, want %q", got, want)
	}

	request.Query = "Skeleton Knight in Another World"
	if got := deriveNyaaAliasQuery(items, request); got != "" {
		t.Fatalf("matching requested title generated redundant alias query %q", got)
	}
}

func TestNyaaSearchUsesRSSContractAndCache(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		query := r.URL.Query()
		if query.Get("page") != "rss" || query.Get("c") != "1_0" || query.Get("f") != "0" || query.Get("q") != "Example S02E07" {
			t.Errorf("unexpected Nyaa query: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(nyaaTestRSS(nyaaTestHash, "Example.S02E07", 10)))
	}))
	defer server.Close()

	cache := &memoryAnimeBRCache{values: make(map[string][]byte)}
	service := newAnimeBRService(nyaaTestConfig(server.URL), cache, server.Client())
	for range 2 {
		items, err := service.searchNyaa(context.Background(), "Example S02E07")
		if err != nil || len(items) != 1 {
			t.Fatalf("searchNyaa() items=%d err=%v", len(items), err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("Nyaa HTTP calls = %d, want 1 after cache hit", got)
	}
}

func TestNyaaRequestSlotHonorsContextWhileBusy(t *testing.T) {
	service := newAnimeBRService(nyaaTestConfig("https://example.invalid"), nil, nil)
	service.nyaaRequestGate <- struct{}{}
	defer func() { <-service.nyaaRequestGate }()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := service.acquireNyaaRequestGate(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("acquireNyaaRequestGate() error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("canceled request remained queued for %v", elapsed)
	}
}

func TestConcurrentIdenticalNyaaSearchUsesOneRequestWithoutConsumingDelay(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(nyaaTestRSS(nyaaTestHash, "Example.S02E07", 10)))
	}))
	defer server.Close()

	cache := &memoryAnimeBRCache{values: make(map[string][]byte)}
	config := nyaaTestConfig(server.URL)
	config.NyaaRequestDelay = 500 * time.Millisecond
	service := newAnimeBRService(config, cache, server.Client())
	start := make(chan struct{})
	errorsByWorker := make(chan error, 2)
	startedAt := time.Now()
	for range 2 {
		go func() {
			<-start
			_, err := service.searchNyaa(context.Background(), "Example S02E07")
			errorsByWorker <- err
		}()
	}
	close(start)
	for range 2 {
		if err := <-errorsByWorker; err != nil {
			t.Fatalf("concurrent searchNyaa() error = %v", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("Nyaa HTTP calls = %d, want 1", got)
	}
	if elapsed := time.Since(startedAt); elapsed >= 400*time.Millisecond {
		t.Fatalf("cache hit incorrectly consumed the 500ms crawl delay: elapsed=%v", elapsed)
	}
}

func TestAnimeBRNyaaDiscoveryIsVerifiedByExactBTIH(t *testing.T) {
	var hashLookups atomic.Int32
	server := newNyaaAnimeBRFixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/json" && r.URL.Query().Get("show") != "torrent" {
			_ = json.NewEncoder(w).Encode([]animeToshoSearchItem{})
			return
		}
		if r.URL.Path == "/json" && r.URL.Query().Get("btih") == nyaaTestHash {
			hashLookups.Add(1)
			_ = json.NewEncoder(w).Encode(verifiedNyaaTestDetail(nyaaTestHash))
			return
		}
		http.NotFound(w, r)
	})
	defer server.Close()

	service := newAnimeBRService(nyaaTestConfig(server.URL), nil, server.Client())
	releases, err := service.Search(context.Background(), AnimeBRSearchRequest{
		Query: "Gaikotsu Kishi sama Tadaima Isekai e Odekake chuu II", Season: 2, Episode: 7, TVDBID: 401279,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(releases) != 1 {
		t.Fatalf("releases = %d, want 1: %+v", len(releases), releases)
	}
	release := releases[0]
	if release.InfoHash != nyaaTestHash || release.Source != "Nyaa.si + Anime Tosho NEW" {
		t.Fatalf("release identity/source = %+v", release)
	}
	if release.DownloadURL != "https://nyaa.si/download/2148182.torrent" || release.Details != "https://nyaa.si/view/2148182" {
		t.Fatalf("Nyaa URLs were not preferred: %+v", release)
	}
	if release.Seeders != 321 || !strings.Contains(release.Title, "S02E07") || !strings.Contains(release.Title, "[Brazilian]") {
		t.Fatalf("release metadata = %+v", release)
	}
	if !slices.Contains(release.Evidence, "Nyaa.si candidate matched Anime Tosho metadata by exact infohash") {
		t.Fatalf("release does not audit the Nyaa/Anime Tosho hash match: %+v", release.Evidence)
	}
	if got := hashLookups.Load(); got != 1 {
		t.Fatalf("BTIH detail lookups = %d, want exactly 1 after query dedupe", got)
	}
}

func TestAnimeBRNyaaAndAnimeToshoDiscoveryDeduplicateByHash(t *testing.T) {
	var idLookups atomic.Int32
	var hashLookups atomic.Int32
	server := newNyaaAnimeBRFixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("show") != "torrent" {
			_ = json.NewEncoder(w).Encode([]animeToshoSearchItem{{
				ID: 630406, Title: "Gaikotsu.Kishi.S02E07.1080p", Status: "complete", InfoHash: nyaaTestHash,
				TorrentURL: "https://animetosho.xyz/download/630406/torrent", Seeders: 5,
			}})
			return
		}
		if r.URL.Query().Get("id") == "630406" {
			idLookups.Add(1)
			_ = json.NewEncoder(w).Encode(verifiedNyaaTestDetail(nyaaTestHash))
			return
		}
		if r.URL.Query().Get("btih") != "" {
			hashLookups.Add(1)
		}
		http.NotFound(w, r)
	})
	defer server.Close()

	service := newAnimeBRService(nyaaTestConfig(server.URL), nil, server.Client())
	releases, err := service.Search(context.Background(), AnimeBRSearchRequest{
		Query: "Gaikotsu Kishi", Season: 2, Episode: 7, TVDBID: 401279,
	})
	if err != nil || len(releases) != 1 {
		t.Fatalf("Search() releases=%d err=%v: %+v", len(releases), err, releases)
	}
	if idLookups.Load() != 1 || hashLookups.Load() != 0 {
		t.Fatalf("detail lookups by id=%d hash=%d, want 1/0", idLookups.Load(), hashLookups.Load())
	}
	if releases[0].Seeders != 321 || releases[0].DownloadURL != "https://nyaa.si/download/2148182.torrent" {
		t.Fatalf("fresh Nyaa metadata did not win merge: %+v", releases[0])
	}
}

func TestAnimeBRNyaaMissingOrMismatchedVerificationIsRejected(t *testing.T) {
	tests := []struct {
		name   string
		detail func(http.ResponseWriter)
	}{
		{
			name: "not indexed yet",
			detail: func(w http.ResponseWriter) {
				http.NotFound(w, nil)
			},
		},
		{
			name: "different infohash",
			detail: func(w http.ResponseWriter) {
				detail := verifiedNyaaTestDetail(strings.Repeat("a", 40))
				_ = json.NewEncoder(w).Encode(detail)
			},
		},
		{
			name: "MultiSub without Brazilian track",
			detail: func(w http.ResponseWriter) {
				detail := verifiedNyaaTestDetail(nyaaTestHash)
				detail.Attachments = []animeToshoAttachment{{Type: "subtitle", Info: animeToshoAttachmentInfo{LanguageCode: "eng", Language: "English"}}}
				_ = json.NewEncoder(w).Encode(detail)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newNyaaAnimeBRFixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/json" && r.URL.Query().Get("show") != "torrent" {
					_ = json.NewEncoder(w).Encode([]animeToshoSearchItem{})
					return
				}
				if r.URL.Path == "/json" && r.URL.Query().Get("btih") == nyaaTestHash {
					tt.detail(w)
					return
				}
				http.NotFound(w, r)
			})
			defer server.Close()

			service := newAnimeBRService(nyaaTestConfig(server.URL), nil, server.Client())
			releases, err := service.Search(context.Background(), AnimeBRSearchRequest{Query: "Gaikotsu Kishi", Season: 2, Episode: 7})
			if err != nil {
				t.Fatalf("Search() error = %v", err)
			}
			if len(releases) != 0 {
				t.Fatalf("unverified Nyaa candidate escaped: %+v", releases)
			}
		})
	}
}

func TestAnimeBRNyaaMissingHashLookupUsesShortNegativeCache(t *testing.T) {
	var hashLookups atomic.Int32
	server := newNyaaAnimeBRFixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/json" && r.URL.Query().Get("show") != "torrent" {
			_ = json.NewEncoder(w).Encode([]animeToshoSearchItem{})
			return
		}
		if r.URL.Path == "/json" && r.URL.Query().Get("btih") == nyaaTestHash {
			hashLookups.Add(1)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		http.NotFound(w, r)
	})
	defer server.Close()

	cache := &memoryAnimeBRCache{values: make(map[string][]byte)}
	service := newAnimeBRService(nyaaTestConfig(server.URL), cache, server.Client())
	request := AnimeBRSearchRequest{Query: "Gaikotsu Kishi", Season: 2, Episode: 7}
	for range 2 {
		releases, err := service.Search(context.Background(), request)
		if err != nil || len(releases) != 0 {
			t.Fatalf("Search() releases=%d err=%v, want empty non-error", len(releases), err)
		}
	}
	if got := hashLookups.Load(); got != 1 {
		t.Fatalf("BTIH 404 lookups = %d, want 1 after negative cache", got)
	}
}

func TestAnimeBRNyaaFailureDoesNotBreakAnimeTosho(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Error(w, "temporary Nyaa error", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("show") != "torrent" {
			_ = json.NewEncoder(w).Encode([]animeToshoSearchItem{{
				ID: 630406, Title: "Gaikotsu.Kishi.S02E07.1080p", Status: "complete", InfoHash: nyaaTestHash,
				TorrentURL: "https://animetosho.xyz/download/630406/torrent", Seeders: 5,
			}})
			return
		}
		_ = json.NewEncoder(w).Encode(verifiedNyaaTestDetail(nyaaTestHash))
	}))
	defer server.Close()

	service := newAnimeBRService(nyaaTestConfig(server.URL), nil, server.Client())
	releases, err := service.Search(context.Background(), AnimeBRSearchRequest{Query: "Gaikotsu Kishi", Season: 2, Episode: 7})
	if err != nil || len(releases) != 1 {
		t.Fatalf("Nyaa failure was not fail-open: releases=%d err=%v", len(releases), err)
	}
	if releases[0].Source != "Anime Tosho NEW" {
		t.Fatalf("unexpected source after Nyaa failure: %+v", releases[0])
	}
}

func TestAnimeBRSlowNyaaUsesIndependentFailOpenBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("show") != "torrent" {
			_ = json.NewEncoder(w).Encode([]animeToshoSearchItem{{
				ID: 630406, Title: "Gaikotsu.Kishi.S02E07.1080p", Status: "complete", InfoHash: nyaaTestHash,
				TorrentURL: "https://animetosho.xyz/download/630406/torrent", Seeders: 5,
			}})
			return
		}
		_ = json.NewEncoder(w).Encode(verifiedNyaaTestDetail(nyaaTestHash))
	}))
	defer server.Close()

	config := nyaaTestConfig(server.URL)
	config.NyaaDiscoveryTimeout = 50 * time.Millisecond
	config.SearchTimeout = 2 * time.Second
	service := newAnimeBRService(config, nil, server.Client())
	started := time.Now()
	releases, err := service.Search(context.Background(), AnimeBRSearchRequest{Query: "Gaikotsu Kishi", Season: 2, Episode: 7})
	if err != nil || len(releases) != 1 {
		t.Fatalf("slow Nyaa was not fail-open: releases=%d err=%v", len(releases), err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("Nyaa exceeded its independent discovery budget: elapsed=%v", elapsed)
	}
	if releases[0].Source != "Anime Tosho NEW" {
		t.Fatalf("unexpected source after Nyaa timeout: %+v", releases[0])
	}
}

func TestAnimeBRLiveNyaaDiscoveryGaikotsu(t *testing.T) {
	if os.Getenv("ANIME_BR_LIVE_TEST") != "1" {
		t.Skip("set ANIME_BR_LIVE_TEST=1 to query Nyaa.si and Anime Tosho NEW")
	}
	service := newAnimeBRService(AnimeBRConfig{
		BaseURL:                  defaultAnimeToshoURL,
		NyaaURL:                  defaultNyaaURL,
		NyaaEnabled:              true,
		NyaaSearchCacheTTL:       time.Minute,
		NyaaRequestDelay:         defaultNyaaRequestDelay,
		StrictFilename:           true,
		MaxDetails:               defaultAnimeBRMaxDetails,
		Workers:                  4,
		RequestTimeout:           30 * time.Second,
		SearchTimeout:            60 * time.Second,
		SearchCacheTTL:           time.Minute,
		DetailCacheTTL:           time.Minute,
		UnverifiedDetailCacheTTL: time.Minute,
	}, nil, nil)

	releases, err := service.Search(context.Background(), AnimeBRSearchRequest{
		Query: "Gaikotsu Kishi sama Tadaima Isekai e Odekake chuu II", Season: 2, Episode: 7, TVDBID: 401279,
	})
	if err != nil {
		t.Fatalf("live Nyaa Search() error = %v", err)
	}
	foundNyaa := false
	for _, release := range releases {
		t.Logf("verified live source=%s seeders=%d title=%s", release.Source, release.Seeders, release.Title)
		if strings.Contains(release.Source, "Nyaa.si") {
			foundNyaa = true
		}
		if !strings.Contains(strings.ToUpper(release.Title), "S02E07") || release.PTBRState != "verified" {
			t.Errorf("invalid live release escaped strict verification: %+v", release)
		}
	}
	if !foundNyaa {
		t.Fatalf("no Nyaa-discovered verified release returned: %+v", releases)
	}
}

func nyaaTestConfig(baseURL string) AnimeBRConfig {
	config := testAnimeBRConfig(baseURL)
	config.NyaaURL = baseURL
	config.NyaaEnabled = true
	config.NyaaSearchCacheTTL = time.Minute
	config.NyaaRequestDelay = time.Nanosecond
	config.UnverifiedDetailCacheTTL = time.Minute
	return config
}

func newNyaaAnimeBRFixtureServer(t *testing.T, animeToshoHandler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" && r.URL.Query().Get("page") == "rss" {
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(nyaaTestRSS(nyaaTestHash, "[Erai-raws] Gaikotsu Kishi S02E07 [1080p][MultiSub]", 321)))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		animeToshoHandler(w, r)
	}))
}

func verifiedNyaaTestDetail(infoHash string) animeToshoDetail {
	return animeToshoDetail{
		ID: 630406, Title: "Gaikotsu Kishi S02E07", Status: "complete", InfoHash: infoHash,
		TorrentURL: "https://animetosho.xyz/download/630406/torrent", TotalSize: 1400000000,
		TVDBID: 401279, TVDBSeason: 2,
		Files: []animeToshoFile{{Filename: "[VARYG] Gaikotsu.Kishi.S02E07.1080p.WEB-DL.H.264.mkv", Processed: true}},
		Attachments: []animeToshoAttachment{{
			Type: "subtitle", Info: animeToshoAttachmentInfo{LanguageCode: "por", Language: "Portuguese[BR]"},
		}},
	}
}

func nyaaTestRSS(infoHash, title string, seeders int) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:nyaa="https://nyaa.si/xmlns/nyaa">
  <channel><title>Nyaa</title><item>
    <title>%s</title>
    <link>https://nyaa.si/download/2148182.torrent</link>
    <guid isPermaLink="true">https://nyaa.si/view/2148182</guid>
    <pubDate>Tue, 18 Aug 2026 19:50:26 -0000</pubDate>
    <nyaa:seeders>%d</nyaa:seeders><nyaa:leechers>12</nyaa:leechers><nyaa:downloads>999</nyaa:downloads>
    <nyaa:infoHash>%s</nyaa:infoHash><nyaa:categoryId>1_2</nyaa:categoryId>
    <nyaa:category>Anime - English-translated</nyaa:category><nyaa:size>1.4 GiB</nyaa:size>
    <nyaa:trusted>Yes</nyaa:trusted><nyaa:remake>No</nyaa:remake>
  </item></channel>
</rss>`, title, seeders, infoHash)
}

type memoryAnimeBRCache struct {
	mu     sync.Mutex
	values map[string][]byte
}

func (c *memoryAnimeBRCache) Get(_ context.Context, key string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.values[key]...), nil
}

func (c *memoryAnimeBRCache) SetWithExpiration(_ context.Context, key string, value []byte, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values[key] = append([]byte(nil), value...)
	return nil
}
