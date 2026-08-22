package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/Erickfb/torrent-indexer/logging"
	"github.com/Erickfb/torrent-indexer/magnet"
	"github.com/Erickfb/torrent-indexer/schema"
	goscrape "github.com/Erickfb/torrent-indexer/scrape"
	"github.com/Erickfb/torrent-indexer/utils"
)

var bludv = IndexerMeta{
	Label:       "bludv",
	URL:         utils.GetIndexerURLFromEnv("INDEXER_BLUDV_URL", "https://bludvfilmes1.xyz/"),
	SearchURL:   "?s=",
	PagePattern: "page/%s",
}

const bludvMaxSeasonSearchPages = 3

var bludvTransmissionTrackers = []string{
	"udp://tracker.openbittorrent.com:80/announce",
	"udp://tracker.opentrackr.org:1337/announce",
	"udp://tracker.trackerfix.com:80/announce",
	"udp://p4p.arenabg.com:1337/announce",
	"udp://explodie.org:6969/announce",
	"udp://open.stealth.si:80/announce",
	"https://tracker.yemekyedim.com:443/announce",
}

var (
	bludvSeasonEpisodeQueryRE  = regexp.MustCompile(`(?i)\bs0*([0-9]{1,3})[\s._-]*e0*([0-9]{1,3})\b`)
	bludvTemporadaEpisodeRE    = regexp.MustCompile(`(?i)\btemporada\s+0*([0-9]{1,3})\s+epis.?dio\s+0*([0-9]{1,3})\b`)
	bludvSeasonOnlyQueryRE     = regexp.MustCompile(`(?i)\bs0*[0-9]{1,3}\b|\be0*[0-9]{1,3}\b|\btemporada\s+0*[0-9]{1,3}\b|\bepis.?dio\s+0*[0-9]{1,3}\b`)
	bludvSeasonInReleaseNameRE = regexp.MustCompile(`(?i)\b0*([0-9]{1,3})\D{0,6}temporada\b|\btemporada\D{0,6}0*([0-9]{1,3})\b|\bs0*([0-9]{1,3})(?:[\s._-]*e0*[0-9]{1,3})?\b`)
	bludvEpisodeInReleaseRE    = regexp.MustCompile(`(?i)\bs0*[0-9]{1,3}[\s._-]*e0*([0-9]{1,3})\b|\be0*([0-9]{1,3})\b|\bep(?:is.?dio|isode)?\.?\s*0*([0-9]{1,3})\b`)
	bludvConjunctionQueryRE    = regexp.MustCompile(`(?i)\b(and|es|y|et)\b`)
	bludvSonarrSeasonEpisodeRE = regexp.MustCompile(`(?i)\bs0*[0-9]{1,3}[\s._-]*e0*[0-9]{1,3}\b`)
	bludvSonarrSeasonRE        = regexp.MustCompile(`(?i)\bs0*[0-9]{1,3}\b`)
	bludvMultiResolutionRE     = regexp.MustCompile(`(?i)\b(?:480p|720p|1080p|2160p|4k)\b(?:\s*(?:[/|,]|\bor\b|\band\b|\be\b|\bou\b)\s*\b(?:480p|720p|1080p|2160p|4k)\b)+`)
	bludvResolutionRE          = regexp.MustCompile(`(?i)\b(?:480p|720p|1080p|2160p|4k)\b`)
	bludvNonWordRE             = regexp.MustCompile(`[^a-z0-9]+`)
	bludvSystemAdsAnchorRE     = regexp.MustCompile(`(?is)<a\s+[^>]*href=["']([^"']*systemads\.net/go\.php[^"']*)["'][^>]*>(.*?)</a>`)
	bludvHTMLTagRE             = regexp.MustCompile(`(?is)<[^>]+>`)
)

var bludvComparableTextReplacer = strings.NewReplacer(
	"&", " and ",
	"á", "a",
	"à", "a",
	"â", "a",
	"ã", "a",
	"ä", "a",
	"é", "e",
	"è", "e",
	"ê", "e",
	"ë", "e",
	"í", "i",
	"ì", "i",
	"î", "i",
	"ï", "i",
	"ó", "o",
	"ò", "o",
	"ô", "o",
	"õ", "o",
	"ö", "o",
	"ú", "u",
	"ù", "u",
	"û", "u",
	"ü", "u",
	"ç", "c",
	"ª", "a",
	"º", "o",
)

