package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	animeBRLegacyHash      = "e50fb447df764201c38a287022f7d387d4db22e3"
	animeBRLegacyOtherHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func TestAnimeBRLegacyFallbackUsesBTIHAndNeverCurrentID(t *testing.T) {
	filename := "Example.Show.S01E11.1080p.WEB-DL.mkv"
	candidate := animeToshoSearchItem{
		ID: 335197, Title: "Example Show S01E11 1080p WEB-DL", Status: "complete",
		InfoHash: animeBRLegacyHash, TorrentURL: "https://new.example/335197.torrent",
	}
	primaryDetail := animeBRLegacyPrimaryGap(candidate, 1, 1234)

	var legacyBTIHCalls atomic.Int32
	var legacyIDCalls atomic.Int32
	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") != "" {
			legacyIDCalls.Add(1)
			writeAnimeBRLegacyJSON(t, w, animeBRLegacyDetailPayload(animeBRLegacyOtherHash, filename, 0, false))
			return
		}
		if r.URL.Query().Get("show") != "torrent" || r.URL.Query().Get("btih") != animeBRLegacyHash {
			t.Errorf("unexpected legacy query: %s", r.URL.RawQuery)
			http.NotFound(w, r)
			return
		}
		legacyBTIHCalls.Add(1)
		writeAnimeBRLegacyJSON(t, w, animeBRLegacyDetailPayload(animeBRLegacyHash, filename, 0, false))
	}))
	defer legacy.Close()

	primary := newAnimeBRLegacyPrimaryServer(t, []animeToshoSearchItem{candidate}, map[int64]animeToshoDetail{candidate.ID: primaryDetail})
	defer primary.Close()
	service := newAnimeBRLegacyTestService(primary, legacy.URL)

	releases, err := service.Search(context.Background(), AnimeBRSearchRequest{
		Query: "Example Show", Season: 1, Episode: 11, TVDBID: 1234,
	})
	if err != nil || len(releases) != 1 {
		t.Fatalf("Search() releases=%d err=%v: %+v", len(releases), err, releases)
	}
	if got := legacyBTIHCalls.Load(); got != 1 {
		t.Fatalf("legacy BTIH calls = %d, want 1", got)
	}
	if got := legacyIDCalls.Load(); got != 0 {
		t.Fatalf("legacy ID calls = %d, want 0; IDs from different Anime Tosho instances can collide", got)
	}
	if releases[0].InfoHash != animeBRLegacyHash || releases[0].PTBRState != "verified" {
		t.Fatalf("wrong legacy release identity/evidence: %+v", releases[0])
	}
	if !slices.Contains(releases[0].Evidence, "extracted subtitle: por / Portuguese(Brazil)") {
		t.Fatalf("legacy PT-BR evidence missing: %+v", releases[0].Evidence)
	}
}

func TestAnimeBRLegacyHashMismatchIsRejected(t *testing.T) {
	filename := "Example.Show.S01E11.1080p.WEB-DL.mkv"
	candidate := animeToshoSearchItem{
		ID: 335197, Title: "Example Show S01E11 1080p", Status: "complete", InfoHash: animeBRLegacyHash,
	}
	var legacyCalls atomic.Int32
	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		legacyCalls.Add(1)
		if r.URL.Query().Get("btih") != animeBRLegacyHash {
			t.Errorf("legacy lookup was not keyed by expected BTIH: %s", r.URL.RawQuery)
		}
		writeAnimeBRLegacyJSON(t, w, animeBRLegacyDetailPayload(animeBRLegacyOtherHash, filename, 0, false))
	}))
	defer legacy.Close()

	primary := newAnimeBRLegacyPrimaryServer(t, []animeToshoSearchItem{candidate}, map[int64]animeToshoDetail{
		candidate.ID: animeBRLegacyPrimaryGap(candidate, 1, 1234),
	})
	defer primary.Close()
	service := newAnimeBRLegacyTestService(primary, legacy.URL)

	releases, err := service.Search(context.Background(), AnimeBRSearchRequest{
		Query: "Example Show", Season: 1, Episode: 11, TVDBID: 1234,
	})
	if err != nil {
		t.Fatalf("hash mismatch should fail closed as an empty result, got error: %v", err)
	}
	if len(releases) != 0 {
		t.Fatalf("mismatched legacy hash escaped verification: %+v", releases)
	}
	if got := legacyCalls.Load(); got != 1 {
		t.Fatalf("legacy calls = %d, want 1", got)
	}
}

