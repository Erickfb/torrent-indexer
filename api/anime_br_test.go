package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestClassifyPTBREvidenceRequiresBrazilianRegion(t *testing.T) {
	file := animeToshoFile{Filename: "Show.S02E10.mkv"}
	tests := []struct {
		name     string
		detail   animeToshoDetail
		verified bool
		probable bool
	}{
		{
			name: "extracted Brazilian attachment",
			detail: animeToshoDetail{Attachments: []animeToshoAttachment{{
				Type: "subtitle",
				Info: animeToshoAttachmentInfo{LanguageCode: "por", Language: "Portuguese[BR]"},
			}}},
			verified: true,
		},
		{
			name: "generic Portuguese is not enough",
			detail: animeToshoDetail{Attachments: []animeToshoAttachment{{
				Type: "subtitle",
				Info: animeToshoAttachmentInfo{LanguageCode: "por", Language: "Portuguese"},
			}}},
			probable: true,
		},
		{
			name:     "MediaInfo Brazilian track",
			detail:   animeToshoDetail{},
			verified: true,
		},
		{
			name:     "MultiSub title alone has no evidence",
			detail:   animeToshoDetail{Title: "Example [MultiSub]"},
			verified: false,
		},
		{
			name: "forced Brazilian subtitle is not enough",
			detail: animeToshoDetail{Attachments: []animeToshoAttachment{{
				Type: "subtitle",
				Info: animeToshoAttachmentInfo{LanguageCode: "por", Language: "Portuguese[BR]", Forced: true},
			}}},
			verified: false,
		},
		{
			name: "forced attachment cannot be promoted by MediaInfo",
			detail: animeToshoDetail{
				Attachments: []animeToshoAttachment{{
					Type: "subtitle",
					Info: animeToshoAttachmentInfo{LanguageCode: "por", Language: "Portuguese[BR]", Forced: true},
				}},
				Files: []animeToshoFile{{
					Filename: "Show.S02E10.mkv",
					Info: animeToshoFileInfo{MediaInfoJ: animeToshoMediaInfo{Subtitles: []animeToshoMediaSubtitle{{
						Language: "por", Title: "Brazilian",
					}}}},
				}},
			},
			verified: false,
		},
	}
	tests[2].detail.Files = []animeToshoFile{file}
	tests[2].detail.Files[0].Info.MediaInfoJ.Subtitles = []animeToshoMediaSubtitle{{
		Language: "por",
		Title:    "Portuguese (Brazilian) / Português (Brasil)",
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detail := tt.detail
			selected := file
			if len(detail.Files) > 0 {
				selected = detail.Files[0]
			} else {
				detail.Files = []animeToshoFile{selected}
			}
			got := classifyPTBREvidence(detail, selected)
			if got.Verified != tt.verified || got.Probable != tt.probable {
				t.Fatalf("evidence = %+v, want verified=%v probable=%v", got, tt.verified, tt.probable)
			}
		})
	}
}

func TestSelectCompatibleVideoFileRejectsAmbiguousSeasonNotation(t *testing.T) {
	files := []animeToshoFile{
		{Filename: "[Erai-raws] Example S2 - 10 [1080p].mkv"},
		{Filename: "Example.S01E10.1080p.mkv"},
	}
	request := AnimeBRSearchRequest{Season: 2, Episode: 10}
	if _, ok := selectCompatibleVideoFile(files, request, true); ok {
		t.Fatal("ambiguous S2 - 10 or wrong S01E10 must not be accepted for S02E10")
	}
	if got, ok := selectCompatibleVideoFile(files, request, false); !ok || !strings.Contains(got.Filename, "S2 - 10") {
		t.Fatalf("non-strict mode should accept the ambiguous fallback explicitly: got=%+v ok=%v", got, ok)
	}

	files = append(files, animeToshoFile{Filename: "Example.S02E10.1080p.mkv"})
	got, ok := selectCompatibleVideoFile(files, request, true)
	if !ok || !strings.Contains(got.Filename, "S02E10") {
		t.Fatalf("selected file = %+v, ok=%v", got, ok)
	}
}