var bludvIgnoredTitleTokens = map[string]bool{
	"torrent":   true,
	"download":  true,
	"baixar":    true,
	"web":       true,
	"dl":        true,
	"webdl":     true,
	"webrip":    true,
	"bluray":    true,
	"blu":       true,
	"ray":       true,
	"brrip":     true,
	"hdrip":     true,
	"hdtv":      true,
	"dual":      true,
	"audio":     true,
	"dublado":   true,
	"legendado": true,
	"720p":      true,
	"1080p":     true,
	"2160p":     true,
	"4k":        true,
}

type bludvSearchLink struct {
	URL   string
	Title string
}

type bludvMagnetLink struct {
	URL     string
	Label   string
	Episode string
}

func (i *Indexer) HandlerBluDVIndexer(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	metadata := bludv

	defer func() {
		i.metrics.IndexerDuration.WithLabelValues(metadata.Label).Observe(time.Since(start).Seconds())
		i.metrics.IndexerRequests.WithLabelValues(metadata.Label).Inc()
	}()

	ctx := r.Context()
	// supported query params: q, season, episode, page, filter_results
	rawQ := r.URL.Query().Get("q")
	q := normalizeBluDVSearchQuery(rawQ)
	page := r.URL.Query().Get("page")
	requestedSeason := extractBluDVSeason(rawQ)
	targetURL := buildBluDVURL(metadata, q, page)

	logging.InfoWithRequest(r).Str("target_url", targetURL).Msg("Processing indexer request")
	resp, err := i.requester.GetDocument(ctx, targetURL)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		err = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		if err != nil {
			logging.ErrorWithRequest(r).Err(err).Msg("Failed to encode error response")
		}
		i.metrics.IndexerErrors.WithLabelValues(metadata.Label).Inc()
		return
	}
	defer resp.Close()

	doc, err := goquery.NewDocumentFromReader(resp)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		err = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		if err != nil {
			logging.ErrorWithRequest(r).Err(err).Msg("Failed to encode error response")
		}

		i.metrics.IndexerErrors.WithLabelValues(metadata.Label).Inc()
		return
	}

	links := extractBluDVSearchLinks(doc)
	if requestedSeason != "" && q != "" {
		links = filterBluDVSearchLinks(rawQ, requestedSeason, links)

		for pageNum := 2; len(links) == 0 && page == "" && pageNum <= bludvMaxSeasonSearchPages; pageNum++ {
			nextURL := buildBluDVURL(metadata, q, fmt.Sprintf("%d", pageNum))
			nextResp, err := i.requester.GetDocument(ctx, nextURL)
			if err != nil {
				logging.WarnWithRequest(r).Err(err).Str("target_url", nextURL).Msg("Failed to fetch BLUDV search page")
				break
			}

			nextDoc, err := goquery.NewDocumentFromReader(nextResp)
			_ = nextResp.Close()
			if err != nil {
				logging.WarnWithRequest(r).Err(err).Str("target_url", nextURL).Msg("Failed to parse BLUDV search page")
				break
			}

			links = filterBluDVSearchLinks(rawQ, requestedSeason, extractBluDVSearchLinks(nextDoc))
		}
	}
	linkURLs := bludvSearchLinkURLs(links)

	// if no links were indexed, expire the document in cache
	if len(linkURLs) == 0 {
		_ = i.requester.ExpireDocument(ctx, targetURL)
	}

	// extract each torrent link
	indexedTorrents := utils.ParallelFlatMap(linkURLs, func(link string) ([]schema.IndexedTorrent, error) {
		return getTorrentsBluDV(ctx, i, link, targetURL)
	})

	// Apply post-processors
	postProcessedTorrents := indexedTorrents
	for _, processor := range i.postProcessors {
		postProcessedTorrents = processor(i, r, postProcessedTorrents)
	}
	postProcessedTorrents = filterBluDVSeasonResults(rawQ, postProcessedTorrents)

	w.Header().Set("Content-Type", "application/json")
	err = json.NewEncoder(w).Encode(Response{
		Results:      postProcessedTorrents,
		Count:        len(postProcessedTorrents),
		IndexedCount: len(indexedTorrents),
	})
	if err != nil {
		logging.Error().Err(err).Msg("Failed to encode response")
	}
}

