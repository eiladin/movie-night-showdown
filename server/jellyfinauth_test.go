package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

// TestJellyfinAuthHeaderForm pins the MediaBrowser scheme: the token travels in
// a quoted Token field, and the key is escaped rather than interpolated raw.
func TestJellyfinAuthHeaderForm(t *testing.T) {
	got := jellyfinAuthHeader(`ab"c`)
	if !strings.HasPrefix(got, "MediaBrowser ") {
		t.Fatalf("auth header = %q, want the MediaBrowser scheme", got)
	}
	if !strings.Contains(got, `Token="ab\"c"`) {
		t.Fatalf("auth header = %q, want the key quoted and escaped in Token", got)
	}
	for _, field := range []string{`Client="Movie Night Showdown"`, `Device="server"`, `DeviceId="movie-night-showdown"`, `Version="`} {
		if !strings.Contains(got, field) {
			t.Errorf("auth header = %q, want it to contain %s", got, field)
		}
	}
}

// jellyfinAuthRecorder answers every Jellyfin path this application reads and
// records the headers each request arrived with.
func jellyfinAuthRecorder(t *testing.T, key string) (*httptest.Server, func() map[string]http.Header) {
	t.Helper()
	seen := map[string]http.Header{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.URL.Path] = r.Header.Clone()
		if r.Header.Get("Authorization") != jellyfinAuthHeader(key) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/Images/Primary"):
			_, _ = w.Write([]byte("poster-bytes"))
		case r.URL.Path == "/Items/Filters":
			_, _ = w.Write([]byte(`{"Genres":["Comedy"],"OfficialRatings":["PG"]}`))
		case r.URL.Path == "/Library/MediaFolders":
			_, _ = w.Write([]byte(`{"Items":[{"Id":"1","Name":"Movies","CollectionType":"movies"}]}`))
		default:
			_, _ = w.Write([]byte(`{"Items":[],"TotalRecordCount":0}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() map[string]http.Header { return seen }
}

// TestJellyfinRequestsUseMediaBrowserAuth checks every Jellyfin call site sends
// the standard Authorization header and none sends the deprecated legacy one,
// which a server with legacy authorization disabled answers with 401.
func TestJellyfinRequestsUseMediaBrowserAuth(t *testing.T) {
	srv, headers := jellyfinAuthRecorder(t, "k")
	c := NewJellyfinClient(Config{JellyfinURL: srv.URL, JellyfinAPIKey: "k"}, libraryRef{})
	ctx := context.Background()

	if _, _, err := c.Movies(ctx, Filters{}); err != nil {
		t.Fatalf("Movies: %v", err)
	}
	if _, err := c.Vocabulary(ctx); err != nil {
		t.Fatalf("Vocabulary: %v", err)
	}
	if _, err := c.Libraries(ctx); err != nil {
		t.Fatalf("Libraries: %v", err)
	}
	body, err := c.fetchPoster(ctx, "abc", "tag1")
	if err != nil {
		t.Fatalf("fetchPoster: %v", err)
	}
	if string(body) != "poster-bytes" {
		t.Fatalf("poster body = %q", body)
	}

	seen := headers()
	paths := make([]string, 0, len(seen))
	for p := range seen {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	want := []string{"/Items", "/Items/Filters", "/Items/abc/Images/Primary", "/Library/MediaFolders"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	for _, p := range paths {
		if got := seen[p].Get("Authorization"); got != jellyfinAuthHeader("k") {
			t.Errorf("%s Authorization = %q, want the MediaBrowser header", p, got)
		}
		if got := seen[p].Get("X-Emby-Token"); got != "" {
			t.Errorf("%s sent the deprecated X-Emby-Token header (%q)", p, got)
		}
	}
}

// TestJellyfinVerifyPathsUseMediaBrowserAuth covers the two settings-screen
// routes, which build their requests separately from JellyfinClient.
func TestJellyfinVerifyPathsUseMediaBrowserAuth(t *testing.T) {
	seen := map[string]http.Header{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.URL.Path] = r.Header.Clone()
		switch r.URL.Path {
		case "/System/Info/Public":
			_, _ = w.Write([]byte(`{"ServerName":"Home","Version":"10.11.0"}`))
		case "/Users":
			_, _ = w.Write([]byte(`[{"Id":"aaa","Name":"Alex"}]`))
		default:
			_, _ = w.Write([]byte(`{"Items":[],"TotalRecordCount":1}`))
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	var items jellyfinItemsResponse
	if err := getJSON(ctx, srv.URL, "/Items", nil, "Authorization", jellyfinAuthHeader("k"), &items); err != nil {
		t.Fatalf("items: %v", err)
	}
	var users []struct{ ID string }
	if err := getJSON(ctx, srv.URL, "/Users", nil, "Authorization", jellyfinAuthHeader("k"), &users); err != nil {
		t.Fatalf("users: %v", err)
	}
	for _, p := range []string{"/Items", "/Users"} {
		if got := seen[p].Get("Authorization"); got != jellyfinAuthHeader("k") {
			t.Errorf("%s Authorization = %q", p, got)
		}
		if got := seen[p].Get("X-Emby-Token"); got != "" {
			t.Errorf("%s sent X-Emby-Token (%q)", p, got)
		}
	}
}
