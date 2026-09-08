package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// jellyfinStub serves the two endpoints a Jellyfin check reads. movieCount of -1
// makes /Items fail with 401, which is how a revoked API key behaves.
func jellyfinStub(t *testing.T, serverName string, movieCount int, validKey string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/System/Info/Public":
			_, _ = fmt.Fprintf(w, `{"ServerName":%q,"Version":"10.9.0"}`, serverName)
		case "/Items":
			if r.Header.Get("Authorization") != jellyfinAuthHeader(validKey) || r.Header.Get("X-Emby-Token") != "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = fmt.Fprintf(w, `{"Items":[],"TotalRecordCount":%d}`, movieCount)
		case "/Users":
			if r.Header.Get("Authorization") != jellyfinAuthHeader(validKey) || r.Header.Get("X-Emby-Token") != "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			// Deliberately out of order, and with one entry carrying no id.
			_, _ = w.Write([]byte(`[
			  {"Id":"bbb","Name":"Sami"},
			  {"Id":"","Name":"broken"},
			  {"Id":"aaa","Name":"Alex"}
			]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

// plexStub serves /library/sections with the given movie library titles.
func plexStub(t *testing.T, validToken string, titles ...string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != validToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		dirs := make([]string, 0, len(titles)+1)
		for i, title := range titles {
			dirs = append(dirs, fmt.Sprintf(`{"key":"%d","type":"movie","title":%q}`, i+1, title))
		}
		// A non-movie section, so the check has to filter rather than count.
		dirs = append(dirs, `{"key":"99","type":"show","title":"TV"}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"MediaContainer":{"Directory":[%s]}}`, strings.Join(dirs, ","))
	}))
	t.Cleanup(ts.Close)
	return ts
}

// newCheckServer builds a server from a config file body, so a test can put
// credentials in storage and exercise the "no candidate submitted" fallback.
func newCheckServer(t *testing.T, body string) (*Server, string) {
	t.Helper()
	clearConfigEnv(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("CACHE_DIR", t.TempDir())
	cfg, err := resolveConfigAt(path, false)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	s := New(cfg)
	return s, s.setupToken
}

func decodeVerify(t *testing.T, rec *httptest.ResponseRecorder) verifySourceResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got verifySourceResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

// Every check route takes a URL from the request and makes the server fetch it.
// Unauthenticated, that is a port scanner for whatever network this is deployed
// on, with the responses returned to the caller.
func TestCheckRoutesRequireSetupToken(t *testing.T) {
	s, _ := newCheckServer(t, "publicUrl: http://nas:8080\n")

	for _, target := range []string{
		"/api/settings/verify/jellyfin",
		"/api/settings/verify/plex",
		"/api/settings/jellyfin/users",
	} {
		rec := postJSON(t, s, target, "", checkRequest{URL: "http://192.168.1.1", Secret: "x"})
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without a setup token: status = %d, want 401", target, rec.Code)
		}
	}
}

func TestVerifyJellyfinReportsTheMovieCount(t *testing.T) {
	stub := jellyfinStub(t, "Anton", 1284, "good-key")
	s, setup := newCheckServer(t, "publicUrl: http://nas:8080\n")

	got := decodeVerify(t, postJSON(t, s, "/api/settings/verify/jellyfin", setup,
		checkRequest{URL: stub.URL, Secret: "good-key"}))

	if !got.Valid {
		t.Fatalf("valid = false, message = %q", got.Message)
	}
	if !strings.Contains(got.Message, "Anton") || !strings.Contains(got.Message, "1284") {
		t.Errorf("message = %q, want the server name and the movie count", got.Message)
	}
}

// A URL that answers but is not Jellyfin, and a Jellyfin that rejects the key,
// are different mistakes with different fixes. The unauthenticated probe against
// /System/Info/Public is what separates them; a single authenticated call could
// not.
func TestVerifyJellyfinSeparatesABadURLFromABadKey(t *testing.T) {
	stub := jellyfinStub(t, "Anton", 10, "good-key")
	s, setup := newCheckServer(t, "publicUrl: http://nas:8080\n")

	notJellyfin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(notJellyfin.Close)

	badURL := decodeVerify(t, postJSON(t, s, "/api/settings/verify/jellyfin", setup,
		checkRequest{URL: notJellyfin.URL, Secret: "good-key"}))
	if badURL.Valid {
		t.Error("a non-Jellyfin URL verified")
	}
	if !strings.Contains(badURL.Message, "No Jellyfin server") {
		t.Errorf("bad URL message = %q, want it to name the URL as the problem", badURL.Message)
	}

	badKey := decodeVerify(t, postJSON(t, s, "/api/settings/verify/jellyfin", setup,
		checkRequest{URL: stub.URL, Secret: "wrong-key"}))
	if badKey.Valid {
		t.Error("a bad API key verified")
	}
	if !strings.Contains(badKey.Message, "rejected the API key") {
		t.Errorf("bad key message = %q, want it to name the key as the problem", badKey.Message)
	}
}

// An empty library is wired correctly. Saying so is still worth it: the swipe
// screen cannot explain an empty deck, and this is the one place that can.
func TestVerifyJellyfinAcceptsAnEmptyLibraryAndSaysSo(t *testing.T) {
	stub := jellyfinStub(t, "Anton", 0, "good-key")
	s, setup := newCheckServer(t, "publicUrl: http://nas:8080\n")

	got := decodeVerify(t, postJSON(t, s, "/api/settings/verify/jellyfin", setup,
		checkRequest{URL: stub.URL, Secret: "good-key"}))

	if !got.Valid {
		t.Errorf("valid = false, message = %q; an empty library is not a wiring fault", got.Message)
	}
	if !strings.Contains(got.Message, "no movies") {
		t.Errorf("message = %q, want it to mention the empty library", got.Message)
	}
}

// The screen never receives a stored credential, so it cannot submit one. A check
// that only worked against a credential being typed would be useless for the
// case that matters: confirming what is already saved still works.
func TestVerifyJellyfinFallsBackToStoredCredentials(t *testing.T) {
	stub := jellyfinStub(t, "Anton", 7, "stored-key")
	s, setup := newCheckServer(t, fmt.Sprintf(
		"publicUrl: http://nas:8080\njellyfin:\n  url: %s\n  apiKey: stored-key\n", stub.URL))

	got := decodeVerify(t, postJSON(t, s, "/api/settings/verify/jellyfin", setup, checkRequest{}))

	if !got.Valid {
		t.Fatalf("valid = false, message = %q; the stored credentials should have been used", got.Message)
	}
	if !strings.Contains(got.Message, "7") {
		t.Errorf("message = %q, want the stored server's movie count", got.Message)
	}
}

func TestJellyfinUsersAreSortedAndComplete(t *testing.T) {
	stub := jellyfinStub(t, "Anton", 5, "good-key")
	s, setup := newCheckServer(t, "publicUrl: http://nas:8080\n")

	rec := postJSON(t, s, "/api/settings/jellyfin/users", setup,
		checkRequest{URL: stub.URL, Secret: "good-key"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got jellyfinUsersResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	want := []jellyfinUser{{ID: "aaa", Name: "Alex"}, {ID: "bbb", Name: "Sami"}}
	if len(got.Users) != len(want) {
		t.Fatalf("got %+v, want %+v (an entry with no id is not selectable)", got.Users, want)
	}
	for i, w := range want {
		if got.Users[i] != w {
			t.Errorf("user %d = %+v, want %+v (the order must be stable)", i, got.Users[i], w)
		}
	}
}

func TestVerifyPlexNamesTheLibraryItWillUse(t *testing.T) {
	stub := plexStub(t, "good-token", "Films")
	s, setup := newCheckServer(t, "publicUrl: http://nas:8080\n")

	got := decodeVerify(t, postJSON(t, s, "/api/settings/verify/plex", setup,
		checkRequest{URL: stub.URL, Secret: "good-token"}))

	if !got.Valid {
		t.Fatalf("valid = false, message = %q", got.Message)
	}
	if !strings.Contains(got.Message, "Films") {
		t.Errorf("message = %q, want the library name", got.Message)
	}
}

func TestVerifyPlexRejectsABadToken(t *testing.T) {
	stub := plexStub(t, "good-token", "Films")
	s, setup := newCheckServer(t, "publicUrl: http://nas:8080\n")

	got := decodeVerify(t, postJSON(t, s, "/api/settings/verify/plex", setup,
		checkRequest{URL: stub.URL, Secret: "wrong-token"}))

	if got.Valid {
		t.Error("a bad token verified")
	}
	if !strings.Contains(got.Message, "rejected the token") {
		t.Errorf("message = %q, want it to name the token as the problem", got.Message)
	}
}

// Several movie libraries is not a failure — discovery picks the first and the
// app works. It is reported because "works" there means dealing from a library
// nobody chose, and the section keys are what the form needs.
func TestVerifyPlexReportsSeveralMovieLibraries(t *testing.T) {
	stub := plexStub(t, "good-token", "Films", "Kids Films")
	s, setup := newCheckServer(t, "publicUrl: http://nas:8080\n")

	got := decodeVerify(t, postJSON(t, s, "/api/settings/verify/plex", setup,
		checkRequest{URL: stub.URL, Secret: "good-token"}))

	if !got.Valid {
		t.Fatalf("valid = false, message = %q; several libraries still works", got.Message)
	}
	for _, want := range []string{"Films", "Kids Films", "key 1", "key 2"} {
		if !strings.Contains(got.Message, want) {
			t.Errorf("message = %q, want it to mention %q", got.Message, want)
		}
	}
}

// A configured section key that does not exist would otherwise surface as a
// library with no movies in it.
func TestVerifyPlexRejectsAnUnknownLibrarySection(t *testing.T) {
	stub := plexStub(t, "good-token", "Films")
	s, setup := newCheckServer(t, "publicUrl: http://nas:8080\n")

	got := decodeVerify(t, postJSON(t, s, "/api/settings/verify/plex", setup,
		checkRequest{URL: stub.URL, Secret: "good-token", LibrarySection: "42"}))

	if got.Valid {
		t.Error("a nonexistent library section verified")
	}
	if !strings.Contains(got.Message, "42") || !strings.Contains(got.Message, "Films") {
		t.Errorf("message = %q, want the bad key and the available libraries", got.Message)
	}
	// The list travels even on the failure. The section key is opaque, so an
	// error that withholds it leaves nothing to correct the value to.
	if len(got.Libraries) != 1 || got.Libraries[0].ID != "1" {
		t.Errorf("sections = %+v, want the available library on a failing check", got.Libraries)
	}
}

// The section list is what lets the settings screen offer the library as a
// choice. Its value is an opaque number with no way to discover it except by
// asking Plex, so a check that reached the server must return it — the non-movie
// sections filtered out.
func TestVerifyPlexReturnsTheMovieLibraries(t *testing.T) {
	stub := plexStub(t, "good-token", "Films", "Kids Films")
	s, setup := newCheckServer(t, "publicUrl: http://nas:8080\n")

	got := decodeVerify(t, postJSON(t, s, "/api/settings/verify/plex", setup,
		checkRequest{URL: stub.URL, Secret: "good-token"}))

	want := []libraryOption{{ID: "1", Name: "Films"}, {ID: "2", Name: "Kids Films"}}
	if len(got.Libraries) != len(want) {
		t.Fatalf("libraries = %+v, want %+v (the TV section must not be offered)", got.Libraries, want)
	}
	for i, w := range want {
		if got.Libraries[i] != w {
			t.Errorf("library %d = %+v, want %+v", i, got.Libraries[i], w)
		}
	}
}

// Nothing to offer when the token was refused: no list, and no implication that
// one was read.
func TestVerifyPlexReturnsNoSectionsWhenItCouldNotRead(t *testing.T) {
	stub := plexStub(t, "good-token", "Films")
	s, setup := newCheckServer(t, "publicUrl: http://nas:8080\n")

	got := decodeVerify(t, postJSON(t, s, "/api/settings/verify/plex", setup,
		checkRequest{URL: stub.URL, Secret: "wrong-token"}))

	if len(got.Libraries) != 0 {
		t.Errorf("sections = %+v, want none from a rejected check", got.Libraries)
	}
}

// A server with no movie library cannot deal a deck, whatever else it has.
func TestVerifyPlexRejectsAServerWithNoMovieLibrary(t *testing.T) {
	stub := plexStub(t, "good-token")
	s, setup := newCheckServer(t, "publicUrl: http://nas:8080\n")

	got := decodeVerify(t, postJSON(t, s, "/api/settings/verify/plex", setup,
		checkRequest{URL: stub.URL, Secret: "good-token"}))

	if got.Valid {
		t.Errorf("a server with no movie library verified: %q", got.Message)
	}
}

// Nothing to check is its own answer, and it must not cost an upstream request.
func TestVerifyRoutesReportMissingDetailsWithoutCallingOut(t *testing.T) {
	s, setup := newCheckServer(t, "publicUrl: http://nas:8080\n")

	for _, target := range []string{"/api/settings/verify/jellyfin", "/api/settings/verify/plex"} {
		got := decodeVerify(t, postJSON(t, s, target, setup, checkRequest{}))
		if got.Valid {
			t.Errorf("%s verified with nothing configured", target)
		}
		if !strings.Contains(got.Message, "server URL") {
			t.Errorf("%s message = %q, want it to ask for a URL", target, got.Message)
		}
	}
}