func buildBluDVURL(metadata IndexerMeta, q, page string) string {
	if page != "" {
		if q != "" {
			return fmt.Sprintf("%spage/%s/?s=%s", metadata.URL, page, url.QueryEscape(q))
		}

		return fmt.Sprintf(fmt.Sprintf("%s%s", metadata.URL, metadata.PagePattern), page)
	}

	return fmt.Sprintf("%s%s%s", metadata.URL, metadata.SearchURL, url.QueryEscape(q))
}

func extractBluDVSearchLinks(doc *goquery.Document) []bludvSearchLink {
	var links []bludvSearchLink
	doc.Find(".post").Each(func(i int, s *goquery.Selection) {
		linkNode := s.Find("div.title > a")
		link, _ := linkNode.Attr("href")
		title := strings.TrimSpace(linkNode.Text())
		if title == "" {
			title, _ = linkNode.Attr("title")
		}
		if link != "" {
			links = append(links, bludvSearchLink{URL: link, Title: title})
		}
	})

	return links
}

func filterBluDVSearchLinks(q, requestedSeason string, links []bludvSearchLink) []bludvSearchLink {
	filtered := make([]bludvSearchLink, 0, len(links))
	for _, link := range links {
		title := link.Title
		if title == "" {
			title = link.URL
		}

		season := extractBluDVSeason(fmt.Sprintf("%s %s", title, link.URL))
		if season == requestedSeason && matchesBluDVRequestedTitle(q, title) {
			filtered = append(filtered, link)
		}
	}

	return filtered
}

func bludvSearchLinkURLs(links []bludvSearchLink) []string {
	urls := make([]string, 0, len(links))
	for _, link := range links {
		urls = append(urls, link.URL)
	}

	return urls
}

func normalizeBluDVSearchQuery(q string) string {
	q = strings.TrimSpace(q)
	if q == "" {
		return q
	}

	q = replaceBluDVSeasonEpisodeWithSeason(q, bludvSeasonEpisodeQueryRE)
	q = replaceBluDVSeasonEpisodeWithSeason(q, bludvTemporadaEpisodeRE)
	q = normalizeBluDVSearchConjunctions(q)

	return strings.Join(strings.Fields(q), " ")
}

func normalizeBluDVSearchConjunctions(q string) string {
	return bludvConjunctionQueryRE.ReplaceAllString(q, "e")
}

func replaceBluDVSeasonEpisodeWithSeason(q string, re *regexp.Regexp) string {
	return re.ReplaceAllStringFunc(q, func(match string) string {
		parts := re.FindStringSubmatch(match)
		if len(parts) < 2 {
			return match
		}

		return fmt.Sprintf("temporada %s", normalizeBluDVSeasonNumber(parts[1]))
	})
}

func filterBluDVSeasonResults(q string, torrents []schema.IndexedTorrent) []schema.IndexedTorrent {
	requestedSeason := extractBluDVSeason(q)
	if requestedSeason == "" {
		return torrents
	}

	filtered := make([]schema.IndexedTorrent, 0, len(torrents))
	for _, it := range torrents {
		title := fmt.Sprintf("%s %s", it.Title, it.OriginalTitle)
		season := extractBluDVSeason(fmt.Sprintf("%s %s", title, it.Details))
		if season == requestedSeason && matchesBluDVRequestedTitle(q, title) {
			filtered = append(filtered, it)
		}
	}

	return filtered
}

func extractBluDVSeason(text string) string {
	matches := bludvSeasonInReleaseNameRE.FindStringSubmatch(text)
	if len(matches) == 0 {
		return ""
	}

	for _, match := range matches[1:] {
		if match != "" {
			return normalizeBluDVSeasonNumber(match)
		}
	}

	return ""
}

func extractBluDVEpisode(text string) string {
	matches := bludvEpisodeInReleaseRE.FindStringSubmatch(text)
	if len(matches) == 0 {
		return ""
	}

	for _, match := range matches[1:] {
		if match != "" {
			return normalizeBluDVSeasonNumber(match)
		}
	}

	return ""
}

