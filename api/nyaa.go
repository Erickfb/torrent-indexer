package handler

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Erickfb/torrent-indexer/logging"
)

var nyaaViewIDRE = regexp.MustCompile(`/view/(\d+)`)
var nyaaLeadingReleaseTagsRE = regexp.MustCompile(`^(?:\s*\[[^\]]+\])+\s*`)
var nyaaTrailingYearRE = regexp.MustCompile(`\s+(?:19|20)\d{2}$`)

type nyaaRSS struct {
	Channel nyaaRSSChannel `xml:"channel"`
}

type nyaaRSSChannel struct {
	Items []nyaaRSSItem `xml:"item"`
}

type nyaaRSSItem struct {
	Title       string `xml:"title"`
	TorrentURL  string `xml:"link"`
	DetailsURL  string `xml:"guid"`
	Published   string `xml:"pubDate"`
	Seeders     string `xml:"seeders"`
	Leechers    string `xml:"leechers"`
	Downloads   string `xml:"downloads"`
	InfoHash    string `xml:"infoHash"`
	CategoryID  string `xml:"categoryId"`
	Category    string `xml:"category"`
	Size        string `xml:"size"`
	Trusted     string `xml:"trusted"`
	Remake      string `xml:"remake"`
	Description string `xml:"description"`
}

func (s *AnimeBRService) searchNyaaForRequest(ctx context.Context, request AnimeBRSearchRequest) ([]animeToshoSearchItem, error) {
	queries := animeBRSearchQueries(request)
	var discovered []animeToshoSearchItem
	var searchErrors []error
	for _, query := range queries {
		items, err := s.searchNyaa(ctx, query)
		if err != nil {
			searchErrors = append(searchErrors, err)
			continue
		}
		discovered = append(discovered, convertNyaaRSSItems(items)...)
	}
	if len(discovered) == 0 && len(searchErrors) == len(queries) {
		return nil, errors.Join(searchErrors...)
	}
	return mergeNyaaSearchItems(discovered), nil
}

func convertNyaaRSSItems(items []nyaaRSSItem) []animeToshoSearchItem {
	converted := make([]animeToshoSearchItem, 0, len(items))
	for _, item := range items {
		candidate, ok := nyaaItemAsAnimeBRSearchItem(item)
		if ok {
			converted = append(converted, candidate)
		}
	}
	return converted
}

func mergeNyaaSearchItems(groups ...[]animeToshoSearchItem) []animeToshoSearchItem {
	itemsByHash := make(map[string]animeToshoSearchItem)
	for _, group := range groups {
		for _, item := range group {
			hash := strings.ToLower(strings.TrimSpace(item.InfoHash))
			if !validInfoHash(hash) {
				continue
			}
			item.InfoHash = hash
			current, exists := itemsByHash[hash]
			if !exists {
				itemsByHash[hash] = item
				continue
			}
			if item.Timestamp > current.Timestamp {
				current.Title = firstNonEmpty(item.Title, current.Title)
				current.TorrentName = firstNonEmpty(item.TorrentName, current.TorrentName)
				current.Link = firstNonEmpty(item.Link, current.Link)
				current.TorrentURL = firstNonEmpty(item.TorrentURL, current.TorrentURL)
				current.Timestamp = item.Timestamp
			}
			current.Seeders = max(current.Seeders, item.Seeders)
			current.Leechers = max(current.Leechers, item.Leechers)
			current.TotalSize = max(current.TotalSize, item.TotalSize)
			current.NyaaID = firstNonZeroInt64(item.NyaaID, current.NyaaID)
			current.Source = "Nyaa.si"
			itemsByHash[hash] = current
		}
	}
	merged := make([]animeToshoSearchItem, 0, len(itemsByHash))
	for _, item := range itemsByHash {
		merged = append(merged, item)
	}
	return merged
}

