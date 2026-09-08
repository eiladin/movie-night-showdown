package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestMoviesRequestsProviderIds guards the Fields query param that carries
// TMDB ids across sources: without it, no Jellyfin movie ever merges with a
// streaming-source entry, silently.
func TestMoviesRequestsProviderIds(t *testing.T) {
	var gotFields string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotFields = r.URL.Query().Get("Fields")
		_, _ = w.Write([]byte(`{"Items":[],"TotalRecordCount":0}`))
	}))
	defer srv.Close()

	c := NewJellyfinClient(Config{JellyfinURL: srv.URL, JellyfinAPIKey: "k"}, libraryRef{})
	if _, _, err := c.Movies(context.Background(), Filters{}); err != nil {
		t.Fatalf("Movies: %v", err)
	}
	if !strings.Contains(gotFields, "ProviderIds") {
		t.Fatalf("Fields = %q, want it to contain ProviderIds", gotFields)
	}
}

// TestJellyfinClient_Movies is an integration test against a real Jellyfin
// server. It skips automatically unless JELLYFIN_URL/JELLYFIN_API_KEY are
// set in the environment.
func TestJellyfinClient_Movies(t *testing.T) {
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.JellyfinURL == "" || cfg.JellyfinAPIKey == "" {
		t.Skip("JELLYFIN_URL/JELLYFIN_API_KEY not set; skipping live Jellyfin integration test")
	}

	client := NewJellyfinClient(cfg, libraryRef{})
	ctx := context.Background()

	all, allCount, err := client.Movies(ctx, ParseFilters(nil))
	if err != nil {
		t.Fatalf("Movies(no filters): %v", err)
	}
	t.Logf("no filters: %d movies fetched, TotalRecordCount=%d", len(all), allCount)
	if allCount == 0 {
		t.Fatal("expected a non-zero movie count from the real Jellyfin library")
	}
	for _, m := range all {
		if !strings.HasPrefix(m.PosterURL, "/api/images/jellyfin/") {
			t.Fatalf("movie %q has non-proxied PosterURL %q", m.Title, m.PosterURL)
		}
		if m.ID == "" {
			t.Fatalf("movie %q has an empty ID", m.Title)
		}
		if m.ID[:3] != "jf:" && m.ID[:5] != "tmdb:" {
			t.Fatalf("movie %q has a non-namespaced ID %q", m.Title, m.ID)
		}
		if len(m.Availability) != 1 || m.Availability[0].Source != SourceJellyfin {
			t.Fatalf("movie %q has unexpected availability %+v", m.Title, m.Availability)
		}
	}

	filtered, filteredCount, err := client.Movies(ctx, Filters{Genres: []string{"Action"}, Limit: jellyfinFetchDepth})
	if err != nil {
		t.Fatalf("Movies(genres=Action): %v", err)
	}
	t.Logf("genres=Action: %d movies fetched, TotalRecordCount=%d", len(filtered), filteredCount)

	if filteredCount >= allCount {
		t.Fatalf("expected genres=Action (%d) to reduce the count vs no filter (%d)", filteredCount, allCount)
	}
}

// TestJellyfinVocabularyOmitsUserID confirms that Vocabulary does NOT send userId
// to /Items/Filters. A real Jellyfin server returns an empty result set for genres
// and official ratings when userId is supplied, which broke the genre picker for
// any deployment with JELLYFIN_USER_ID set. The stub mirrors this behaviour:
// if userId is present, return empty arrays; otherwise return populated ones.
func TestJellyfinVocabularyOmitsUserID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Items/Filters" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		// Real Jellyfin's behaviour: /Items/Filters with userId returns empty.
		if userID := r.URL.Query().Get("userId"); userID != "" {
			_, _ = w.Write([]byte(`{"Genres":[],"OfficialRatings":[]}`))
			return
		}

		// Without userId, the full vocabulary is returned.
		_, _ = w.Write([]byte(`{"Genres":["Comedy","Action","Drama"],"OfficialRatings":["PG","PG-13","R"]}`))
	}))
	defer srv.Close()

	// Create a client with a non-empty userID so we would catch a regression
	// if the code silently sent it anyway.
	c := NewJellyfinClient(Config{JellyfinURL: srv.URL, JellyfinAPIKey: "k", JellyfinUserID: "user-123"}, libraryRef{})
	got, err := c.Vocabulary(context.Background())
	if err != nil {
		t.Fatalf("Vocabulary: %v", err)
	}

	wantGenres := []string{"Comedy", "Action", "Drama"}
	if len(got.Genres) != len(wantGenres) {
		t.Fatalf("got %d genres, want %d", len(got.Genres), len(wantGenres))
	}
	for i, w := range wantGenres {
		if got.Genres[i] != w {
			t.Errorf("genre[%d] = %q, want %q", i, got.Genres[i], w)
		}
	}

	wantRatings := []string{"PG", "PG-13", "R"}
	if len(got.OfficialRatings) != len(wantRatings) {
		t.Fatalf("got %d ratings, want %d", len(got.OfficialRatings), len(wantRatings))
	}
	for i, w := range wantRatings {
		if got.OfficialRatings[i] != w {
			t.Errorf("rating[%d] = %q, want %q", i, got.OfficialRatings[i], w)
		}
	}
}