func matchesBluDVRequestedTitle(q, title string) bool {
	requestedTokens := bludvRequestedTitleTokens(q)
	if len(requestedTokens) == 0 {
		return true
	}

	titleTokens := bludvReleaseTitleTokens(title)
	if len(titleTokens) != len(requestedTokens) {
		return false
	}

	for idx := range requestedTokens {
		if titleTokens[idx] != requestedTokens[idx] {
			return false
		}
	}

	return true
}

func bludvRequestedTitleTokens(q string) []string {
	q = bludvSeasonEpisodeQueryRE.ReplaceAllString(q, " ")
	q = bludvTemporadaEpisodeRE.ReplaceAllString(q, " ")
	q = bludvSeasonOnlyQueryRE.ReplaceAllString(q, " ")

	return bludvComparableTokens(q)
}

func bludvReleaseTitleTokens(title string) []string {
	normalized := normalizeBluDVComparableText(title)
	if seasonIndex := bludvSeasonInReleaseNameRE.FindStringIndex(normalized); seasonIndex != nil {
		normalized = normalized[:seasonIndex[0]]
	}

	return bludvComparableTokens(normalized)
}

func bludvComparableTokens(text string) []string {
	normalized := normalizeBluDVComparableText(text)
	normalized = bludvNonWordRE.ReplaceAllString(normalized, " ")

	var tokens []string
	for _, token := range strings.Fields(normalized) {
		if isBluDVConjunctionToken(token) {
			token = "and"
		}
		if bludvIgnoredTitleTokens[token] {
			continue
		}
		tokens = append(tokens, token)
	}

	return tokens
}

func isBluDVConjunctionToken(token string) bool {
	switch token {
	case "e", "and", "es", "y", "et":
		return true
	default:
		return false
	}
}

func normalizeBluDVComparableText(text string) string {
	text = html.UnescapeString(strings.ToLower(text))
	return bludvComparableTextReplacer.Replace(text)
}

func cleanBluDVHTMLText(raw string) string {
	text := bludvHTMLTagRE.ReplaceAllString(raw, " ")
	text = html.UnescapeString(text)

	return strings.Join(strings.Fields(text), " ")
}

func normalizeBluDVSeasonNumber(season string) string {
	season = strings.TrimLeft(season, "0")
	if season == "" {
		return "0"
	}

	return season
}

func normalizeBluDVReleaseTitleForSonarr(title string) string {
	title = strings.TrimSpace(title)
	if title == "" || bludvSonarrSeasonEpisodeRE.MatchString(title) {
		return title
	}

	episode := extractBluDVEpisode(title)
	if bludvSonarrSeasonRE.MatchString(title) {
		if episode == "" {
			return title
		}

		return addBluDVEpisodeToExistingSonarrSeason(title, episode)
	}

	season := extractBluDVSeason(title)
	if season == "" {
		return title
	}

	matches := bludvSeasonInReleaseNameRE.FindStringIndex(title)
	if len(matches) == 0 {
		return fmt.Sprintf("%s %s", title, formatBluDVReleaseTag(season, episode))
	}

	prefix := strings.TrimSpace(title[:matches[0]])
	suffix := strings.TrimSpace(title[matches[0]:])
	if prefix == "" {
		return fmt.Sprintf("%s %s", formatBluDVReleaseTag(season, episode), suffix)
	}

	return fmt.Sprintf("%s %s %s", prefix, formatBluDVReleaseTag(season, episode), suffix)
}

func addBluDVEpisodeToExistingSonarrSeason(title, episode string) string {
	matches := bludvSonarrSeasonRE.FindStringIndex(title)
	if len(matches) == 0 {
		return title
	}

	season := extractBluDVSeason(title[matches[0]:matches[1]])
	if season == "" {
		return title
	}

	return fmt.Sprintf(
		"%s%s%s",
		title[:matches[0]],
		formatBluDVReleaseTag(season, episode),
		title[matches[1]:],
	)
}

func formatBluDVReleaseTag(season, episode string) string {
	tag := formatBluDVSeasonTag(season)
	if episode != "" {
		tag += formatBluDVEpisodeTag(episode)
	}

	return tag
}

func formatBluDVSeasonTag(season string) string {
	if len(season) == 1 {
		season = "0" + season
	}

	return "S" + season
}