func TestAnimeBRLegacyNumericForcedDecoding(t *testing.T) {
	tests := []struct {
		name   string
		value  string
		forced bool
	}{
		{name: "numeric zero", value: "0", forced: false},
		{name: "numeric one", value: "1", forced: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var attachment animeToshoAttachment
			raw := `{"type":"subtitle","info":{"lang":"por","name":"Portuguese(Brazil)","forced":` + tt.value + `}}`
			if err := json.Unmarshal([]byte(raw), &attachment); err != nil {
				t.Fatalf("json.Unmarshal() numeric forced=%s error = %v", tt.value, err)
			}
			if bool(attachment.Info.Forced) != tt.forced {
				t.Fatalf("numeric forced=%s decoded as %v, want %v", tt.value, bool(attachment.Info.Forced), tt.forced)
			}
		})
	}
}

func TestAnimeBRLegacyForcedInvalidRepresentationsFailClosed(t *testing.T) {
	for _, value := range []string{`null`, `"0"`, `"1"`, `2`} {
		raw := `{"type":"subtitle","info":{"lang":"por","name":"Portuguese(Brazil)","forced":` + value + `}}`
		var attachment animeToshoAttachment
		if err := json.Unmarshal([]byte(raw), &attachment); err == nil {
			t.Errorf("forced=%s unexpectedly decoded as non-forced", value)
		}
	}
}

func TestAnimeBRLegacyPTBREvidenceRejectsForcedOnly(t *testing.T) {
	tests := []struct {
		name         string
		forced       int
		withMediaPOR bool
		wantRelease  bool
	}{
		{name: "non-forced Brazilian subtitle", forced: 0, withMediaPOR: false, wantRelease: true},
		{name: "forced-only cannot be promoted by MediaInfo", forced: 1, withMediaPOR: true, wantRelease: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := animeToshoSearchItem{
				ID: 335197, Title: "Example Show S01E11 1080p", Status: "complete", InfoHash: animeBRLegacyHash,
			}
			legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeAnimeBRLegacyJSON(t, w, animeBRLegacyDetailPayload(
					animeBRLegacyHash, "Example.Show.S01E11.1080p.mkv", tt.forced, tt.withMediaPOR,
				))
			}))
			defer legacy.Close()
			primary := newAnimeBRLegacyPrimaryServer(t, []animeToshoSearchItem{candidate}, map[int64]animeToshoDetail{
				candidate.ID: animeBRLegacyPrimaryGap(candidate, 1, 1234),
			})
			defer primary.Close()

			releases, err := newAnimeBRLegacyTestService(primary, legacy.URL).Search(context.Background(), AnimeBRSearchRequest{
				Query: "Example Show", Season: 1, Episode: 11, TVDBID: 1234,
			})
			if err != nil {
				t.Fatalf("Search() error = %v", err)
			}
			wantCount := 0
			if tt.wantRelease {
				wantCount = 1
			}
			if len(releases) != wantCount {
				t.Fatalf("releases=%d, want %d: %+v", len(releases), wantCount, releases)
			}
		})
	}
}

