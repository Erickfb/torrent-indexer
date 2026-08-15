package handler

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	torznabDefaultLimit = 50
	torznabMaximumLimit = 100
)

type torznabCaps struct {
	XMLName      xml.Name            `xml:"caps"`
	Server       torznabCapsServer   `xml:"server"`
	Limits       torznabCapsLimits   `xml:"limits"`
	Registration torznabRegistration `xml:"registration"`
	Searching    torznabSearching    `xml:"searching"`
	Categories   torznabCategories   `xml:"categories"`
}

type torznabCapsServer struct {
	Version   string `xml:"version,attr"`
	Title     string `xml:"title,attr"`
	Strapline string `xml:"strapline,attr"`
}

type torznabCapsLimits struct {
	Maximum int `xml:"max,attr"`
	Default int `xml:"default,attr"`
}

type torznabRegistration struct {
	Available string `xml:"available,attr"`
	Open      string `xml:"open,attr"`
}

type torznabSearching struct {
	Search      torznabSearchCapability `xml:"search"`
	TVSearch    torznabSearchCapability `xml:"tv-search"`
	MovieSearch torznabSearchCapability `xml:"movie-search"`
	AudioSearch torznabSearchCapability `xml:"audio-search"`
	BookSearch  torznabSearchCapability `xml:"book-search"`
}

type torznabSearchCapability struct {
	Available       string `xml:"available,attr"`
	SupportedParams string `xml:"supportedParams,attr,omitempty"`
}

type torznabCategories struct {
	Categories []torznabCategory `xml:"category"`
}

type torznabCategory struct {
	ID            int               `xml:"id,attr"`
	Name          string            `xml:"name,attr"`
	Description   string            `xml:"description,attr,omitempty"`
	Subcategories []torznabCategory `xml:"subcat"`
}

type torznabRSS struct {
	XMLName      xml.Name       `xml:"rss"`
	Version      string         `xml:"version,attr"`
	XMLNSTorznab string         `xml:"xmlns:torznab,attr"`
	XMLNSNewznab string         `xml:"xmlns:newznab,attr"`
	Channel      torznabChannel `xml:"channel"`
}

type torznabChannel struct {
	Title       string          `xml:"title"`
	Link        string          `xml:"link"`
	Description string          `xml:"description"`
	Language    string          `xml:"language"`
	Response    torznabResponse `xml:"newznab:response"`
	Items       []torznabItem   `xml:"item"`
}

type torznabResponse struct {
	Offset int `xml:"offset,attr"`
	Total  int `xml:"total,attr"`
}

type torznabItem struct {
	Title       string             `xml:"title"`
	GUID        torznabGUID        `xml:"guid"`
	Link        string             `xml:"link"`
	Comments    string             `xml:"comments"`
	PubDate     string             `xml:"pubDate"`
	Description string             `xml:"description"`
	Enclosure   torznabEnclosure   `xml:"enclosure"`
	Attributes  []torznabAttribute `xml:"torznab:attr"`
}

type torznabGUID struct {
	IsPermaLink string `xml:"isPermaLink,attr"`
	Value       string `xml:",chardata"`
}

type torznabEnclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

type torznabAttribute struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

type torznabError struct {
	XMLName     xml.Name `xml:"error"`
	Code        int      `xml:"code,attr"`
	Description string   `xml:"description,attr"`
}

func (s *AnimeBRService) HandlerTorznab(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeTorznabError(w, 201, "Only GET requests are supported")
		return
	}

	function := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("t")))
	switch function {
	case "caps":
		writeTorznabXML(w, torznabCaps{
			Server:       torznabCapsServer{Version: "1.0", Title: "Anime BR Verified", Strapline: "Verified Brazilian Portuguese anime subtitles"},
			Limits:       torznabCapsLimits{Maximum: torznabMaximumLimit, Default: torznabDefaultLimit},
			Registration: torznabRegistration{Available: "no", Open: "yes"},
			Searching: torznabSearching{
				Search:      torznabSearchCapability{Available: "yes", SupportedParams: "q"},
				TVSearch:    torznabSearchCapability{Available: "yes", SupportedParams: "q,season,ep"},
				MovieSearch: torznabSearchCapability{Available: "no"},
				AudioSearch: torznabSearchCapability{Available: "no"},
				BookSearch:  torznabSearchCapability{Available: "no"},
			},
			Categories: torznabCategories{Categories: []torznabCategory{{
				ID: 5000, Name: "TV", Subcategories: []torznabCategory{{ID: 5070, Name: "Anime", Description: "Anime with verified Portuguese (Brazil) subtitles"}},
			}}},
		})
	case "search", "tvsearch":
		s.handleTorznabSearch(w, r)
	case "":
		writeTorznabError(w, 200, "Missing t parameter")
	default:
		writeTorznabError(w, 202, "Unsupported function: "+function)
	}
}