func formatBluDVEpisodeTag(episode string) string {
	if len(episode) == 1 {
		episode = "0" + episode
	}

	return "E" + episode
}

func normalizeBluDVReleaseQualityForSonarr(title, releaseTitle string, torrentIndex, torrentCount int, sizes []string) string {
	matches := bludvMultiResolutionRE.FindStringIndex(title)
	if len(matches) == 0 {
		return title
	}

	resolutionBlock := title[matches[0]:matches[1]]
	resolutions := extractBluDVResolutions(resolutionBlock)
	if len(resolutions) < 2 {
		return title
	}

	selectedResolution := selectBluDVResolution(releaseTitle, torrentIndex, torrentCount, sizes, resolutions)
	if selectedResolution == "" {
		return title
	}

	return fmt.Sprintf("%s%s%s", title[:matches[0]], selectedResolution, title[matches[1]:])
}

func normalizeBluDVReleaseQualityFromLabelForSonarr(title, label string) string {
	resolutions := extractBluDVResolutions(label)
	if len(resolutions) != 1 {
		return title
	}

	matches := bludvMultiResolutionRE.FindStringIndex(title)
	if len(matches) == 0 {
		return title
	}

	return fmt.Sprintf("%s%s%s", title[:matches[0]], resolutions[0], title[matches[1]:])
}

func normalizeBluDVReleaseEpisodeForSonarr(title, episode string) string {
	if episode == "" || bludvSonarrSeasonEpisodeRE.MatchString(title) {
		return title
	}

	if bludvSonarrSeasonRE.MatchString(title) {
		return addBluDVEpisodeToExistingSonarrSeason(title, episode)
	}

	return title
}

func selectBluDVResolution(releaseTitle string, torrentIndex, torrentCount int, sizes, resolutions []string) string {
	releaseResolutions := extractBluDVResolutions(releaseTitle)
	if len(releaseResolutions) == 1 {
		return releaseResolutions[0]
	}

	if torrentCount == len(sizes) {
		return selectBluDVResolutionBySize(torrentIndex, sizes, resolutions)
	}

	return ""
}

func selectBluDVResolutionBySize(torrentIndex int, sizes, resolutions []string) string {
	if torrentIndex < 0 || torrentIndex >= len(sizes) || len(sizes) != len(resolutions) {
		return ""
	}

	type indexedSize struct {
		index int
		bytes int64
	}

	indexedSizes := make([]indexedSize, 0, len(sizes))
	for idx, size := range sizes {
		bytes := utils.ParseSize(size)
		if bytes == 0 {
			return ""
		}

		indexedSizes = append(indexedSizes, indexedSize{index: idx, bytes: bytes})
	}

	sort.SliceStable(indexedSizes, func(i, j int) bool {
		return indexedSizes[i].bytes < indexedSizes[j].bytes
	})

	sortedResolutions := append([]string(nil), resolutions...)
	sort.SliceStable(sortedResolutions, func(i, j int) bool {
		return bludvResolutionRank(sortedResolutions[i]) < bludvResolutionRank(sortedResolutions[j])
	})

	for rank, indexedSize := range indexedSizes {
		if indexedSize.index == torrentIndex {
			return sortedResolutions[rank]
		}
	}

	return ""
}

func extractBluDVResolutions(text string) []string {
	matches := bludvResolutionRE.FindAllString(text, -1)
	resolutions := make([]string, 0, len(matches))
	seen := map[string]bool{}

	for _, match := range matches {
		resolution := normalizeBluDVResolution(match)
		if seen[resolution] {
			continue
		}

		seen[resolution] = true
		resolutions = append(resolutions, resolution)
	}

	return resolutions
}

func normalizeBluDVResolution(resolution string) string {
	resolution = strings.ToLower(strings.TrimSpace(resolution))
	if resolution == "4k" {
		return "2160p"
	}

	return resolution
}

func bludvResolutionRank(resolution string) int {
	switch normalizeBluDVResolution(resolution) {
	case "480p":
		return 480
	case "720p":
		return 720
	case "1080p":
		return 1080
	case "2160p":
		return 2160
	default:
		return 0
	}
}

func shouldAppendBluDVTransmissionTrackers() bool {
	value := strings.ToLower(strings.TrimSpace(utils.GetEnvOrDefault("INDEXER_BLUDV_APPEND_TRACKERS", "false")))
	return value == "1" || value == "true" || value == "yes"
}