func deriveNyaaAliasQuery(items []animeToshoSearchItem, request AnimeBRSearchRequest) string {
	if !(request.SeasonSpecified || request.Season > 0) || request.Episode <= 0 {
		return ""
	}
	requestedBase := normalizeNyaaTitleBase(stripTrailingAnimeYear(cleanAnimeBRQuery(request.Query)))
	type aliasCandidate struct {
		base  string
		count int
	}
	byKey := make(map[string]*aliasCandidate)
	order := make([]string, 0)
	for _, item := range items {
		title := firstNonEmpty(item.Title, item.TorrentName)
		marker := standardEpisodeRE.FindStringIndex(title)
		if marker == nil {
			continue
		}
		base := strings.TrimSpace(title[:marker[0]])
		base = nyaaLeadingReleaseTagsRE.ReplaceAllString(base, "")
		base = strings.NewReplacer(".", " ", "_", " ").Replace(base)
		base = strings.Trim(strings.Join(strings.Fields(base), " "), " -_.")
		base = nyaaTrailingYearRE.ReplaceAllString(base, "")
		key := normalizeNyaaTitleBase(base)
		if key == "" || key == requestedBase {
			continue
		}
		if candidate, exists := byKey[key]; exists {
			candidate.count++
			continue
		}
		byKey[key] = &aliasCandidate{base: base, count: 1}
		order = append(order, key)
	}
	var best *aliasCandidate
	for _, key := range order {
		candidate := byKey[key]
		if best == nil || candidate.count > best.count || (candidate.count == best.count && len(candidate.base) < len(best.base)) {
			best = candidate
		}
	}
	if best == nil {
		return ""
	}
	return fmt.Sprintf("%s S%02dE%02d", best.base, request.Season, request.Episode)
}

func normalizeNyaaTitleBase(value string) string {
	value = strings.NewReplacer(".", " ", "_", " ", "-", " ").Replace(strings.ToLower(value))
	return strings.Join(strings.Fields(value), " ")
}