func (s *AnimeBRService) handleTorznabSearch(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()
	offset, err := parseTorznabNonNegativeInt(values.Get("offset"), 0)
	if err != nil {
		writeTorznabError(w, 201, "Invalid offset")
		return
	}
	limit, err := parseTorznabNonNegativeInt(values.Get("limit"), torznabDefaultLimit)
	if err != nil || limit == 0 {
		writeTorznabError(w, 201, "Invalid limit")
		return
	}
	limit = min(limit, torznabMaximumLimit)
	if !torznabCategoryAllowsAnime(values.Get("cat")) {
		writeTorznabFeed(w, r, nil, offset, 0)
		return
	}

	request, err := animeBRRequestFromQuery(values)
	if err != nil {
		writeTorznabError(w, 201, err.Error())
		return
	}
	releases, err := s.Search(r.Context(), request)
	if err != nil {
		writeTorznabError(w, 900, err.Error())
		return
	}
	total := len(releases)
	if offset >= total {
		releases = nil
	} else {
		end := min(offset+limit, total)
		releases = releases[offset:end]
	}
	writeTorznabFeed(w, r, releases, offset, total)
}

func writeTorznabFeed(w http.ResponseWriter, r *http.Request, releases []AnimeBRRelease, offset, total int) {
	items := make([]torznabItem, 0, len(releases))
	for _, release := range releases {
		peers := release.Seeders + release.Leechers
		attributes := []torznabAttribute{
			{Name: "size", Value: strconv.FormatInt(release.Size, 10)},
			{Name: "category", Value: "5000"},
			{Name: "category", Value: "5070"},
			{Name: "seeders", Value: strconv.Itoa(release.Seeders)},
			{Name: "leechers", Value: strconv.Itoa(release.Leechers)},
			{Name: "peers", Value: strconv.Itoa(peers)},
			{Name: "infohash", Value: strings.ToLower(release.InfoHash)},
			{Name: "magneturl", Value: release.MagnetURL},
			{Name: "subs", Value: "Portuguese (Brazil)"},
			{Name: "language", Value: "Japanese"},
			{Name: "tag", Value: "ptbr-verified"},
		}
		if release.Episode > 0 {
			attributes = append(attributes, torznabAttribute{Name: "season", Value: strconv.Itoa(release.Season)})
		}
		if release.Episode > 0 {
			attributes = append(attributes, torznabAttribute{Name: "episode", Value: strconv.Itoa(release.Episode)})
		}
		if release.TVDBID > 0 {
			attributes = append(attributes, torznabAttribute{Name: "tvdbid", Value: strconv.FormatInt(release.TVDBID, 10)})
		}

		downloadURL := release.DownloadURL
		enclosureType := "application/x-bittorrent"
		if strings.HasPrefix(downloadURL, "magnet:") {
			enclosureType = "application/x-bittorrent;x-scheme-handler/magnet"
		}
		pubDate := release.Published
		if pubDate.IsZero() {
			pubDate = time.Now().UTC()
		}
		items = append(items, torznabItem{
			Title:       release.Title,
			GUID:        torznabGUID{IsPermaLink: "false", Value: "urn:btih:" + strings.ToLower(release.InfoHash)},
			Link:        release.Details,
			Comments:    release.Details,
			PubDate:     pubDate.Format(time.RFC1123Z),
			Description: strings.Join(release.Evidence, "; "),
			Enclosure:   torznabEnclosure{URL: downloadURL, Length: release.Size, Type: enclosureType},
			Attributes:  attributes,
		})
	}

	baseURL := requestBaseURL(r)
	writeTorznabXML(w, torznabRSS{
		Version:      "2.0",
		XMLNSTorznab: "http://torznab.com/schemas/2015/feed",
		XMLNSNewznab: "http://www.newznab.com/DTD/2010/feeds/attributes/",
		Channel: torznabChannel{
			Title:       "Anime BR Verified",
			Link:        baseURL,
			Description: "Anime releases with independently verified Portuguese (Brazil) subtitles",
			Language:    "pt-BR",
			Response:    torznabResponse{Offset: offset, Total: total},
			Items:       items,
		},
	})
}

func writeTorznabError(w http.ResponseWriter, code int, description string) {
	writeTorznabXML(w, torznabError{Code: code, Description: description})
}

func writeTorznabXML(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(xml.Header))
	encoder := xml.NewEncoder(w)
	encoder.Indent("", "  ")
	_ = encoder.Encode(value)
}

func parseTorznabNonNegativeInt(value string, fallback int) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("invalid non-negative integer")
	}
	return parsed, nil
}

func torznabCategoryAllowsAnime(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true
	}
	for _, category := range strings.Split(raw, ",") {
		switch strings.TrimSpace(category) {
		case "5000", "5070":
			return true
		}
	}
	return false
}

func requestBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); forwarded == "http" || forwarded == "https" {
		scheme = forwarded
	}
	host := r.Host
	if host == "" {
		host = "localhost"
	}
	return (&url.URL{Scheme: scheme, Host: host, Path: "/api"}).String()
}
