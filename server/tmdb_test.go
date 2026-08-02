package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

// testTMDBConfig is the minimum a TMDBSource needs, with the region set so
// query assertions do not depend on LoadConfig having run.
func testTMDBConfig() Config {
	return Config{TMDBReadToken: "x", TMDBWatchRegion: defaultWatchRegion}
}

func newTestTMDBSource(t *testing.T, handler http.HandlerFunc) (*TMDBSource, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	s := NewTMDBSource(
		Config{TMDBReadToken: "test-token", TMDBWatchRegion: defaultWatchRegion},
		StreamingProvider{ID: SourceNetflix, Name: "Netflix", TMDBID: 8},
	)
	if s == nil {
		t.Fatalf("NewTMDBSource returned nil for a resolved provider with a token")
	}
	s.baseURL = srv.URL
	s.imageURL = srv.URL + "/img"
	return s, srv
}

// Which providers exist is decided by resolution, not here; the only thing a
// source still refuses to be built without is a token.
func TestNewTMDBSourceRequiresToken(t *testing.T) {
	p := StreamingProvider{ID: SourceNetflix, Name: "Netflix", TMDBID: 8}
	if got := NewTMDBSource(Config{}, p); got != nil {
		t.Fatalf("expected nil with no token, got %+v", got)
	}
	if got := NewTMDBSource(Config{TMDBReadToken: "x"}, p); got == nil {
		t.Fatalf("expected a source for a resolved provider with a token")
	}
}

func TestDiscoverParamsMapsFilters(t *testing.T) {
	s := NewTMDBSource(testTMDBConfig(), StreamingProvider{ID: SourceNetflix, Name: "Netflix", TMDBID: 8})
	f := Filters{
		Genres:    []string{"Western", "Comedy", "Neo-Noir"},
		YearMin:   1980,
		YearMax:   2020,
		RatingMin: 7.5,
	}
	q := s.discoverParams(f, "PG-13", 3)

	checks := map[string]string{
		"with_watch_providers":          "8",
		"watch_region":                  "US",
		"with_watch_monetization_types": "flatrate",
		"vote_count.gte":                "200",
		"page":                          "3",
		"primary_release_date.gte":      "1980-01-01",
		"primary_release_date.lte":      "2020-12-31",
		"vote_average.gte":              "7.5",
		"certification_country":         "US",
		"certification":                 "PG-13",
	}
	for k, want := range checks {
		if got := q.Get(k); got != want {
			t.Fatalf("param %q = %q, want %q", k, got, want)
		}
	}
	// Western=37, Comedy=35, Neo-Noir has no equivalent and is dropped.
	if got := q.Get("with_genres"); got != "37|35" {
		t.Fatalf("with_genres = %q, want %q", got, "37|35")
	}
	if q.Has("certification.lte") {
		t.Fatalf("certification.lte must never be sent; it would admit unselected ratings")
	}
}

func TestDiscoverParamsOmitsUnsetFilters(t *testing.T) {
	s := NewTMDBSource(testTMDBConfig(), StreamingProvider{ID: SourceDisney, Name: "Disney+", TMDBID: 337})
	q := s.discoverParams(Filters{}, "", 1)
	for _, k := range []string{"with_genres", "primary_release_date.gte", "primary_release_date.lte", "vote_average.gte", "certification", "certification_country"} {
		if q.Has(k) {
			t.Fatalf("param %q should be absent for empty filters, got %q", k, q.Get(k))
		}
	}
	if got := q.Get("with_watch_providers"); got != "337" {
		t.Fatalf("with_watch_providers = %q, want 337", got)
	}
}

func TestSamplePagesClampsToTotal(t *testing.T) {
	s := NewTMDBSource(testTMDBConfig(), StreamingProvider{ID: SourceNetflix, Name: "Netflix", TMDBID: 8})

	if got := s.samplePages(0); got != nil {
		t.Fatalf("samplePages(0) = %v, want nil", got)
	}
	if got := s.samplePages(1); len(got) != 1 || got[0] != 1 {
		t.Fatalf("samplePages(1) = %v, want [1]", got)
	}
	if got := s.samplePages(3); len(got) != 3 {
		t.Fatalf("samplePages(3) = %v, want 3 pages", got)
	}
	for _, total := range []int{4, 25, 74, 1169} {
		got := s.samplePages(total)
		if len(got) != tmdbPagesPerProvider {
			t.Fatalf("samplePages(%d) returned %d pages, want %d", total, len(got), tmdbPagesPerProvider)
		}
		seen := map[int]bool{}
		for _, p := range got {
			if p < 1 || p > total || p > tmdbMaxSampledPages {
				t.Fatalf("samplePages(%d) returned out-of-range page %d", total, p)
			}
			if seen[p] {
				t.Fatalf("samplePages(%d) returned duplicate page %d", total, p)
			}
			seen[p] = true
		}
	}
}