func TestAnimeBRLegacySeasonOneBareEpisodeIsAcceptedAndNormalized(t *testing.T) {
	filename := "[Erai-raws] Deatte 5 Byou de Battle - 11 [1080p][Multiple Subtitle][D0F9D129].mkv"
	candidate := animeToshoSearchItem{
		ID: 335197, Title: filename, TorrentName: filename, Status: "complete", InfoHash: animeBRLegacyHash,
		TorrentURL: "https://new.example/335197.torrent",
	}
	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeAnimeBRLegacyJSON(t, w, animeBRLegacyDetailPayload(animeBRLegacyHash, filename, 0, false))
	}))
	defer legacy.Close()
	primary := newAnimeBRLegacyPrimaryServer(t, []animeToshoSearchItem{candidate}, map[int64]animeToshoDetail{
		candidate.ID: animeBRLegacyPrimaryGap(candidate, 1, 391736),
	})
	defer primary.Close()

	releases, err := newAnimeBRLegacyTestService(primary, legacy.URL).Search(context.Background(), AnimeBRSearchRequest{
		Query: "Deatte 5 Byou de Battle", Season: 1, SeasonSpecified: true, Episode: 11, TVDBID: 391736,
	})
	if err != nil || len(releases) != 1 {
		t.Fatalf("Search() releases=%d err=%v: %+v", len(releases), err, releases)
	}
	season, episode, ok := episodeFromFilename(releases[0].Title)
	if !ok || season != 1 || episode != 11 {
		t.Fatalf("legacy S1 title was not normalized to S01E11: %q", releases[0].Title)
	}
	if len(releases[0].Files) != 1 || releases[0].Files[0] != filename {
		t.Fatalf("original legacy filename was not retained for audit: %+v", releases[0].Files)
	}
}

func TestAnimeBRLegacySeasonTwoBareEpisodeRemainsRejected(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		episode  int
	}{
		{name: "bare episode", filename: "Example Show - 11 [1080p].mkv", episode: 11},
		{name: "ambiguous S2 dash episode", filename: "Example Show S2 - 10 [1080p].mkv", episode: 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := animeToshoSearchItem{
				ID: 700, Title: tt.filename, TorrentName: tt.filename, Status: "complete", InfoHash: animeBRLegacyHash,
			}
			legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeAnimeBRLegacyJSON(t, w, animeBRLegacyDetailPayload(animeBRLegacyHash, tt.filename, 0, false))
			}))
			defer legacy.Close()
			primary := newAnimeBRLegacyPrimaryServer(t, []animeToshoSearchItem{candidate}, map[int64]animeToshoDetail{
				candidate.ID: animeBRLegacyPrimaryGap(candidate, 2, 5678),
			})
			defer primary.Close()

			releases, err := newAnimeBRLegacyTestService(primary, legacy.URL).Search(context.Background(), AnimeBRSearchRequest{
				Query: "Example Show", Season: 2, SeasonSpecified: true, Episode: tt.episode, TVDBID: 5678,
			})
			if err != nil {
				t.Fatalf("Search() error = %v", err)
			}
			if len(releases) != 0 {
				t.Fatalf("ambiguous season-two payload escaped strict mode: %+v", releases)
			}
		})
	}
}

func TestAnimeBRLegacyNyaaOnlyCanonicalFilenameIsAccepted(t *testing.T) {
	filename := "Example.Show.S01E11.1080p.WEB-DL.mkv"
	candidate := animeToshoSearchItem{
		Title: filename, TorrentName: filename, Status: "complete", Source: "Nyaa.si",
		InfoHash: animeBRLegacyHash, TorrentURL: "https://nyaa.example/download/42.torrent", NyaaID: 42,
	}
	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeAnimeBRLegacyJSON(t, w, animeBRLegacyDetailPayload(animeBRLegacyHash, filename, 0, false))
	}))
	defer legacy.Close()
	primary := newAnimeBRLegacyPrimaryServer(t, []animeToshoSearchItem{candidate}, map[int64]animeToshoDetail{0: {}})
	defer primary.Close()

	service := newAnimeBRLegacyTestService(primary, legacy.URL)
	detail, err := service.getAnimeToshoDetailForCandidate(context.Background(), context.Background(), candidate)
	if err != nil {
		t.Fatalf("Nyaa-only enrichment error = %v", err)
	}
	release, ok := service.buildVerifiedRelease(candidate, detail, AnimeBRSearchRequest{Query: "Example Show", Season: 1, Episode: 11})
	if !ok {
		t.Fatal("Nyaa-only exact filename was not accepted after exact-BTIH archive enrichment")
	}
	if release.Source != "Nyaa.si + Anime Tosho archive" {
		t.Fatalf("unexpected Nyaa-only source: %q", release.Source)
	}
}

