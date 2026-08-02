package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func decodeSetup(t *testing.T, cfg Config) (setupResponse, string) {
	t.Helper()
	cfg.CacheDir = t.TempDir()
	rec := httptest.NewRecorder()
	New(cfg).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/setup", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/setup = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	var got setupResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decoding setup response: %v", err)
	}
	return got, body
}

// A fresh install has no credentials at all, and must say so rather than
// advertising a source that every query would fail against.
func TestSetupReportsUnconfiguredFreshInstall(t *testing.T) {
	got, _ := decodeSetup(t, Config{
		Port: "8080", SessionTTL: "4h",
		StreamingProviders: defaultStreamingProviders,
	})

	if got.Configured || got.Jellyfin || got.Streaming {
		t.Fatalf("got %+v, want everything false", got)
	}
	if len(got.Sources) != 0 {
		t.Fatalf("got sources %v, want none", got.Sources)
	}
}

func TestSetupReportsStreamingOnly(t *testing.T) {
	got, _ := decodeSetup(t, Config{
		Port: "8080", SessionTTL: "4h",
		TMDBReadToken:      "token",
		StreamingProviders: []SourceID{SourceNetflix},
	})

	if !got.Configured || got.Jellyfin || !got.Streaming {
		t.Fatalf("got %+v, want configured+streaming without jellyfin", got)
	}
	if len(got.Sources) != 1 || got.Sources[0] != SourceNetflix {
		t.Fatalf("got sources %v, want [netflix]", got.Sources)
	}
}

func TestSetupReportsJellyfinOnly(t *testing.T) {
	got, _ := decodeSetup(t, Config{
		Port: "8080", SessionTTL: "4h",
		JellyfinURL: "http://jellyfin.example", JellyfinAPIKey: "key",
		StreamingProviders: defaultStreamingProviders,
	})

	if !got.Configured || !got.Jellyfin || got.Streaming {
		t.Fatalf("got %+v, want configured+jellyfin without streaming", got)
	}
	if len(got.Sources) != 1 || got.Sources[0] != SourceJellyfin {
		t.Fatalf("got sources %v, want [jellyfin]", got.Sources)
	}
}

// A token that resolves to no providers cannot query anything, so streaming
// must not be reported as available.
func TestSetupReportsNoStreamingWhenProvidersEmpty(t *testing.T) {
	got, _ := decodeSetup(t, Config{
		Port: "8080", SessionTTL: "4h",
		TMDBReadToken:      "token",
		StreamingProviders: []SourceID{},
	})

	if got.Configured || got.Streaming {
		t.Fatalf("got %+v, want unconfigured", got)
	}
}

// The setup endpoint is read by browsers, so no credential may appear in it.
func TestSetupResponseCarriesNoCredentials(t *testing.T) {
	_, body := decodeSetup(t, Config{
		Port: "8080", SessionTTL: "4h",
		JellyfinURL: "http://jellyfin.example", JellyfinAPIKey: "jellyfin-secret",
		TMDBReadToken:      "tmdb-secret",
		StreamingProviders: defaultStreamingProviders,
	})

	for _, secret := range []string{"jellyfin-secret", "tmdb-secret"} {
		if strings.Contains(body, secret) {
			t.Fatalf("setup response leaked %q: %s", secret, body)
		}
	}
}

// Without a Jellyfin library there is nothing to enumerate, so the picker is
// offered the default vocabulary instead of an empty one.
func TestLibraryFiltersFallsBackToDefaultVocabulary(t *testing.T) {
	cfg := Config{
		Port: "8080", SessionTTL: "4h", CacheDir: t.TempDir(),
		TMDBReadToken:      "token",
		StreamingProviders: []SourceID{SourceNetflix},
	}
	rec := httptest.NewRecorder()
	New(cfg).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/library/filters", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/library/filters = %d, want 200 (must not require Jellyfin)", rec.Code)
	}
	var got libraryFiltersResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding filters response: %v", err)
	}
	want := defaultAvailableFilters()
	if len(got.Genres) != len(want.Genres) || len(got.Genres) == 0 {
		t.Fatalf("got %d genres, want %d", len(got.Genres), len(want.Genres))
	}
	if len(got.OfficialRatings) != len(tmdbCertificationOrder) {
		t.Fatalf("got ratings %v, want %v", got.OfficialRatings, tmdbCertificationOrder)
	}
}

// The default vocabulary must only offer values a streaming query can honor.
func TestDefaultAvailableFiltersMatchTMDBVocabulary(t *testing.T) {
	got := defaultAvailableFilters()

	if len(got.Genres) != len(tmdbGenreNames) {
		t.Fatalf("got %d genres, want %d", len(got.Genres), len(tmdbGenreNames))
	}
	for i := 1; i < len(got.Genres); i++ {
		if got.Genres[i-1] > got.Genres[i] {
			t.Fatalf("genres are not sorted: %v", got.Genres)
		}
	}
	for _, r := range got.OfficialRatings {
		if !tmdbCertifications[r] {
			t.Fatalf("rating %q is not a TMDB certification", r)
		}
	}
}