func TestSearchBuildsProxiedMovies(t *testing.T) {
	var gotAuth string
	s, _ := newTestTMDBSource(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"page":        1,
			"total_pages": 1,
			"results": []map[string]any{
				{"id": 603, "title": "The Matrix", "release_date": "1999-03-30",
					"overview": "o", "genre_ids": []int{28, 878}, "vote_average": 8.2,
					"poster_path": "/abc.jpg"},
				{"id": 999, "title": "No Poster", "release_date": "2001-01-01",
					"poster_path": ""},
			},
		})
	})

	movies, err := s.Search(context.Background(), Filters{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotAuth != "Bearer test-token" {
		t.Fatalf("Authorization = %q, want %q", gotAuth, "Bearer test-token")
	}
	if len(movies) != 1 {
		t.Fatalf("expected 1 movie (the poster-less one is dropped), got %d", len(movies))
	}
	m := movies[0]
	if m.ID != "tmdb:603" {
		t.Fatalf("ID = %q, want tmdb:603", m.ID)
	}
	if m.Year != 1999 {
		t.Fatalf("Year = %d, want 1999", m.Year)
	}
	if m.PosterURL != "/api/images/netflix/abc.jpg" {
		t.Fatalf("PosterURL = %q, want /api/images/netflix/abc.jpg", m.PosterURL)
	}
	if len(m.Availability) != 1 || m.Availability[0].Source != SourceNetflix {
		t.Fatalf("Availability = %+v, want one netflix entry", m.Availability)
	}
	if m.Runtime != 0 || m.OfficialRating != "" {
		t.Fatalf("Discover returns neither runtime nor certification; got runtime=%d rating=%q", m.Runtime, m.OfficialRating)
	}
	source, id, _ := parsePosterRef(m.PosterURL)
	if source != SourceNetflix || id != "abc.jpg" {
		t.Fatalf("poster ref round-trip failed: source=%q id=%q", source, id)
	}
}

func TestSearchIssuesOneQueryPerCertification(t *testing.T) {
	var certs []string
	s, _ := newTestTMDBSource(t, func(w http.ResponseWriter, r *http.Request) {
		q, _ := url.ParseQuery(r.URL.RawQuery)
		certs = append(certs, q.Get("certification"))
		_ = json.NewEncoder(w).Encode(map[string]any{"page": 1, "total_pages": 1, "results": []any{}})
	})

	if _, err := s.Search(context.Background(), Filters{OfficialRatings: []string{"G", "PG", "Unrated-Nonsense"}}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(certs) != 2 {
		t.Fatalf("expected 2 requests (one per recognized certification), got %d: %v", len(certs), certs)
	}
	if certs[0] != "G" || certs[1] != "PG" {
		t.Fatalf("certifications = %v, want [G PG]", certs)
	}
}

func TestSearchSamplesMultiplePages(t *testing.T) {
	var pagesRequested []int
	s, _ := newTestTMDBSource(t, func(w http.ResponseWriter, r *http.Request) {
		pageNum, _ := strconv.Atoi(r.URL.Query().Get("page"))
		pagesRequested = append(pagesRequested, pageNum)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"page":        pageNum,
			"total_pages": 10,
			"results": []map[string]any{
				{"id": pageNum, "title": "T", "release_date": "2001-01-01",
					"poster_path": "/p.jpg"},
			},
		})
	})

	movies, err := s.Search(context.Background(), Filters{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	// Each stub page returns exactly one movie whose id is its page number, so
	// the result size is the number of pages represented. That is exactly the
	// sample size; the request count is not asserted because page 1 is always
	// probed to learn total_pages and is wasted when unsampled.
	if len(movies) != tmdbPagesPerProvider {
		t.Fatalf("got %d movies, want one per sampled page (%d); requests were %v",
			len(movies), tmdbPagesPerProvider, pagesRequested)
	}

	seen := map[int]bool{}
	for _, p := range pagesRequested {
		if p < 1 || p > 10 {
			t.Fatalf("page %d out of range, requests were %v", p, pagesRequested)
		}
		if seen[p] {
			t.Fatalf("page %d requested twice: %v", p, pagesRequested)
		}
		seen[p] = true
	}
	if !seen[1] {
		t.Fatalf("page 1 must always be requested to learn total_pages; requests were %v", pagesRequested)
	}
}

func TestSearchReturnsNothingWhenNoGenreMaps(t *testing.T) {
	called := false
	s, _ := newTestTMDBSource(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		_ = json.NewEncoder(w).Encode(map[string]any{"page": 1, "total_pages": 1, "results": []any{}})
	})

	movies, err := s.Search(context.Background(), Filters{Genres: []string{"Anime", "Sports"}})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if called {
		t.Fatalf("no selected genre maps to TMDB; the provider must not be queried unfiltered")
	}
	if len(movies) != 0 {
		t.Fatalf("got %d movies, want 0", len(movies))
	}
}

func TestSearchReturnsNothingWhenNoCertificationMaps(t *testing.T) {
	called := false
	s, _ := newTestTMDBSource(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		_ = json.NewEncoder(w).Encode(map[string]any{"page": 1, "total_pages": 1, "results": []any{}})
	})

	movies, err := s.Search(context.Background(), Filters{OfficialRatings: []string{"TV-14", "Approved"}})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if called {
		t.Fatalf("no selected certification maps to TMDB; the provider must not be queried unfiltered")
	}
	if len(movies) != 0 {
		t.Fatalf("got %d movies, want 0", len(movies))
	}
}

func TestSearchCapsResultsAtLimit(t *testing.T) {
	s, _ := newTestTMDBSource(t, func(w http.ResponseWriter, r *http.Request) {
		results := make([]map[string]any, 0, 20)
		page := r.URL.Query().Get("page")
		for i := 0; i < 20; i++ {
			results = append(results, map[string]any{
				"id": len(page)*1000 + i, "title": "T", "release_date": "2001-01-01",
				"poster_path": "/p.jpg",
			})
		}
		pageNum, _ := strconv.Atoi(page)
		_ = json.NewEncoder(w).Encode(map[string]any{"page": pageNum, "total_pages": 10, "results": results})
	})

	movies, err := s.Search(context.Background(), Filters{Limit: 7})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(movies) != 7 {
		t.Fatalf("got %d movies, want the Limit of 7", len(movies))
	}
}

func TestSearchPropagatesUpstreamFailure(t *testing.T) {
	s, _ := newTestTMDBSource(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
	if _, err := s.Search(context.Background(), Filters{}); err == nil {
		t.Fatalf("expected an error from a 401 response, got nil")
	}
}
