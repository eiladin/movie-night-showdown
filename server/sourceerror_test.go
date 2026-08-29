package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A rejected credential and an unreachable host have different fixes, so the
// status code has to survive the trip out of the client and into the report.
func TestUnauthorizedSourceIsReportedAsUnauthorized(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer stub.Close()

	jf := NewJellyfinClient(Config{JellyfinURL: stub.URL, JellyfinAPIKey: "wrong"}, libraryRef{})
	_, failed, err := gatherShoe(context.Background(), []MovieSource{jf}, Filters{})
	if err == nil {
		t.Fatal("a single failing source must report the failure")
	}
	if len(failed) != 1 {
		t.Fatalf("failed = %v, want one problem", failed)
	}
	if failed[0].Reason != FailureUnauthorized {
		t.Errorf("reason = %q, want %q", failed[0].Reason, FailureUnauthorized)
	}
	if failed[0].Source != SourceJellyfin || failed[0].Label != "Jellyfin" {
		t.Errorf("problem = %+v, want the jellyfin source labelled Jellyfin", failed[0])
	}
}

// The vocabulary path classifies the same way; the host screen reads its
// problems from there before any deck exists.
func TestUnauthorizedVocabularyIsReportedAsUnauthorized(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer stub.Close()

	jf := NewJellyfinClient(Config{JellyfinURL: stub.URL, JellyfinAPIKey: "wrong"}, libraryRef{})
	_, failed, _ := gatherVocabulary(context.Background(), []MovieSource{jf})
	if len(failed) != 1 || failed[0].Reason != FailureUnauthorized {
		t.Fatalf("failed = %+v, want one unauthorized problem", failed)
	}
}

// Nothing answering is not a credential problem, and must never be reported as
// one: it would send an operator to rotate a working key.
func TestUnreachableSourceIsReportedAsUnreachable(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closed := stub.URL
	stub.Close()

	jf := NewJellyfinClient(Config{JellyfinURL: closed, JellyfinAPIKey: "k"}, libraryRef{})
	_, failed, _ := gatherShoe(context.Background(), []MovieSource{jf}, Filters{})
	if len(failed) != 1 || failed[0].Reason != FailureUnreachable {
		t.Fatalf("failed = %+v, want one unreachable problem", failed)
	}
}

// A 500 is not a credential problem either. Only 401 and 403 are.
func TestServerErrorIsReportedAsUnreachable(t *testing.T) {
	if got := failureReason(statusFailure(http.StatusInternalServerError, errAllSourcesFailed)); got != FailureUnreachable {
		t.Errorf("reason = %q, want %q", got, FailureUnreachable)
	}
	if got := failureReason(errAllSourcesFailed); got != FailureUnreachable {
		t.Errorf("unclassified reason = %q, want %q", got, FailureUnreachable)
	}
}

// The gap this closes: a library configured by name whose media server rejects
// the credentials registers no source at all — correctly, since an unresolved
// name cannot become a URL-safe SourceID — and used to be reported to nobody.
// The picker was simply empty.
func TestUnresolvedLibraryIsReportedByFilters(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer stub.Close()

	cfg := Config{
		Port: "8080", SessionTTL: "4h", CacheDir: t.TempDir(),
		JellyfinURL:       stub.URL,
		JellyfinAPIKey:    "wrong",
		JellyfinLibraries: []libraryRef{{ID: "Kids Movies"}},
	}

	rec := httptest.NewRecorder()
	New(cfg).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/library/filters", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/library/filters = %d, want 200: the rest of the screen must keep working", rec.Code)
	}

	var got libraryFiltersResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding filters response: %v", err)
	}
	if len(got.Problems) != 1 {
		t.Fatalf("problems = %+v, want the unresolved library reported", got.Problems)
	}
	p := got.Problems[0]
	if p.Reason != FailureUnresolved {
		t.Errorf("reason = %q, want %q", p.Reason, FailureUnresolved)
	}
	if p.Source != "" {
		t.Errorf("source = %q, want empty: an unresolved name has no SourceID", p.Source)
	}
	if p.Label != "Jellyfin — Kids Movies" {
		t.Errorf("label = %q, want the qualified library label", p.Label)
	}
}

// A name the server does not have is the same story with a working credential:
// it registers no source, so it has to be reported or it vanishes.
func TestUnmatchedLibraryNameIsReportedByPreview(t *testing.T) {
	stub := newMediaFolderStub(t, twoMovieFolders, 0)

	cfg := Config{
		Port: "8080", SessionTTL: "4h", CacheDir: t.TempDir(),
		JellyfinURL:       stub.URL,
		JellyfinAPIKey:    "k",
		JellyfinLibraries: []libraryRef{{ID: "Nonexistent"}},
	}

	rec := httptest.NewRecorder()
	New(cfg).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/library/preview", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/library/preview = %d, want 200", rec.Code)
	}

	var got libraryPreviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding preview response: %v", err)
	}
	if len(got.Problems) != 1 || got.Problems[0].Reason != FailureUnresolved {
		t.Fatalf("problems = %+v, want the unmatched library reported as unresolved", got.Problems)
	}
}