func (s *AnimeBRService) searchNyaa(ctx context.Context, query string) ([]nyaaRSSItem, error) {
	endpoint, err := url.Parse(s.config.NyaaURL)
	if err != nil {
		return nil, err
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/"
	values := endpoint.Query()
	values.Set("page", "rss")
	values.Set("c", "1_0")
	values.Set("f", "0")
	if query != "" {
		values.Set("q", query)
	}
	endpoint.RawQuery = values.Encode()

	var feed nyaaRSS
	if err := s.getNyaaXML(ctx, endpoint.String(), &feed); err != nil {
		return nil, fmt.Errorf("Nyaa search failed: %w", err)
	}
	return feed.Channel.Items, nil
}

func (s *AnimeBRService) getNyaaXML(ctx context.Context, rawURL string, destination any) error {
	cacheKey := animeBRCacheKey(rawURL)
	if s.cache != nil {
		if cached, err := s.cache.Get(ctx, cacheKey); err == nil && len(cached) > 0 {
			if err := xml.Unmarshal(cached, destination); err == nil {
				return nil
			}
		}
	}

	releaseRequestSlot, err := s.acquireNyaaRequestGate(ctx)
	if err != nil {
		return err
	}
	defer releaseRequestSlot()

	// Another concurrent request for the same URL may have populated the cache
	// while this call waited for the global Nyaa request slot.
	if s.cache != nil {
		if cached, cacheErr := s.cache.Get(ctx, cacheKey); cacheErr == nil && len(cached) > 0 {
			if unmarshalErr := xml.Unmarshal(cached, destination); unmarshalErr == nil {
				return nil
			}
		}
	}
	if err := s.waitForNyaaRequestTime(ctx); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/rss+xml,application/xml;q=0.9,*/*;q=0.5")
	req.Header.Set("User-Agent", "torrent-indexer/anime-br (+https://github.com/Erickfb/torrent-indexer)")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &animeBRHTTPStatusError{StatusCode: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAnimeBRResponseBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxAnimeBRResponseBytes {
		return fmt.Errorf("response exceeds %d bytes", maxAnimeBRResponseBytes)
	}
	if err := xml.Unmarshal(body, destination); err != nil {
		return fmt.Errorf("invalid Nyaa RSS: %w", err)
	}
	if s.cache != nil {
		if err := s.cache.SetWithExpiration(ctx, cacheKey, body, s.config.NyaaSearchCacheTTL); err != nil {
			logging.Debug().Err(err).Msg("Failed to cache Nyaa RSS response")
		}
	}
	return nil
}

func (s *AnimeBRService) acquireNyaaRequestGate(ctx context.Context) (func(), error) {
	select {
	case s.nyaaRequestGate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return func() { <-s.nyaaRequestGate }, nil
}

func (s *AnimeBRService) waitForNyaaRequestTime(ctx context.Context) error {
	if !s.nyaaLastRequest.IsZero() {
		wait := s.config.NyaaRequestDelay - time.Since(s.nyaaLastRequest)
		if wait > 0 {
			timer := time.NewTimer(wait)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	s.nyaaLastRequest = time.Now()
	return nil
}

func nyaaItemAsAnimeBRSearchItem(item nyaaRSSItem) (animeToshoSearchItem, bool) {
	infoHash := strings.ToLower(strings.TrimSpace(item.InfoHash))
	if !validInfoHash(infoHash) || !strings.HasPrefix(strings.TrimSpace(item.CategoryID), "1_") {
		return animeToshoSearchItem{}, false
	}
	torrentURL := strings.TrimSpace(item.TorrentURL)
	detailsURL := strings.TrimSpace(item.DetailsURL)
	published := parseNyaaDate(item.Published)
	var timestamp int64
	if !published.IsZero() {
		timestamp = published.Unix()
	}
	return animeToshoSearchItem{
		Title:       strings.TrimSpace(item.Title),
		Link:        detailsURL,
		Timestamp:   timestamp,
		Status:      "complete",
		TorrentURL:  torrentURL,
		TorrentName: strings.TrimSpace(item.Title),
		InfoHash:    infoHash,
		Seeders:     parseNyaaInt(item.Seeders),
		Leechers:    parseNyaaInt(item.Leechers),
		TotalSize:   parseNyaaSize(item.Size),
		Source:      "Nyaa.si",
		NyaaID:      parseNyaaViewID(detailsURL),
	}, true
}

func mergeAnimeBRSearchItems(animeToshoItems, nyaaItems []animeToshoSearchItem) []animeToshoSearchItem {
	itemsByHash := make(map[string]animeToshoSearchItem, len(animeToshoItems)+len(nyaaItems))
	for _, item := range animeToshoItems {
		hash := strings.ToLower(strings.TrimSpace(item.InfoHash))
		if !validInfoHash(hash) {
			continue
		}
		item.InfoHash = hash
		if item.Source == "" {
			item.Source = "Anime Tosho NEW"
		}
		itemsByHash[hash] = item
	}
	for _, nyaaItem := range nyaaItems {
		hash := strings.ToLower(strings.TrimSpace(nyaaItem.InfoHash))
		if !validInfoHash(hash) {
			continue
		}
		if current, exists := itemsByHash[hash]; exists {
			current.Title = firstNonEmpty(nyaaItem.Title, current.Title)
			current.TorrentName = firstNonEmpty(nyaaItem.TorrentName, current.TorrentName)
			current.Link = firstNonEmpty(nyaaItem.Link, current.Link)
			current.TorrentURL = firstNonEmpty(nyaaItem.TorrentURL, current.TorrentURL)
			current.MagnetURI = firstNonEmpty(nyaaItem.MagnetURI, current.MagnetURI)
			if nyaaItem.Timestamp > 0 {
				current.Timestamp = nyaaItem.Timestamp
			}
			if nyaaItem.TotalSize > 0 {
				current.TotalSize = nyaaItem.TotalSize
			}
			current.Seeders = nyaaItem.Seeders
			current.Leechers = nyaaItem.Leechers
			current.NyaaID = nyaaItem.NyaaID
			// A valid Nyaa RSS item proves the torrent is published even when the
			// current Anime Tosho summary is a historical "unknown" placeholder.
			// Final acceptance still requires exact-hash file and subtitle metadata.
			current.Status = "complete"
			current.Source = "Nyaa.si + Anime Tosho NEW"
			itemsByHash[hash] = current
			continue
		}
		nyaaItem.InfoHash = hash
		nyaaItem.Source = "Nyaa.si"
		itemsByHash[hash] = nyaaItem
	}

	items := make([]animeToshoSearchItem, 0, len(itemsByHash))
	for _, item := range itemsByHash {
		items = append(items, item)
	}
	return items
}

func parseNyaaViewID(rawURL string) int64 {
	match := nyaaViewIDRE.FindStringSubmatch(rawURL)
	if len(match) != 2 {
		return 0
	}
	id, _ := strconv.ParseInt(match[1], 10, 64)
	return id
}

func parseNyaaInt(value string) int {
	parsed, _ := strconv.Atoi(strings.TrimSpace(value))
	return max(parsed, 0)
}

func parseNyaaDate(value string) time.Time {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func parseNyaaSize(value string) int64 {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) < 2 {
		return 0
	}
	number, err := strconv.ParseFloat(strings.ReplaceAll(fields[0], ",", ""), 64)
	if err != nil || number < 0 {
		return 0
	}
	multipliers := map[string]float64{
		"b":   1,
		"kib": 1 << 10,
		"mib": 1 << 20,
		"gib": 1 << 30,
		"tib": 1 << 40,
		"kb":  1e3,
		"mb":  1e6,
		"gb":  1e9,
		"tb":  1e12,
	}
	multiplier, ok := multipliers[strings.ToLower(fields[1])]
	if !ok || number > float64(math.MaxInt64)/multiplier {
		return 0
	}
	return int64(math.Round(number * multiplier))
}
