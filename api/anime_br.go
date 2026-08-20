package handler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Erickfb/torrent-indexer/cache"
	"github.com/Erickfb/torrent-indexer/logging"
)

const (
	defaultAnimeToshoURL                   = "https://feed.animetosho.xyz"
	defaultAnimeBRMaxDetails               = 48
	defaultAnimeBRWorkers                  = 4
	defaultAnimeBRUnverifiedDetailCacheTTL = 10 * time.Minute
	maxAnimeBRResponseBytes                = 16 << 20
	animeBRCacheVersion                    = "v2"
)

var (
	standardEpisodeRE = regexp.MustCompile(`(?i)\bS0*(\d{1,3})E0*(\d{1,5})(?:v\d+)?\b`)
	seasonDashRE      = regexp.MustCompile(`(?i)\bSeason\s*0*(\d{1,3})\b.*?\s-\s*0*(\d{1,5})(?:\D|$)`)
	bareEpisodeRE     = regexp.MustCompile(`(?i)\s-\s*0*(\d{1,5})(?:v\d+)?(?:\s|\[|\(|$)`)
	queryEpisodeRE    = regexp.MustCompile(`(?i)\bS0*\d{1,3}E0*\d{1,5}\b`)
	brazilianLabelRE  = regexp.MustCompile(`(?i)(brazil|brasil|pt[\s._-]*br|portuguese\s*[\[(]\s*br\s*[\])]|portugu[eê]s\s*[\[(]\s*br(?:asil)?\s*[\])])`)
	ptBRFileRE        = regexp.MustCompile(`(?i)(?:^|[. _-])(?:pt[._-]?br|por[._-]?br|brazil(?:ian)?)(?:[. _-]|$)`)
)

type animeBRCache interface {
	Get(context.Context, string) ([]byte, error)
	SetWithExpiration(context.Context, string, []byte, time.Duration) error
}

type AnimeBRConfig struct {
	BaseURL                  string
	StrictFilename           bool
	MaxDetails               int
	Workers                  int
	RequestTimeout           time.Duration
	SearchTimeout            time.Duration
	SearchCacheTTL           time.Duration
	DetailCacheTTL           time.Duration
	UnverifiedDetailCacheTTL time.Duration
}

type AnimeBRService struct {
	config AnimeBRConfig
	cache  animeBRCache
	client *http.Client
}

type AnimeBRSearchRequest struct {
	Query           string
	Season          int
	SeasonSpecified bool
	Episode         int
	TVDBID          int64
}

type AnimeBRRelease struct {
	ID                int64     `json:"id"`
	Title             string    `json:"title"`
	OriginalTitle     string    `json:"original_title"`
	Details           string    `json:"details"`
	DownloadURL       string    `json:"download_url"`
	MagnetURL         string    `json:"magnet_url"`
	InfoHash          string    `json:"info_hash"`
	Published         time.Time `json:"published"`
	Size              int64     `json:"size"`
	Seeders           int       `json:"seeders"`
	Leechers          int       `json:"leechers"`
	Season            int       `json:"season,omitempty"`
	Episode           int       `json:"episode,omitempty"`
	TVDBID            int64     `json:"tvdb_id,omitempty"`
	Source            string    `json:"source"`
	PTBRState         string    `json:"ptbr_state"`
	SubtitleLanguages []string  `json:"subtitle_languages"`
	Evidence          []string  `json:"evidence"`
	Files             []string  `json:"files"`
	Score             int       `json:"score"`
}

type animeToshoSearchItem struct {
	ID                     int64  `json:"id"`
	Title                  string `json:"title"`
	Link                   string `json:"link"`
	Timestamp              int64  `json:"timestamp"`
	Status                 string `json:"status"`
	TorrentURL             string `json:"torrent_url"`
	TorrentName            string `json:"torrent_name"`
	InfoHash               string `json:"info_hash"`
	MagnetURI              string `json:"magnet_uri"`
	Seeders                int    `json:"seeders"`
	Leechers               int    `json:"leechers"`
	TotalSize              int64  `json:"total_size"`
	TorrentDownloadedCount int    `json:"torrent_downloaded_count"`
}