func extractBluDVSystemAdsMagnetLinks(ctx context.Context, i *Indexer, content *goquery.Selection, referer string) []bludvMagnetLink {
	htmlContent, err := content.Html()
	if err != nil {
		return nil
	}

	matches := bludvSystemAdsAnchorRE.FindAllStringSubmatchIndex(htmlContent, -1)
	links := make([]bludvMagnetLink, 0, len(matches))
	currentEpisode := ""
	lastEnd := 0

	for _, match := range matches {
		between := cleanBluDVHTMLText(htmlContent[lastEnd:match[0]])
		if episode := extractBluDVEpisode(between); episode != "" {
			currentEpisode = episode
		}

		href := html.UnescapeString(htmlContent[match[2]:match[3]])
		label := cleanBluDVHTMLText(htmlContent[match[4]:match[5]])
		if len(extractBluDVResolutions(label)) != 1 {
			label = strings.Join(strings.Fields(fmt.Sprintf("%s %s", between, label)), " ")
		}
		magnetLink, err := getMagnetFromSystemAds(ctx, i, href, referer)
		if err != nil {
			logging.Warn().Err(err).Str("href", href).Msg("Failed to decode systemads link")
			lastEnd = match[1]
			continue
		}

		links = append(links, bludvMagnetLink{
			URL:     magnetLink,
			Label:   label,
			Episode: currentEpisode,
		})
		lastEnd = match[1]
	}

	return links
}