func TestAnimeBRLegacyNyaaOnlyBareFilenameRemainsRejected(t *testing.T) {
	filename := "Example Show - 11 [1080p].mkv"
	candidate := animeToshoSearchItem{
		Title: filename, TorrentName: filename, Status: "complete", Source: "Nyaa.si",
		InfoHash: animeBRLegacyHash, TorrentURL: "https://nyaa.example/download/42.torrent", NyaaID: 42,
	}
	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeAnimeBRLegacyJSON(t, w, animeBRLegacyDetailPayload(animeBRLegacyHash, filename, 0, false))
	}))
	defer legacy.Close()
	primary := newAnimeBRLegacyPrimaryServer(t, []animeToshoSearchItem{candidate}, map[int64]animeToshoDetail{0: {}})
	defer primary.Close()

	service := newAnimeBRLegacyTestService(primary, legacy.URL)
	detail, err := service.getAnimeToshoDetailForCandidate(context.Background(), context.Background(), candidate)
	if err != nil {
		t.Fatalf("Nyaa-only enrichment error = %v", err)
	}
	if release, ok := service.buildVerifiedRelease(candidate, detail, AnimeBRSearchRequest{Query: "Example Show", Season: 1, Episode: 11}); ok {
		t.Fatalf("Nyaa-only bare filename lacked authoritative season metadata: %+v", release)
	}
}

func TestAnimeBRPrimaryVerifiedDetailDoesNotCallLegacy(t *testing.T) {
	candidate := animeToshoSearchItem{
		ID: 800, Title: "Example Show S01E11 1080p", Status: "complete", InfoHash: animeBRLegacyHash,
	}
	detail := animeBRLegacyPrimaryGap(candidate, 1, 1234)
	detail.Files = []animeToshoFile{{Filename: "Example.Show.S01E11.1080p.mkv", Processed: true}}
	detail.Attachments = []animeToshoAttachment{{
		Type: "subtitle", Info: animeToshoAttachmentInfo{LanguageCode: "por", Language: "Portuguese[BR]"},
	}}
	var legacyCalls atomic.Int32
	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		legacyCalls.Add(1)
		http.Error(w, "legacy must not be called", http.StatusInternalServerError)
	}))
	defer legacy.Close()
	primary := newAnimeBRLegacyPrimaryServer(t, []animeToshoSearchItem{candidate}, map[int64]animeToshoDetail{candidate.ID: detail})
	defer primary.Close()

	releases, err := newAnimeBRLegacyTestService(primary, legacy.URL).Search(context.Background(), AnimeBRSearchRequest{
		Query: "Example Show", Season: 1, Episode: 11, TVDBID: 1234,
	})
	if err != nil || len(releases) != 1 {
		t.Fatalf("Search() releases=%d err=%v: %+v", len(releases), err, releases)
	}
	if got := legacyCalls.Load(); got != 0 {
		t.Fatalf("legacy calls = %d, want 0 for a stable primary detail", got)
	}
}