type animeToshoDetail struct {
	ID          int64                  `json:"id"`
	Title       string                 `json:"title"`
	Link        string                 `json:"link"`
	Timestamp   int64                  `json:"timestamp"`
	Status      string                 `json:"status"`
	Deleted     bool                   `json:"deleted"`
	IsBatch     bool                   `json:"is_batch"`
	IsDupe      bool                   `json:"is_dupe"`
	TorrentURL  string                 `json:"torrent_url"`
	TorrentName string                 `json:"torrent_name"`
	InfoHash    string                 `json:"info_hash"`
	MagnetURI   string                 `json:"magnet_uri"`
	Seeders     int                    `json:"seeders"`
	Leechers    int                    `json:"leechers"`
	TotalSize   int64                  `json:"total_size"`
	TVDBID      int64                  `json:"tvdbid"`
	TVDBSeason  int                    `json:"tvdb_season"`
	Attachments []animeToshoAttachment `json:"attachments"`
	Files       []animeToshoFile       `json:"files"`
}

type animeToshoAttachment struct {
	Type string                   `json:"type"`
	URL  string                   `json:"url"`
	Info animeToshoAttachmentInfo `json:"info"`
}

type animeToshoAttachmentInfo struct {
	Language     string `json:"language"`
	LanguageCode string `json:"language_code"`
	Lang         string `json:"lang"`
	Name         string `json:"name"`
	Format       string `json:"format"`
	Forced       bool   `json:"forced"`
}

type animeToshoFile struct {
	Filename    string                 `json:"filename"`
	Size        int64                  `json:"size"`
	Processed   bool                   `json:"processed"`
	Info        animeToshoFileInfo     `json:"info"`
	Attachments []animeToshoAttachment `json:"attachments"`
}

type animeToshoFileInfo struct {
	MediaInfoJ animeToshoMediaInfo `json:"mediainfoj"`
}

type animeToshoMediaInfo struct {
	Subtitles []animeToshoMediaSubtitle `json:"subtitles"`
}

type animeToshoMediaSubtitle struct {
	Codec    string `json:"codec"`
	Language string `json:"language"`
	Title    string `json:"title"`
}

type animeBREvidence struct {
	Verified bool
	Probable bool
	Reasons  []string
}

func NewAnimeBRService(redis *cache.Redis) *AnimeBRService {
	config := AnimeBRConfig{
		BaseURL:                  envOrDefault("ANIME_BR_ANIMETOSHO_URL", defaultAnimeToshoURL),
		StrictFilename:           !strings.EqualFold(os.Getenv("ANIME_BR_STRICT_FILENAME"), "false"),
		MaxDetails:               envIntOrDefault("ANIME_BR_MAX_DETAILS", defaultAnimeBRMaxDetails),
		Workers:                  envIntOrDefault("ANIME_BR_DETAIL_CONCURRENCY", defaultAnimeBRWorkers),
		RequestTimeout:           time.Duration(envIntOrDefault("ANIME_BR_TIMEOUT_SECONDS", 20)) * time.Second,
		SearchTimeout:            time.Duration(envIntOrDefault("ANIME_BR_SEARCH_TIMEOUT_SECONDS", 45)) * time.Second,
		SearchCacheTTL:           5 * time.Minute,
		DetailCacheTTL:           7 * 24 * time.Hour,
		UnverifiedDetailCacheTTL: time.Duration(envIntOrDefault("ANIME_BR_UNVERIFIED_DETAIL_CACHE_SECONDS", int(defaultAnimeBRUnverifiedDetailCacheTTL/time.Second))) * time.Second,
	}

	return newAnimeBRService(config, redis, nil)
}