func getTorrentsBluDV(ctx context.Context, i *Indexer, link, referer string) ([]schema.IndexedTorrent, error) {
	var indexedTorrents []schema.IndexedTorrent
	doc, err := getDocument(ctx, i, link, referer)
	if err != nil {
		return nil, err
	}

	article := doc.Find(".post")
	title := strings.Replace(article.Find(".title > h1").Text(), " - Download", "", -1)
	textContent := article.Find("div.content")
	date := getPublishedDate(doc)
	magnets := textContent.Find("a[href^=\"magnet\"]")
	var magnetLinks []bludvMagnetLink
	magnets.Each(func(i int, s *goquery.Selection) {
		magnetLink, _ := s.Attr("href")
		magnetLinks = append(magnetLinks, bludvMagnetLink{
			URL:   magnetLink,
			Label: strings.TrimSpace(s.Text()),
		})
	})
	magnetLinks = append(magnetLinks, extractBluDVSystemAdsMagnetLinks(ctx, i, textContent, link)...)

	adwareDomains := []string{
		"https://www.seuvideo.xyz",
		"https://www.systemads.org",
		"https://superadsgo.xyz",
	}

	// Process adware links for each domain in the list
	for _, domain := range adwareDomains {
		adwareLinks := textContent.Find(fmt.Sprintf("a[href^=\"%s\"]", domain))
		adwareLinks.Each(func(_ int, s *goquery.Selection) {
			href, _ := s.Attr("href")
			// extract querysting "id" from url
			parsedUrl, err := url.Parse(href)
			if err != nil {
				logging.Error().Err(err).Str("href", href).Msg("Failed to parse URL")
				return
			}
			magnetLink := parsedUrl.Query().Get("id")
			magnetLinkDecoded, err := utils.DecodeAdLink(magnetLink)
			if err != nil {
				logging.Error().Err(err).Str("href", href).Msg("Failed to decode ad link")
				return
			}

			// if decoded magnet link is indeed a magnet link, append it
			if strings.HasPrefix(magnetLinkDecoded, "magnet:") {
				magnetLinks = append(magnetLinks, bludvMagnetLink{
					URL:   magnetLinkDecoded,
					Label: strings.TrimSpace(s.Text()),
				})
			} else if !strings.Contains(magnetLinkDecoded, "watch.brplayer") {
				logging.Warn().
					Str("href", href).
					Str("decoded", magnetLinkDecoded).
					Str("indexer", bludv.Label).
					Msg("Link decoding resulted in non-magnet link")
			}
		})
	}

	var audio []schema.Audio
	var year string
	var size []string
	article.Find("div.content p").Each(func(i int, s *goquery.Selection) {
		// pattern:
		// Título Traduzido: Fundação
		// Título Original: Foundation
		// IMDb: 7,5
		// Ano de Lançamento: 2023
		// Gênero: Ação | Aventura | Ficção
		// Formato: MKV
		// Qualidade: WEB-DL
		// Áudio: Português | Inglês
		// Idioma: Português | Inglês
		// Legenda: Português
		// Tamanho: –
		// Qualidade de Áudio: 10
		// Qualidade de Vídeo: 10
		// Duração: 59 Min.
		// Servidor: Torrent
		text := s.Text()

		audio = append(audio, findAudioFromText(text)...)
		y := findYearFromText(text, title)
		if y != "" {
			year = y
		}
		size = append(size, findSizesFromText(text)...)
	})

	// find any link from imdb
	imdbLink := ""
	article.Find("div.content a").Each(func(i int, s *goquery.Selection) {
		link, _ := s.Attr("href")
		_imdbLink, err := getIMDBLink(link)
		if err == nil {
			imdbLink = _imdbLink
		}
	})

	size = utils.StableUniq(size)

	var chanIndexedTorrent = make(chan schema.IndexedTorrent)

	// for each magnet link, create a new indexed torrent
	for it, magnetLink := range magnetLinks {
		it := it
		go func(it int, magnetLink bludvMagnetLink) {
			parsedMagnet, err := magnet.ParseMagnetUri(magnetLink.URL)
			if err != nil {
				logging.Error().Err(err).Str("magnet_link", magnetLink.URL).Msg("Failed to parse magnet URI")
			}
			releaseTitle := parsedMagnet.DisplayName
			infoHash := parsedMagnet.InfoHash.String()
			trackers := parsedMagnet.Trackers
			magnetAudio := getAudioFromTitle(releaseTitle, audio)

			peer, seed, err := goscrape.GetLeechsAndSeeds(ctx, i.redis, i.metrics, infoHash, trackers)
			if err != nil {
				logging.Error().Err(err).Str("info_hash", infoHash).Msg("Failed to get leechers and seeders")
			}

			// if the number of sizes is equal to the number of magnets, then assign the size to each indexed torrent in order
			var mySize string
			if len(size) == len(magnetLinks) {
				mySize = size[it]
			} else if len(size) == 1 && magnetLink.Episode == "" {
				mySize = size[0]
			}
			if mySize == "" {
				go func() {
					_, _ = i.magnetMetadataAPI.FetchMetadata(ctx, magnetLink.URL)
				}()
			}

			title := processTitle(title, magnetAudio)
			title = normalizeBluDVReleaseTitleForSonarr(title)
			title = normalizeBluDVReleaseQualityForSonarr(title, releaseTitle, it, len(magnetLinks), size)
			title = normalizeBluDVReleaseQualityFromLabelForSonarr(title, magnetLink.Label)
			title = normalizeBluDVReleaseEpisodeForSonarr(title, magnetLink.Episode)

			downloadMagnetLink := magnetLink.URL
			if shouldAppendBluDVTransmissionTrackers() {
				downloadMagnetLink = appendTrackersToMagnetLink(downloadMagnetLink, bludvTransmissionTrackers)
			}

			ixt := schema.IndexedTorrent{
				Title:         releaseTitle,
				OriginalTitle: title,
				Details:       link,
				Year:          year,
				IMDB:          imdbLink,
				Audio:         magnetAudio,
				MagnetLink:    downloadMagnetLink,
				Date:          date,
				InfoHash:      infoHash,
				Trackers:      trackers,
				LeechCount:    peer,
				SeedCount:     seed,
				Size:          mySize,
			}
			chanIndexedTorrent <- ixt
		}(it, magnetLink)
	}

	for i := 0; i < len(magnetLinks); i++ {
		it := <-chanIndexedTorrent
		indexedTorrents = append(indexedTorrents, it)
	}

	return indexedTorrents, nil
}

func getPublishedDate(document *goquery.Document) time.Time {
	var date time.Time
	//<meta property="article:published_time" content="2019-08-23T13:20:57+00:00">
	datePublished := strings.TrimSpace(document.Find("meta[property=\"article:published_time\"]").AttrOr("content", ""))

	if datePublished != "" {
		date, _ = time.Parse(time.RFC3339, datePublished)
	}

	return date
}
