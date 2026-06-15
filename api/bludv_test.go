package handler

import (
	"testing"

	"github.com/Erickfb/torrent-indexer/schema"
)

func TestNormalizeBluDVSearchQueryConvertsSeasonEpisode(t *testing.T) {
	got := normalizeBluDVSearchQuery("rick and morty s01e02")
	want := "rick and morty temporada 1"

	if got != want {
		t.Fatalf("normalizeBluDVSearchQuery() = %q, want %q", got, want)
	}
}

func TestNormalizeBluDVSearchQueryDropsEpisodeFromTemporadaQuery(t *testing.T) {
	got := normalizeBluDVSearchQuery("rick and morty temporada 1 episodio 2")
	want := "rick and morty temporada 1"

	if got != want {
		t.Fatalf("normalizeBluDVSearchQuery() = %q, want %q", got, want)
	}
}

func TestNormalizeBluDVSearchQueryConvertsSeasonEpisodeWithSeparators(t *testing.T) {
	got := normalizeBluDVSearchQuery("Rick.And.Morty.S01.E02.1080p")
	want := "Rick.And.Morty.temporada 1.1080p"

	if got != want {
		t.Fatalf("normalizeBluDVSearchQuery() = %q, want %q", got, want)
	}
}

func TestNormalizeBluDVSearchQueryLeavesOtherQueriesUnchanged(t *testing.T) {
	got := normalizeBluDVSearchQuery("vingadores ultimato")
	want := "vingadores ultimato"

	if got != want {
		t.Fatalf("normalizeBluDVSearchQuery() = %q, want %q", got, want)
	}
}

func TestFilterBluDVSeasonResultsKeepsRequestedSeason(t *testing.T) {
	torrents := []schema.IndexedTorrent{
		{OriginalTitle: "Rick and Morty 7a Temporada Torrent"},
		{OriginalTitle: "Rick and Morty 1a Temporada Torrent"},
		{Title: "Rick and Morty S01 WEB-DL"},
	}

	got := filterBluDVSeasonResults("rick and morty temporada 1 episodio 2", torrents)

	if len(got) != 2 {
		t.Fatalf("got %d torrents, want 2", len(got))
	}
	if got[0].OriginalTitle != "Rick and Morty 1a Temporada Torrent" {
		t.Fatalf("first result = %q", got[0].OriginalTitle)
	}
	if got[1].Title != "Rick and Morty S01 WEB-DL" {
		t.Fatalf("second result = %q", got[1].Title)
	}
}

func TestFilterBluDVSeasonResultsRejectsSpinOffTitle(t *testing.T) {
	torrents := []schema.IndexedTorrent{
		{OriginalTitle: "Rick e Morty: O Anime 1a Temporada Torrent"},
		{OriginalTitle: "Rick and Morty 1a Temporada Torrent"},
	}

	got := filterBluDVSeasonResults("rick e morty s01e01", torrents)

	if len(got) != 1 {
		t.Fatalf("got %d torrents, want 1", len(got))
	}
	if got[0].OriginalTitle != "Rick and Morty 1a Temporada Torrent" {
		t.Fatalf("result = %q", got[0].OriginalTitle)
	}
}

func TestFilterBluDVSeasonResultsReturnsEmptyWhenSeasonDoesNotMatch(t *testing.T) {
	torrents := []schema.IndexedTorrent{
		{OriginalTitle: "Rick and Morty 7a Temporada Torrent"},
		{OriginalTitle: "Rick and Morty 5a Temporada Torrent"},
	}

	got := filterBluDVSeasonResults("rick and morty s01e02", torrents)

	if len(got) != 0 {
		t.Fatalf("got %d torrents, want 0", len(got))
	}
}

func TestBuildBluDVURLUsesSearchPagination(t *testing.T) {
	got := buildBluDVURL(IndexerMeta{URL: "https://bludv2.xyz/", SearchURL: "?s="}, "rick and morty temporada 1", "2")
	want := "https://bludv2.xyz/page/2/?s=rick+and+morty+temporada+1"

	if got != want {
		t.Fatalf("buildBluDVURL() = %q, want %q", got, want)
	}
}

func TestFilterBluDVSearchLinksKeepsRequestedTitleAndSeason(t *testing.T) {
	links := []bludvSearchLink{
		{URL: "https://bludv2.xyz/rick-e-morty-o-anime-1a-temporada/", Title: "Rick e Morty: O Anime 1a Temporada"},
		{URL: "https://bludv2.xyz/rick-and-morty-7a-temporada/", Title: "Rick and Morty 7a Temporada"},
		{URL: "https://bludv2.xyz/rick-and-morty-1a-temporada/", Title: "Rick and Morty 1a Temporada"},
	}

	got := filterBluDVSearchLinks("rick e morty s01e01", "1", links)

	if len(got) != 1 {
		t.Fatalf("got %d links, want 1", len(got))
	}
	if got[0].URL != "https://bludv2.xyz/rick-and-morty-1a-temporada/" {
		t.Fatalf("URL = %q", got[0].URL)
	}
}

func TestMatchesBluDVRequestedTitleTreatsEAndAndAsEquivalent(t *testing.T) {
	if !matchesBluDVRequestedTitle("rick e morty s01e01", "Rick and Morty 1a Temporada Torrent") {
		t.Fatal("expected title to match")
	}
}

func TestMatchesBluDVRequestedTitleRejectsExtraSubtitle(t *testing.T) {
	if matchesBluDVRequestedTitle("rick e morty s01e01", "Rick e Morty: O Anime 1a Temporada") {
		t.Fatal("expected spin-off title not to match")
	}
}