func newAnimeBRService(config AnimeBRConfig, c animeBRCache, client *http.Client) *AnimeBRService {
	config.BaseURL = strings.TrimRight(config.BaseURL, "/")
	if config.MaxDetails <= 0 {
		config.MaxDetails = defaultAnimeBRMaxDetails
	}
	if config.Workers <= 0 {
		config.Workers = defaultAnimeBRWorkers
	}
	if config.RequestTimeout <= 0 {
		config.RequestTimeout = 20 * time.Second
	}
	if config.SearchTimeout <= 0 {
		config.SearchTimeout = 45 * time.Second
	}
	if config.SearchCacheTTL <= 0 {
		config.SearchCacheTTL = 5 * time.Minute
	}
	if config.DetailCacheTTL <= 0 {
		config.DetailCacheTTL = 7 * 24 * time.Hour
	}
	if config.UnverifiedDetailCacheTTL <= 0 {
		config.UnverifiedDetailCacheTTL = defaultAnimeBRUnverifiedDetailCacheTTL
	}
	if client == nil {
		client = &http.Client{Timeout: config.RequestTimeout}
	}
	return &AnimeBRService{config: config, cache: c, client: client}
}

func (s *AnimeBRService) Search(ctx context.Context, request AnimeBRSearchRequest) ([]AnimeBRRelease, error) {
	ctx, cancel := context.WithTimeout(ctx, s.config.SearchTimeout)
	defer cancel()

	items, err := s.searchAnimeToshoForRequest(ctx, request)
	if err != nil {
		return nil, err
	}

	candidates := make([]animeToshoSearchItem, 0, len(items))
	for _, item := range items {
		if !strings.EqualFold(item.Status, "complete") || !validInfoHash(item.InfoHash) {
			continue
		}
		if !candidateCouldMatchEpisode(item.Title+" "+item.TorrentName, request) {
			continue
		}
		candidates = append(candidates, item)
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		iScore := animeBRQualityScore(candidates[i].Title, candidates[i].Seeders)
		jScore := animeBRQualityScore(candidates[j].Title, candidates[j].Seeders)
		if iScore == jScore {
			return candidates[i].Timestamp > candidates[j].Timestamp
		}
		return iScore > jScore
	})
	if len(candidates) > s.config.MaxDetails {
		candidates = candidates[:s.config.MaxDetails]
	}

	type detailResult struct {
		release *AnimeBRRelease
		err     error
	}
	jobs := make(chan animeToshoSearchItem)
	results := make(chan detailResult, len(candidates))
	workers := min(s.config.Workers, len(candidates))
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				detail, detailErr := s.getAnimeToshoDetail(ctx, item.ID)
				if detailErr != nil {
					results <- detailResult{err: detailErr}
					continue
				}
				release, accepted := s.buildVerifiedRelease(item, detail, request)
				if accepted {
					results <- detailResult{release: &release}
				} else {
					results <- detailResult{}
				}
			}
		}()
	}
	go func() {
		for _, item := range candidates {
			jobs <- item
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	releasesByHash := make(map[string]AnimeBRRelease)
	var detailErrors []error
	successfulDetails := 0
	for result := range results {
		if result.err != nil {
			detailErrors = append(detailErrors, result.err)
			continue
		}
		successfulDetails++
		if result.release == nil {
			continue
		}
		hash := strings.ToLower(result.release.InfoHash)
		current, exists := releasesByHash[hash]
		if !exists || result.release.Score > current.Score {
			releasesByHash[hash] = *result.release
		}
	}

	releases := make([]AnimeBRRelease, 0, len(releasesByHash))
	for _, release := range releasesByHash {
		releases = append(releases, release)
	}
	sort.SliceStable(releases, func(i, j int) bool {
		if releases[i].Score == releases[j].Score {
			if releases[i].Seeders == releases[j].Seeders {
				return releases[i].Published.After(releases[j].Published)
			}
			return releases[i].Seeders > releases[j].Seeders
		}
		return releases[i].Score > releases[j].Score
	})

	if successfulDetails == 0 && len(detailErrors) > 0 {
		return nil, fmt.Errorf("failed to enrich Anime BR candidates: %w", errors.Join(detailErrors...))
	}
	if len(detailErrors) > 0 {
		logging.Debug().Int("failed", len(detailErrors)).Int("succeeded", successfulDetails).Msg("Some Anime BR candidate details could not be enriched")
	}
	return releases, nil
}

func (s *AnimeBRService) searchAnimeToshoForRequest(ctx context.Context, request AnimeBRSearchRequest) ([]animeToshoSearchItem, error) {
	queries := animeBRSearchQueries(request)
	itemsByKey := make(map[string]animeToshoSearchItem)
	var searchErrors []error
	for _, query := range queries {
		items, err := s.searchAnimeTosho(ctx, query)
		if err != nil {
			searchErrors = append(searchErrors, err)
			continue
		}
		for _, item := range items {
			key := strings.ToLower(strings.TrimSpace(item.InfoHash))
			if key == "" {
				key = "id:" + strconv.FormatInt(item.ID, 10)
			}
			if current, exists := itemsByKey[key]; !exists || item.Timestamp > current.Timestamp {
				itemsByKey[key] = item
			}
		}
	}
	if len(itemsByKey) == 0 && len(searchErrors) == len(queries) {
		return nil, errors.Join(searchErrors...)
	}
	items := make([]animeToshoSearchItem, 0, len(itemsByKey))
	for _, item := range itemsByKey {
		items = append(items, item)
	}
	return items, nil
}

func animeBRSearchQueries(request AnimeBRSearchRequest) []string {
	base := cleanAnimeBRQuery(request.Query)
	queries := make([]string, 0, 3)
	add := func(query string) {
		query = strings.Join(strings.Fields(query), " ")
		if !slices.Contains(queries, query) {
			queries = append(queries, query)
		}
	}
	hasSeason := request.SeasonSpecified || request.Season > 0
	if hasSeason && request.Episode > 0 {
		add(fmt.Sprintf("%s S%02dE%02d", base, request.Season, request.Episode))
		add(fmt.Sprintf("%s %02d", base, request.Episode))
	} else if hasSeason {
		add(fmt.Sprintf("%s S%02d", base, request.Season))
	}
	add(base)
	return queries
}

func (s *AnimeBRService) buildVerifiedRelease(item animeToshoSearchItem, detail animeToshoDetail, request AnimeBRSearchRequest) (AnimeBRRelease, bool) {
	if detail.Deleted || detail.IsDupe || detail.IsBatch || !strings.EqualFold(detail.Status, "complete") {
		return AnimeBRRelease{}, false
	}
	if request.TVDBID > 0 && detail.TVDBID > 0 && request.TVDBID != detail.TVDBID {
		return AnimeBRRelease{}, false
	}
	if request.Season > 0 && detail.TVDBSeason > 0 && request.Season != detail.TVDBSeason {
		return AnimeBRRelease{}, false
	}

	matchingFile, ok := selectCompatibleVideoFile(detail.Files, request, s.config.StrictFilename)
	if !ok {
		return AnimeBRRelease{}, false
	}
	evidence := classifyPTBREvidence(detail, matchingFile)
	if !evidence.Verified {
		return AnimeBRRelease{}, false
	}

	title := strings.TrimSpace(item.Title)
	if matchingFile.Filename != "" {
		title = strings.TrimSuffix(filepath.Base(matchingFile.Filename), filepath.Ext(matchingFile.Filename))
	}
	if title == "" {
		title = strings.TrimSpace(detail.Title)
	}
	if !strings.Contains(strings.ToLower(title), "[brazilian]") {
		title += " [Brazilian]"
	}

	downloadURL := firstNonEmpty(detail.TorrentURL, item.TorrentURL, detail.MagnetURI, item.MagnetURI)
	if downloadURL == "" {
		return AnimeBRRelease{}, false
	}
	infoHash := strings.ToLower(firstNonEmpty(detail.InfoHash, item.InfoHash))
	if !validInfoHash(infoHash) {
		return AnimeBRRelease{}, false
	}

	season, episode := request.Season, request.Episode
	if season == 0 || episode == 0 {
		if parsedSeason, parsedEpisode, found := episodeFromFilename(matchingFile.Filename); found {
			season, episode = parsedSeason, parsedEpisode
		}
	}
	fileNames := make([]string, 0, len(detail.Files))
	for _, file := range detail.Files {
		if strings.TrimSpace(file.Filename) != "" {
			fileNames = append(fileNames, file.Filename)
		}
	}

	timestamp := firstNonZeroInt64(detail.Timestamp, item.Timestamp)
	// Search summaries carry the current swarm counts. Detail responses are cached
	// for days because their files/subtitles are immutable, so their peer counts
	// must not override fresher listing data.
	seeders := item.Seeders
	leechers := item.Leechers
	size := firstNonZeroInt64(detail.TotalSize, item.TotalSize)
	originalTitle := firstNonEmpty(item.Title, detail.Title)
	return AnimeBRRelease{
		ID:                firstNonZeroInt64(detail.ID, item.ID),
		Title:             title,
		OriginalTitle:     originalTitle,
		Details:           firstNonEmpty(detail.Link, item.Link, fmt.Sprintf("https://animetosho.xyz/view/%d", item.ID)),
		DownloadURL:       downloadURL,
		MagnetURL:         firstNonEmpty(detail.MagnetURI, item.MagnetURI),
		InfoHash:          infoHash,
		Published:         time.Unix(timestamp, 0).UTC(),
		Size:              size,
		Seeders:           seeders,
		Leechers:          leechers,
		Season:            season,
		Episode:           episode,
		TVDBID:            detail.TVDBID,
		Source:            "Anime Tosho NEW",
		PTBRState:         "verified",
		SubtitleLanguages: []string{"Portuguese (Brazil)"},
		Evidence:          slices.Compact(evidence.Reasons),
		Files:             fileNames,
		Score:             animeBRQualityScore(title, seeders),
	}, true
}

func (s *AnimeBRService) searchAnimeTosho(ctx context.Context, query string) ([]animeToshoSearchItem, error) {
	endpoint, err := url.Parse(s.config.BaseURL + "/json")
	if err != nil {
		return nil, err
	}
	values := endpoint.Query()
	if query != "" {
		values.Set("q", query)
	}
	endpoint.RawQuery = values.Encode()

	var items []animeToshoSearchItem
	if err := s.getJSON(ctx, endpoint.String(), s.config.SearchCacheTTL, &items); err != nil {
		return nil, fmt.Errorf("Anime Tosho search failed: %w", err)
	}
	return items, nil
}

func (s *AnimeBRService) getAnimeToshoDetail(ctx context.Context, id int64) (animeToshoDetail, error) {
	endpoint, err := url.Parse(s.config.BaseURL + "/json")
	if err != nil {
		return animeToshoDetail{}, err
	}
	values := endpoint.Query()
	values.Set("show", "torrent")
	values.Set("id", strconv.FormatInt(id, 10))
	endpoint.RawQuery = values.Encode()

	var detail animeToshoDetail
	if err := s.getJSON(ctx, endpoint.String(), s.config.DetailCacheTTL, &detail); err != nil {
		return animeToshoDetail{}, fmt.Errorf("Anime Tosho detail %d failed: %w", id, err)
	}
	return detail, nil
}

func (s *AnimeBRService) getJSON(ctx context.Context, rawURL string, ttl time.Duration, destination any) error {
	cacheKey := animeBRCacheKey(rawURL)
	if s.cache != nil {
		if cached, err := s.cache.Get(ctx, cacheKey); err == nil && len(cached) > 0 {
			if err := json.Unmarshal(cached, destination); err == nil {
				return nil
			}
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "torrent-indexer/anime-br")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAnimeBRResponseBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxAnimeBRResponseBytes {
		return fmt.Errorf("response exceeds %d bytes", maxAnimeBRResponseBytes)
	}
	if err := json.Unmarshal(body, destination); err != nil {
		return err
	}
	cacheTTL := ttl
	if detail, ok := destination.(*animeToshoDetail); ok {
		cacheTTL = s.animeBRDetailCacheTTL(*detail)
	}
	if s.cache != nil {
		if err := s.cache.SetWithExpiration(ctx, cacheKey, body, cacheTTL); err != nil {
			logging.Debug().Err(err).Msg("Failed to cache Anime BR response")
		}
	}
	return nil
}

func (s *AnimeBRService) animeBRDetailCacheTTL(detail animeToshoDetail) time.Duration {
	if animeBRDetailHasStablePTBRMetadata(detail) {
		return s.config.DetailCacheTTL
	}
	return s.config.UnverifiedDetailCacheTTL
}

func animeBRDetailHasStablePTBRMetadata(detail animeToshoDetail) bool {
	if !strings.EqualFold(detail.Status, "complete") || detail.Deleted {
		return false
	}

	for _, file := range detail.Files {
		if !file.Processed || !isVideoFileName(file.Filename) {
			continue
		}
		if classifyPTBREvidence(detail, file).Verified {
			return true
		}
	}
	return false
}

func classifyPTBREvidence(detail animeToshoDetail, selectedFile animeToshoFile) animeBREvidence {
	evidence := animeBREvidence{}
	attachments := append([]animeToshoAttachment{}, selectedFile.Attachments...)
	videoCount := 0
	for _, file := range detail.Files {
		if isVideoFileName(file.Filename) {
			videoCount++
		}
	}
	// Torrent-level attachments can only be attributed safely when the torrent
	// contains one video. With multiple videos, only file-scoped evidence counts.
	if videoCount == 1 {
		attachments = append(attachments, detail.Attachments...)
	}
	hasBrazilianAttachment := false
	hasNonForcedBrazilianAttachment := false
	for _, attachment := range attachments {
		if !strings.EqualFold(attachment.Type, "subtitle") {
			continue
		}
		code := firstNonEmpty(attachment.Info.LanguageCode, attachment.Info.Lang)
		label := firstNonEmpty(attachment.Info.Language, attachment.Info.Name)
		isBrazilian := isBrazilianPortuguese(code, label)
		if isBrazilian {
			hasBrazilianAttachment = true
		}
		if attachment.Info.Forced || isForcedSubtitleLabel(label) {
			continue
		}
		if isBrazilian {
			hasNonForcedBrazilianAttachment = true
			evidence.Verified = true
			evidence.Reasons = append(evidence.Reasons, fmt.Sprintf("extracted subtitle: %s / %s", code, label))
		} else if isGenericPortuguese(code, label) {
			evidence.Probable = true
			evidence.Reasons = append(evidence.Reasons, fmt.Sprintf("generic Portuguese subtitle (not accepted in strict mode): %s / %s", code, label))
		}
	}
	for _, subtitle := range selectedFile.Info.MediaInfoJ.Subtitles {
		if isForcedSubtitleLabel(subtitle.Title) {
			continue
		}
		if isBrazilianPortuguese(subtitle.Language, subtitle.Title) {
			// Anime Tosho attachments preserve the explicit forced flag while
			// MediaInfo often does not. If every Brazilian attachment is forced,
			// MediaInfo must not promote that same track to full-dialogue proof.
			if hasBrazilianAttachment && !hasNonForcedBrazilianAttachment {
				continue
			}
			evidence.Verified = true
			evidence.Reasons = append(evidence.Reasons, fmt.Sprintf("MediaInfo subtitle: %s / %s", subtitle.Language, subtitle.Title))
		} else if isGenericPortuguese(subtitle.Language, subtitle.Title) {
			evidence.Probable = true
		}
	}
	for _, file := range detail.Files {
		if ptBRFileRE.MatchString(file.Filename) && isSubtitleFile(file.Filename) &&
			!isForcedSubtitleLabel(file.Filename) &&
			(videoCount == 1 || sameEpisodeMarker(file.Filename, selectedFile.Filename)) {
			evidence.Verified = true
			evidence.Reasons = append(evidence.Reasons, "Brazilian Portuguese sidecar subtitle: "+file.Filename)
		}
	}
	return evidence
}

func isForcedSubtitleLabel(label string) bool {
	lower := strings.ToLower(label)
	return strings.Contains(lower, "forced") || strings.Contains(lower, "signs & songs") || strings.Contains(lower, "signs and songs")
}

func sameEpisodeMarker(left, right string) bool {
	leftSeason, leftEpisode, leftOK := episodeFromFilename(left)
	rightSeason, rightEpisode, rightOK := episodeFromFilename(right)
	return leftOK && rightOK && leftSeason == rightSeason && leftEpisode == rightEpisode
}

func isBrazilianPortuguese(code, label string) bool {
	code = strings.ToLower(strings.TrimSpace(code))
	label = strings.TrimSpace(label)
	if code == "pt-br" || code == "ptbr" || code == "por-br" {
		return true
	}
	return (code == "por" || code == "pt" || strings.Contains(strings.ToLower(label), "portugu")) && brazilianLabelRE.MatchString(label)
}

func isGenericPortuguese(code, label string) bool {
	code = strings.ToLower(strings.TrimSpace(code))
	label = strings.ToLower(strings.TrimSpace(label))
	return code == "por" || code == "pt" || strings.Contains(label, "portuguese") || strings.Contains(label, "português")
}

func selectCompatibleVideoFile(files []animeToshoFile, request AnimeBRSearchRequest, strict bool) (animeToshoFile, bool) {
	hasSeason := request.SeasonSpecified || request.Season > 0
	hasEpisode := request.Episode > 0
	for _, file := range files {
		if !isVideoFileName(file.Filename) {
			continue
		}
		parsedSeason, parsedEpisode, found := episodeFromFilename(file.Filename)
		if hasSeason {
			if found && parsedSeason == request.Season && (!hasEpisode || parsedEpisode == request.Episode) {
				return file, true
			}
			if !strict && !found {
				return file, true
			}
			continue
		}
		if found || !strict {
			return file, true
		}
	}
	return animeToshoFile{}, false
}

func episodeFromFilename(filename string) (int, int, bool) {
	match := standardEpisodeRE.FindStringSubmatch(filename)
	if len(match) != 3 {
		return 0, 0, false
	}
	season, seasonErr := strconv.Atoi(match[1])
	episode, episodeErr := strconv.Atoi(match[2])
	return season, episode, seasonErr == nil && episodeErr == nil && season >= 0 && episode > 0
}

func candidateCouldMatchEpisode(title string, request AnimeBRSearchRequest) bool {
	hasSeason := request.SeasonSpecified || request.Season > 0
	if !hasSeason {
		return true
	}
	hasEpisode := request.Episode > 0
	standardMatches := standardEpisodeRE.FindAllStringSubmatch(title, -1)
	if len(standardMatches) > 0 {
		for _, match := range standardMatches {
			candidateSeason, _ := strconv.Atoi(match[1])
			candidateEpisode, _ := strconv.Atoi(match[2])
			if candidateSeason == request.Season && (!hasEpisode || candidateEpisode == request.Episode) {
				return true
			}
		}
		return false
	}
	if match := seasonDashRE.FindStringSubmatch(title); len(match) == 3 {
		candidateSeason, _ := strconv.Atoi(match[1])
		candidateEpisode, _ := strconv.Atoi(match[2])
		return candidateSeason == request.Season && (!hasEpisode || candidateEpisode == request.Episode)
	}
	if !hasEpisode {
		return false
	}
	if matches := bareEpisodeRE.FindAllStringSubmatch(title, -1); len(matches) > 0 {
		for _, match := range matches {
			candidateEpisode, _ := strconv.Atoi(match[1])
			if candidateEpisode == request.Episode {
				return true
			}
		}
	}
	return false
}

func cleanAnimeBRQuery(query string) string {
	query = queryEpisodeRE.ReplaceAllString(query, " ")
	return strings.Join(strings.Fields(query), " ")
}

func animeBRQualityScore(title string, seeders int) int {
	lower := strings.ToLower(title)
	score := min(max(seeders, 0), 300)
	if strings.Contains(lower, "web-dl") || strings.Contains(lower, "webdl") {
		score += 500
	} else if strings.Contains(lower, "webrip") || strings.Contains(lower, "web-rip") {
		score += 300
	}
	if strings.Contains(lower, "1080p") {
		score += 350
	} else if strings.Contains(lower, "2160p") || strings.Contains(lower, "4k") {
		score += 300
	} else if strings.Contains(lower, "720p") {
		score += 150
	}
	if strings.Contains(lower, "avc") || strings.Contains(lower, "h.264") || strings.Contains(lower, "x264") {
		score += 100
	} else if strings.Contains(lower, "hevc") || strings.Contains(lower, "h.265") || strings.Contains(lower, "x265") {
		score += 40
	}
	return score
}

func isVideoFileName(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mkv", ".mp4", ".m4v", ".avi", ".webm", ".ts", ".m2ts":
		return true
	default:
		return false
	}
}

func isSubtitleFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".ass", ".ssa", ".srt", ".vtt", ".sup":
		return true
	default:
		return false
	}
}