func TestAnimeBRLegacyUnavailableIsFailOpenForPrimaryResults(t *testing.T) {
	gapHash := animeBRLegacyHash
	validHash := animeBRLegacyOtherHash
	gapCandidate := animeToshoSearchItem{ID: 900, Title: "Example Show S01E11 Legacy", Status: "complete", InfoHash: gapHash}
	validCandidate := animeToshoSearchItem{ID: 901, Title: "Example Show S01E11 Primary", Status: "complete", InfoHash: validHash}
	gapDetail := animeBRLegacyPrimaryGap(gapCandidate, 1, 1234)
	validDetail := animeBRLegacyPrimaryGap(validCandidate, 1, 1234)
	validDetail.Files = []animeToshoFile{{Filename: "Example.Show.S01E11.Primary.1080p.mkv", Processed: true}}
	validDetail.Attachments = []animeToshoAttachment{{
		Type: "subtitle", Info: animeToshoAttachmentInfo{LanguageCode: "por", Language: "Portuguese[BR]"},
	}}
	var legacyCalls atomic.Int32
	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		legacyCalls.Add(1)
		http.Error(w, "legacy unavailable", http.StatusBadGateway)
	}))
	defer legacy.Close()
	primary := newAnimeBRLegacyPrimaryServer(t, []animeToshoSearchItem{gapCandidate, validCandidate}, map[int64]animeToshoDetail{
		gapCandidate.ID: gapDetail, validCandidate.ID: validDetail,
	})
	defer primary.Close()

	releases, err := newAnimeBRLegacyTestService(primary, legacy.URL).Search(context.Background(), AnimeBRSearchRequest{
		Query: "Example Show", Season: 1, Episode: 11, TVDBID: 1234,
	})
	if err != nil {
		t.Fatalf("optional legacy outage broke primary results: %v", err)
	}
	if len(releases) != 1 || releases[0].InfoHash != validHash {
		t.Fatalf("primary verified result was not preserved: %+v", releases)
	}
	if got := legacyCalls.Load(); got != 1 {
		t.Fatalf("legacy calls = %d, want 1 only for the incomplete primary candidate", got)
	}
}

func TestAnimeBRSearchQueriesIncludeYearlessEpisodeFallback(t *testing.T) {
	queries := animeBRSearchQueries(AnimeBRSearchRequest{
		Query: "Deatte 5 Byou de Battle (2021) S01E11", Season: 1, Episode: 11,
	})
	if len(queries) == 0 {
		t.Fatal("animeBRSearchQueries() returned no queries")
	}
	for _, want := range []string{
		"Deatte 5 Byou de Battle S01E11",
		"Deatte 5 Byou de Battle 11",
		"Deatte 5 Byou de Battle",
	} {
		if !slices.Contains(queries, want) {
			t.Errorf("yearless fallback %q missing from %#v", want, queries)
		}
	}
	for i, query := range queries {
		if slices.Contains(queries[:i], query) {
			t.Fatalf("duplicate search query %q in %#v", query, queries)
		}
	}
}

func TestAnimeBRLiveHistoricalDeatteS01E11(t *testing.T) {
	if os.Getenv("ANIME_BR_LIVE_TEST") != "1" {
		t.Skip("set ANIME_BR_LIVE_TEST=1 to query the Anime Tosho archive")
	}
	service := newAnimeBRService(AnimeBRConfig{
		BaseURL:              defaultAnimeToshoURL,
		LegacyBaseURL:        defaultLegacyAnimeToshoURL,
		LegacyEnabled:        true,
		LegacyTimeout:        defaultLegacyAnimeToshoTimeout,
		NyaaURL:              defaultNyaaURL,
		NyaaEnabled:          true,
		NyaaSearchCacheTTL:   time.Minute,
		NyaaRequestDelay:     defaultNyaaRequestDelay,
		NyaaDiscoveryTimeout: defaultNyaaDiscoveryTimeout,
		StrictFilename:       true,
		MaxDetails:           defaultAnimeBRMaxDetails,
		Workers:              defaultAnimeBRWorkers,
		RequestTimeout:       20 * time.Second,
		SearchTimeout:        45 * time.Second,
		SearchCacheTTL:       time.Minute,
		DetailCacheTTL:       time.Minute,
	}, nil, nil)

	releases, err := service.Search(context.Background(), AnimeBRSearchRequest{
		Query: "Deatte 5 Byou de Battle", Season: 1, SeasonSpecified: true, Episode: 11,
	})
	if err != nil {
		t.Fatalf("live historical Search() error = %v", err)
	}
	if len(releases) == 0 {
		t.Fatal("live historical Search() returned no verified S01E11 releases")
	}
	for _, release := range releases {
		t.Logf("verified historical release: %s | %s | %v", release.Title, release.Source, release.Evidence)
		if !strings.Contains(strings.ToUpper(release.Title), "S01E11") || !strings.Contains(release.Source, "archive") {
			t.Errorf("historical release was not safely normalized: %+v", release)
		}
	}
}