func TestCandidateCouldMatchEpisode(t *testing.T) {
	tests := []struct {
		title string
		want  bool
	}{
		{"Example S02E10 1080p", true},
		{"Example S01E10 1080p", false},
		{"Example Season 2 - 10 [1080p]", true},
		{"Example Season 3 - 10 [1080p]", false},
		{"Example II - 10 [1080p]", true},
	}
	for _, tt := range tests {
		if got := candidateCouldMatchEpisode(tt.title, AnimeBRSearchRequest{Season: 2, Episode: 10}); got != tt.want {
			t.Errorf("candidateCouldMatchEpisode(%q) = %v, want %v", tt.title, got, tt.want)
		}
	}

	seasonRequest := AnimeBRSearchRequest{Season: 2}
	if !candidateCouldMatchEpisode("Example S02E03", seasonRequest) || candidateCouldMatchEpisode("Example S01E03", seasonRequest) {
		t.Fatal("season-only candidate filtering did not enforce season 2")
	}
	files := []animeToshoFile{{Filename: "Example.S01E03.mkv"}, {Filename: "Example.S02E03.mkv"}}
	got, ok := selectCompatibleVideoFile(files, seasonRequest, true)
	if !ok || !strings.Contains(got.Filename, "S02E03") {
		t.Fatalf("season-only file selection = %+v, ok=%v", got, ok)
	}
}

func TestLongEpisodesRevisionsAndSpecials(t *testing.T) {
	longRequest := AnimeBRSearchRequest{Season: 1, Episode: 1173}
	longFile := animeToshoFile{Filename: "One.Piece.S01E1173v2.1080p.mkv"}
	if got, ok := selectCompatibleVideoFile([]animeToshoFile{longFile}, longRequest, true); !ok || got.Filename != longFile.Filename {
		t.Fatalf("long revised episode was not selected: got=%+v ok=%v", got, ok)
	}

	specialRequest := AnimeBRSearchRequest{Season: 0, SeasonSpecified: true, Episode: 4}
	specialFile := animeToshoFile{Filename: "Example.Show.S00E04.1080p.mkv"}
	if got, ok := selectCompatibleVideoFile([]animeToshoFile{specialFile}, specialRequest, true); !ok || got.Filename != specialFile.Filename {
		t.Fatalf("special episode was not selected: got=%+v ok=%v", got, ok)
	}
}

func TestAnimeBRRequestExtractsEpisodeMarkerFromRawQuery(t *testing.T) {
	request, err := animeBRRequestFromQuery(url.Values{"q": {"Example Show S00E04"}})
	if err != nil {
		t.Fatalf("animeBRRequestFromQuery() error = %v", err)
	}
	if !request.SeasonSpecified || request.Season != 0 || request.Episode != 4 {
		t.Fatalf("request = %+v, want explicit S00E04", request)
	}
}

func TestAnimeBRSearchQueriesPrioritizeExactEpisode(t *testing.T) {
	queries := animeBRSearchQueries(AnimeBRSearchRequest{Query: "One Piece S01E1173", Season: 1, Episode: 1173})
	want := []string{"One Piece S01E1173", "One Piece 1173", "One Piece"}
	if len(queries) != len(want) {
		t.Fatalf("queries = %#v, want %#v", queries, want)
	}
	for i := range want {
		if queries[i] != want[i] {
			t.Fatalf("queries = %#v, want %#v", queries, want)
		}
	}
}

