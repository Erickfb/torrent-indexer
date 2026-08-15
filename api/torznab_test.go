package handler

import (
	"encoding/xml"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTorznabCaps(t *testing.T) {
	service := newAnimeBRService(testAnimeBRConfig("https://feed.invalid"), nil, nil)
	request := httptest.NewRequest("GET", "/api?t=caps", nil)
	recorder := httptest.NewRecorder()

	service.HandlerTorznab(recorder, request)

	if recorder.Code != 200 {
		t.Fatalf("status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `<tv-search available="yes" supportedParams="q,season,ep"></tv-search>`) {
		t.Fatalf("caps missing TV search: %s", body)
	}
	if !strings.Contains(body, `id="5070" name="Anime"`) {
		t.Fatalf("caps missing anime category: %s", body)
	}
	var root struct{ XMLName xml.Name }
	if err := xml.Unmarshal(recorder.Body.Bytes(), &root); err != nil || root.XMLName.Local != "caps" {
		t.Fatalf("invalid caps XML: root=%v err=%v", root.XMLName, err)
	}
}

func TestTorznabTVSearchReturnsVerifiedRelease(t *testing.T) {
	server := newAnimeBRFixtureServer(t)
	defer server.Close()
	service := newAnimeBRService(testAnimeBRConfig(server.URL), nil, server.Client())
	request := httptest.NewRequest("GET", "/api?t=tvsearch&q=Example+Show&season=2&ep=10&tvdbid=1234&extended=1", nil)
	recorder := httptest.NewRecorder()

	service.HandlerTorznab(recorder, request)

	body := recorder.Body.String()
	if recorder.Code != 200 || !strings.Contains(recorder.Header().Get("Content-Type"), "application/xml") {
		t.Fatalf("status=%d content-type=%q body=%s", recorder.Code, recorder.Header().Get("Content-Type"), body)
	}
	checks := []string{
		"Example.Show.S02E10.1080p.WEB-DL.H.264 [Brazilian]",
		`name="subs" value="Portuguese (Brazil)"`,
		`name="category" value="5070"`,
		`name="season" value="2"`,
		`name="episode" value="10"`,
		`newznab:response offset="0" total="1"`,
		`&amp;tr=https://tracker.example/announce`,
	}
	for _, check := range checks {
		if !strings.Contains(body, check) {
			t.Errorf("Torznab body missing %q:\n%s", check, body)
		}
	}
	if strings.Contains(body, "S2 - 10") {
		t.Fatalf("ambiguous payload leaked into feed: %s", body)
	}
	var root struct{ XMLName xml.Name }
	if err := xml.Unmarshal(recorder.Body.Bytes(), &root); err != nil || root.XMLName.Local != "rss" {
		t.Fatalf("invalid RSS XML: root=%v err=%v\n%s", root.XMLName, err, body)
	}
}

func TestTorznabRejectsInvalidPagingAsXML(t *testing.T) {
	service := newAnimeBRService(testAnimeBRConfig("https://feed.invalid"), nil, nil)
	request := httptest.NewRequest("GET", "/api?t=search&offset=-1", nil)
	recorder := httptest.NewRecorder()

	service.HandlerTorznab(recorder, request)

	body := recorder.Body.String()
	if recorder.Code != 200 || !strings.Contains(body, `<error code="201" description="Invalid offset"></error>`) {
		t.Fatalf("unexpected response: status=%d body=%s", recorder.Code, body)
	}
}