func newAnimeBRLegacyTestService(primary *httptest.Server, legacyURL string) *AnimeBRService {
	config := testAnimeBRConfig(primary.URL)
	config.NyaaEnabled = false
	config.LegacyBaseURL = legacyURL
	config.LegacyEnabled = true
	config.LegacyTimeout = 2 * time.Second
	config.SearchTimeout = 10 * time.Second
	return newAnimeBRService(config, nil, primary.Client())
}

func newAnimeBRLegacyPrimaryServer(
	t *testing.T,
	items []animeToshoSearchItem,
	details map[int64]animeToshoDetail,
) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/json" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("show") != "torrent" {
			_ = json.NewEncoder(w).Encode(items)
			return
		}
		id := parseAnimeBRLegacyTestID(r.URL.Query().Get("id"))
		detail, ok := details[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(detail)
	}))
}

func parseAnimeBRLegacyTestID(value string) int64 {
	id, _ := strconv.ParseInt(value, 10, 64)
	return id
}

func animeBRLegacyPrimaryGap(item animeToshoSearchItem, season int, tvdbID int64) animeToshoDetail {
	return animeToshoDetail{
		ID: item.ID, Title: item.Title, Status: "complete", InfoHash: item.InfoHash,
		TorrentURL: firstNonEmpty(item.TorrentURL, "https://new.example/"+strconv.FormatInt(item.ID, 10)+".torrent"),
		TVDBSeason: season, TVDBID: tvdbID, TotalSize: 1_446_570_201,
	}
}

func animeBRLegacyDetailPayload(infoHash, filename string, forced int, withMediaPOR bool) []byte {
	mediaSubtitles := []map[string]any{}
	if withMediaPOR {
		mediaSubtitles = append(mediaSubtitles, map[string]any{
			"codec": "ass", "language": "por", "title": "Portuguese(Brazil)",
		})
	}
	payload := map[string]any{
		"title":        filename,
		"timestamp":    int64(1_632_159_038),
		"status":       "complete",
		"is_dupe":      false,
		"deleted":      false,
		"torrent_url":  "https://legacy.example/torrent",
		"torrent_name": filename,
		"info_hash":    infoHash,
		"total_size":   int64(1_446_570_201),
		"num_files":    1,
		"files": []map[string]any{{
			"id": 900156, "filename": filename, "size": int64(1_446_570_201), "processed": true,
			"info": map[string]any{"mediainfoj": map[string]any{"subtitles": mediaSubtitles}},
			"attachments": []map[string]any{{
				"id": 1303624, "type": "subtitle", "size": 40398,
				"info": map[string]any{
					"codec": "ASS", "lang": "por", "name": "Portuguese(Brazil)",
					"default": 0, "enabled": 1, "forced": forced, "trackid": 3, "tracknum": 4,
				},
			}},
		}},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return body
}

func writeAnimeBRLegacyJSON(t *testing.T, w http.ResponseWriter, body []byte) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(body); err != nil {
		t.Errorf("writing legacy fixture: %v", err)
	}
}
