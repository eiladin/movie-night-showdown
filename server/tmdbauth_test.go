package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Every authenticated TMDB call goes through tmdbAPI, so the bearer header has
// one construction. These pin that both API call sites still send it.
func TestTMDBDiscoverSendsBearerToken(t *testing.T) {
	var got http.Header
	s, _ := newTestTMDBSource(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"page":1,"total_pages":1,"results":[]}`))
	})

	if _, err := s.Search(context.Background(), Filters{}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if want := "Bearer test-token"; got.Get("Authorization") != want {
		t.Fatalf("Authorization = %q, want %q", got.Get("Authorization"), want)
	}
	if got.Get("Accept") != "application/json" {
		t.Fatalf("Accept = %q, want application/json", got.Get("Accept"))
	}
}

func TestTMDBProviderListSendsBearerToken(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	t.Cleanup(srv.Close)

	r := newProviderResolver(resolveConfig(srv.URL))
	if _, err := r.fetch(context.Background()); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if want := "Bearer token"; got.Get("Authorization") != want {
		t.Fatalf("Authorization = %q, want %q", got.Get("Authorization"), want)
	}
	if got.Get("Accept") != "application/json" {
		t.Fatalf("Accept = %q, want application/json", got.Get("Accept"))
	}
}

// The provider list once returned a bare error, which made a rejected token and
// an unreachable TMDB indistinguishable on the settings screen. It is the whole
// reason listProviders reports a cause.
func TestTMDBProviderListClassifiesUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	r := newProviderResolver(resolveConfig(srv.URL))
	_, err := r.fetch(context.Background())
	if err == nil {
		t.Fatalf("expected an error from a 401 provider list")
	}
	if got := failureReason(err); got != FailureUnauthorized {
		t.Fatalf("reason = %q, want %q", got, FailureUnauthorized)
	}
	// The token must never reach a log line.
	if strings.Contains(err.Error(), "token") {
		t.Fatalf("error message %q leaks the credential", err.Error())
	}
}

func TestTMDBProviderListClassifiesUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	r := newProviderResolver(resolveConfig(srv.URL))
	_, err := r.fetch(context.Background())
	if err == nil {
		t.Fatalf("expected an error from a 500 provider list")
	}
	if got := failureReason(err); got != FailureUnreachable {
		t.Fatalf("reason = %q, want %q", got, FailureUnreachable)
	}
}

// The poster CDN is a different host and is unauthenticated. This pins the
// boundary: no refactor may quietly send the v4 Read Token to image.tmdb.org.
func TestTMDBPosterFetchSendsNoCredential(t *testing.T) {
	var got http.Header
	s, _ := newTestTMDBSource(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write([]byte("bytes"))
	})

	if _, err := s.fetchPoster(context.Background(), "abc.jpg", ""); err != nil {
		t.Fatalf("fetchPoster: %v", err)
	}
	if v := got.Get("Authorization"); v != "" {
		t.Fatalf("poster request carried Authorization %q, want none", v)
	}
}