func TestAnimeBRSearchReturnsOnlyVerifiedCompatibleRelease(t *testing.T) {
	server := newAnimeBRFixtureServer(t)
	defer server.Close()
	service := newAnimeBRService(testAnimeBRConfig(server.URL), nil, server.Client())

	releases, err := service.Search(context.Background(), AnimeBRSearchRequest{
		Query: "Example Show S02E10", Season: 2, Episode: 10, TVDBID: 1234,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(releases) != 1 {
		t.Fatalf("len(releases) = %d, want 1: %+v", len(releases), releases)
	}
	release := releases[0]
	if !strings.Contains(release.Title, "S02E10") || !strings.Contains(release.Title, "[Brazilian]") {
		t.Fatalf("title = %q", release.Title)
	}
	if release.PTBRState != "verified" || release.TVDBID != 1234 {
		t.Fatalf("release metadata = %+v", release)
	}
}

func TestAnimeBRDetailCacheTTLRefreshesUnverifiedMetadata(t *testing.T) {
	const (
		verifiedTTL   = 48 * time.Hour
		unverifiedTTL = 3 * time.Minute
	)
	baseDetail := animeToshoDetail{
		Status: "complete",
		Files: []animeToshoFile{{
			Filename:  "Example.Show.S02E07.1080p.WEB-DL.mkv",
			Processed: true,
		}},
	}
	verifiedDetail := baseDetail
	verifiedDetail.Attachments = []animeToshoAttachment{{
		Type: "subtitle",
		Info: animeToshoAttachmentInfo{LanguageCode: "por", Language: "Portuguese[BR]"},
	}}
	forcedOnlyDetail := baseDetail
	forcedOnlyDetail.Attachments = []animeToshoAttachment{{
		Type: "subtitle",
		Info: animeToshoAttachmentInfo{LanguageCode: "por", Language: "Portuguese[BR]", Forced: true},
	}}
	unprocessedDetail := verifiedDetail
	unprocessedDetail.Files = append([]animeToshoFile(nil), verifiedDetail.Files...)
	unprocessedDetail.Files[0].Processed = false

	service := newAnimeBRService(AnimeBRConfig{
		BaseURL:                  "https://example.invalid",
		DetailCacheTTL:           verifiedTTL,
		UnverifiedDetailCacheTTL: unverifiedTTL,
	}, nil, nil)
	tests := []struct {
		name   string
		detail animeToshoDetail
		want   time.Duration
	}{
		{name: "verified and processed", detail: verifiedDetail, want: verifiedTTL},
		{name: "metadata extraction not finished", detail: baseDetail, want: unverifiedTTL},
		{name: "forced subtitle only", detail: forcedOnlyDetail, want: unverifiedTTL},
		{name: "video not processed", detail: unprocessedDetail, want: unverifiedTTL},
		{name: "upstream still pending", detail: animeToshoDetail{Status: "pending"}, want: unverifiedTTL},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := service.animeBRDetailCacheTTL(tt.detail); got != tt.want {
				t.Fatalf("detail cache TTL = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAnimeBRDetailResponseUsesAdaptiveCacheTTL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		detail := animeToshoDetail{
			ID:     7,
			Status: "complete",
			Files: []animeToshoFile{{
				Filename:  "Example.Show.S02E07.1080p.WEB-DL.mkv",
				Processed: true,
			}},
		}
		if r.URL.Query().Get("id") == "8" {
			detail.ID = 8
			detail.Attachments = []animeToshoAttachment{{
				Type: "subtitle",
				Info: animeToshoAttachmentInfo{LanguageCode: "por", Language: "Portuguese[BR]"},
			}}
		}
		_ = json.NewEncoder(w).Encode(detail)
	}))
	defer server.Close()

	const (
		verifiedTTL   = 48 * time.Hour
		unverifiedTTL = 3 * time.Minute
	)
	for _, tt := range []struct {
		id   int64
		want time.Duration
	}{
		{id: 7, want: unverifiedTTL},
		{id: 8, want: verifiedTTL},
	} {
		cache := &recordingAnimeBRCache{}
		service := newAnimeBRService(AnimeBRConfig{
			BaseURL:                  server.URL,
			DetailCacheTTL:           verifiedTTL,
			UnverifiedDetailCacheTTL: unverifiedTTL,
		}, cache, server.Client())
		if _, err := service.getAnimeToshoDetail(context.Background(), tt.id); err != nil {
			t.Fatalf("getAnimeToshoDetail(%d) error = %v", tt.id, err)
		}
		if cache.ttl != tt.want {
			t.Fatalf("detail %d cached for %v, want %v", tt.id, cache.ttl, tt.want)
		}
	}
}

func TestAnimeBRCacheKeyHasVersionedNamespace(t *testing.T) {
	key := animeBRCacheKey("https://feed.animetosho.xyz/json?show=torrent&id=630216")
	if !strings.HasPrefix(key, "anime_br:v3:http:") {
		t.Fatalf("cache key %q does not invalidate the previous namespace", key)
	}
}

func TestAnimeBRLiveMushokuS03E07(t *testing.T) {
	if os.Getenv("ANIME_BR_LIVE_TEST") != "1" {
		t.Skip("set ANIME_BR_LIVE_TEST=1 to query Anime Tosho NEW")
	}
	service := newAnimeBRService(AnimeBRConfig{
		BaseURL:        defaultAnimeToshoURL,
		StrictFilename: true,
		MaxDetails:     24,
		Workers:        4,
		RequestTimeout: 30 * time.Second,
		SearchCacheTTL: time.Minute,
		DetailCacheTTL: time.Minute,
	}, nil, nil)

	releases, err := service.Search(context.Background(), AnimeBRSearchRequest{
		Query: "Mushoku Tensei III", Season: 3, Episode: 7, TVDBID: 371310,
	})
	if err != nil {
		t.Fatalf("live Search() error = %v", err)
	}
	if len(releases) == 0 {
		t.Fatal("live Search() returned no verified S03E07 releases")
	}
	for _, release := range releases {
		t.Logf("verified live release: %s | %v", release.Title, release.Evidence)
		if !strings.Contains(strings.ToUpper(release.Title), "S03E07") {
			t.Errorf("incompatible live release escaped strict mode: %q", release.Title)
		}
		if release.PTBRState != "verified" || len(release.Evidence) == 0 {
			t.Errorf("release lacks PT-BR proof: %+v", release)
		}
	}
}

func TestAnimeBRLiveBeginningS02E10(t *testing.T) {
	if os.Getenv("ANIME_BR_LIVE_TEST") != "1" {
		t.Skip("set ANIME_BR_LIVE_TEST=1 to query Anime Tosho NEW")
	}
	service := newAnimeBRService(AnimeBRConfig{
		BaseURL:        defaultAnimeToshoURL,
		StrictFilename: true,
		MaxDetails:     defaultAnimeBRMaxDetails,
		Workers:        4,
		RequestTimeout: 30 * time.Second,
		SearchCacheTTL: time.Minute,
		DetailCacheTTL: time.Minute,
	}, nil, nil)

	releases, err := service.Search(context.Background(), AnimeBRSearchRequest{
		Query: "The Beginning After the End", Season: 2, Episode: 10, TVDBID: 455684,
	})
	if err != nil {
		t.Fatalf("live Search() error = %v", err)
	}
	if len(releases) == 0 {
		t.Fatal("live Search() returned no verified S02E10 releases")
	}
	for _, release := range releases {
		t.Logf("verified live release: %s | %v", release.Title, release.Evidence)
		upperTitle := strings.ToUpper(release.Title)
		if !strings.Contains(upperTitle, "S02E10") || strings.Contains(upperTitle, " S2 - 10") {
			t.Errorf("incompatible live release escaped strict mode: %q", release.Title)
		}
	}
}

func TestAnimeBRLiveOnePieceS01E1173(t *testing.T) {
	if os.Getenv("ANIME_BR_LIVE_TEST") != "1" {
		t.Skip("set ANIME_BR_LIVE_TEST=1 to query Anime Tosho NEW")
	}
	service := newAnimeBRService(AnimeBRConfig{
		BaseURL:        defaultAnimeToshoURL,
		StrictFilename: true,
		MaxDetails:     defaultAnimeBRMaxDetails,
		Workers:        4,
		RequestTimeout: 30 * time.Second,
		SearchTimeout:  45 * time.Second,
	}, nil, nil)

	releases, err := service.Search(context.Background(), AnimeBRSearchRequest{
		Query: "One Piece", Season: 1, Episode: 1173,
	})
	if err != nil {
		t.Fatalf("live Search() error = %v", err)
	}
	if len(releases) == 0 {
		t.Fatal("live Search() returned no verified S01E1173 releases")
	}
	for _, release := range releases {
		t.Logf("verified long-episode release: %s", release.Title)
		if !strings.Contains(strings.ToUpper(release.Title), "S01E1173") {
			t.Errorf("incompatible long episode escaped strict mode: %q", release.Title)
		}
	}
}

func TestAnimeBRLiveGaikotsuS02E07(t *testing.T) {
	if os.Getenv("ANIME_BR_LIVE_TEST") != "1" {
		t.Skip("set ANIME_BR_LIVE_TEST=1 to query Anime Tosho NEW")
	}
	service := newAnimeBRService(AnimeBRConfig{
		BaseURL:        defaultAnimeToshoURL,
		StrictFilename: true,
		MaxDetails:     defaultAnimeBRMaxDetails,
		Workers:        4,
		RequestTimeout: 30 * time.Second,
		SearchTimeout:  45 * time.Second,
	}, nil, nil)

	releases, err := service.Search(context.Background(), AnimeBRSearchRequest{
		Query:  "Gaikotsu Kishi sama Tadaima Isekai e Odekake chuu II",
		Season: 2, Episode: 7, TVDBID: 401279,
	})
	if err != nil {
		t.Fatalf("live Search() error = %v", err)
	}
	if len(releases) == 0 {
		t.Fatal("live Search() returned no verified S02E07 releases")
	}
	foundPreferredH264 := false
	for _, release := range releases {
		t.Logf("verified Gaikotsu release: %s | %v", release.Title, release.Evidence)
		upperTitle := strings.ToUpper(release.Title)
		if !strings.Contains(upperTitle, "S02E07") {
			t.Errorf("incompatible live release escaped strict mode: %q", release.Title)
		}
		if strings.Contains(upperTitle, "VARYG") || strings.Contains(upperTitle, "TOONSHUB") || strings.Contains(upperTitle, "ANOZU") {
			foundPreferredH264 = true
		}
	}
	if !foundPreferredH264 {
		t.Errorf("verified releases did not include the known VARYG, ToonsHub, or AnoZu H.264 options")
	}
}

func TestAnimeBRLiveEmptySearchForProwlarrConnection(t *testing.T) {
	if os.Getenv("ANIME_BR_LIVE_TEST") != "1" {
		t.Skip("set ANIME_BR_LIVE_TEST=1 to query Anime Tosho NEW")
	}
	service := newAnimeBRService(AnimeBRConfig{
		BaseURL:        defaultAnimeToshoURL,
		StrictFilename: true,
		MaxDetails:     defaultAnimeBRMaxDetails,
		Workers:        4,
		RequestTimeout: 30 * time.Second,
	}, nil, nil)

	releases, err := service.Search(context.Background(), AnimeBRSearchRequest{})
	if err != nil {
		t.Fatalf("live empty Search() error = %v", err)
	}
	if len(releases) == 0 {
		t.Fatal("live empty Search() returned no result for Prowlarr connection test")
	}
	t.Logf("empty search returned %d verified compatible releases", len(releases))
}

func testAnimeBRConfig(baseURL string) AnimeBRConfig {
	return AnimeBRConfig{
		BaseURL:        baseURL,
		StrictFilename: true,
		MaxDetails:     10,
		Workers:        2,
		RequestTimeout: 2 * time.Second,
		SearchTimeout:  5 * time.Second,
		SearchCacheTTL: time.Minute,
		DetailCacheTTL: time.Minute,
	}
}

func newAnimeBRFixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("show") != "torrent" {
			_ = json.NewEncoder(w).Encode([]animeToshoSearchItem{
				{ID: 1, Title: "Example Show S02E10 1080p WEB-DL H.264", Status: "complete", TorrentURL: "https://downloads.example/1.torrent", InfoHash: strings.Repeat("1", 40), MagnetURI: "magnet:?xt=urn:btih:" + strings.Repeat("1", 40) + "&tr=https://tracker.example/announce", Seeders: 50, TotalSize: 1000, Timestamp: 1_700_000_000},
				{ID: 2, Title: "Example Show Season 2 - 10 1080p WEB-DL", Status: "complete", TorrentURL: "https://downloads.example/2.torrent", InfoHash: strings.Repeat("2", 40), Seeders: 100, TotalSize: 1000, Timestamp: 1_700_000_000},
				{ID: 3, Title: "Example Show S02E10 1080p WEB-DL", Status: "complete", TorrentURL: "https://downloads.example/3.torrent", InfoHash: strings.Repeat("3", 40), Seeders: 75, TotalSize: 1000, Timestamp: 1_700_000_000},
			})
			return
		}

		id := r.URL.Query().Get("id")
		detail := animeToshoDetail{
			Status: "complete", TVDBID: 1234, TVDBSeason: 2, TotalSize: 1000, Timestamp: 1_700_000_000,
		}
		switch id {
		case "1":
			detail.ID = 1
			detail.Title = "Example Show S02E10"
			detail.InfoHash = strings.Repeat("1", 40)
			detail.TorrentURL = "https://downloads.example/1.torrent"
			detail.MagnetURI = "magnet:?xt=urn:btih:" + strings.Repeat("1", 40) + "&tr=https://tracker.example/announce"
			detail.Files = []animeToshoFile{{Filename: "Example.Show.S02E10.1080p.WEB-DL.H.264.mkv", Processed: true}}
			detail.Attachments = []animeToshoAttachment{{Type: "subtitle", Info: animeToshoAttachmentInfo{LanguageCode: "por", Language: "Portuguese[BR]"}}}
		case "2":
			detail.ID = 2
			detail.InfoHash = strings.Repeat("2", 40)
			detail.TorrentURL = "https://downloads.example/2.torrent"
			detail.Files = []animeToshoFile{{Filename: "Example Show S2 - 10 [1080p].mkv", Processed: true}}
			detail.Attachments = []animeToshoAttachment{{Type: "subtitle", Info: animeToshoAttachmentInfo{LanguageCode: "por", Language: "Portuguese[BR]"}}}
		case "3":
			detail.ID = 3
			detail.InfoHash = strings.Repeat("3", 40)
			detail.TorrentURL = "https://downloads.example/3.torrent"
			detail.Files = []animeToshoFile{{Filename: "Example.Show.S02E10.1080p.WEB-DL.mkv", Processed: true}}
			detail.Attachments = []animeToshoAttachment{{Type: "subtitle", Info: animeToshoAttachmentInfo{LanguageCode: "eng", Language: "English"}}}
		default:
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(detail)
	}))
	return server
}

type recordingAnimeBRCache struct {
	key string
	ttl time.Duration
}

func (c *recordingAnimeBRCache) Get(context.Context, string) ([]byte, error) {
	return nil, nil
}

func (c *recordingAnimeBRCache) SetWithExpiration(_ context.Context, key string, _ []byte, ttl time.Duration) error {
	c.key = key
	c.ttl = ttl
	return nil
}