func validInfoHash(hash string) bool {
	if len(hash) != 40 {
		return false
	}
	for _, char := range strings.ToLower(hash) {
		if !strings.ContainsRune("0123456789abcdef", char) {
			return false
		}
	}
	return true
}

func animeBRCacheKey(rawURL string) string {
	hash := sha256.Sum256([]byte(rawURL))
	return fmt.Sprintf("anime_br:%s:http:%x", animeBRCacheVersion, hash[:])
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envIntOrDefault(name string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func firstNonZeroInt64(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func (s *AnimeBRService) HandlerJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	request, err := animeBRRequestFromQuery(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	releases, err := s.Search(r.Context(), request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"results": releases, "count": len(releases)})
}

func animeBRRequestFromQuery(values url.Values) (AnimeBRSearchRequest, error) {
	rawSeason := strings.TrimSpace(values.Get("season"))
	season, err := parseOptionalNonNegativeInt(rawSeason)
	if err != nil {
		return AnimeBRSearchRequest{}, fmt.Errorf("invalid season")
	}
	rawEpisode := strings.TrimSpace(values.Get("ep"))
	episode, err := parseOptionalPositiveInt(rawEpisode)
	if err != nil {
		return AnimeBRSearchRequest{}, fmt.Errorf("invalid ep")
	}
	seasonSpecified := rawSeason != ""
	if marker := standardEpisodeRE.FindStringSubmatch(values.Get("q")); len(marker) == 3 {
		if !seasonSpecified {
			season, _ = strconv.Atoi(marker[1])
			seasonSpecified = true
		}
		if rawEpisode == "" {
			episode, _ = strconv.Atoi(marker[2])
		}
	}
	var tvdbID int64
	if rawTVDBID := strings.TrimSpace(values.Get("tvdbid")); rawTVDBID != "" {
		tvdbID, err = strconv.ParseInt(rawTVDBID, 10, 64)
		if err != nil || tvdbID <= 0 {
			return AnimeBRSearchRequest{}, fmt.Errorf("invalid tvdbid")
		}
	}
	return AnimeBRSearchRequest{
		Query:           values.Get("q"),
		Season:          season,
		SeasonSpecified: seasonSpecified,
		Episode:         episode,
		TVDBID:          tvdbID,
	}, nil
}

func parseOptionalNonNegativeInt(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("invalid non-negative integer")
	}
	return parsed, nil
}

func parseOptionalPositiveInt(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	if dot := strings.IndexByte(value, '.'); dot >= 0 {
		value = value[:dot]
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("invalid positive integer")
	}
	return parsed, nil
}
