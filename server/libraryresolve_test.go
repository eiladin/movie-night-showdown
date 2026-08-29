package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mediaFolderStub serves Jellyfin's /Library/MediaFolders and counts how many
// times it was asked, so resolution timing can be asserted. It refuses the first
// failFirst requests, which is what a server still starting looks like.
type mediaFolderStub struct {
	*httptest.Server
	folderCalls atomic.Int32
	failFirst   atomic.Int32
	body        string
}

const twoMovieFolders = `{"Items":[
  {"Id":"f0e1d2c3b4a5968778695a4b3c2d1e0f","Name":"Movies","CollectionType":"movies"},
  {"Id":"aabbccdd11223344556677889900aabb","Name":"Kids Movies","CollectionType":"movies"},
  {"Id":"11112222333344445555666677778888","Name":"Shows","CollectionType":"tvshows"}
]}`

func newMediaFolderStub(t *testing.T, body string, failFirst int) *mediaFolderStub {
	t.Helper()
	ts := &mediaFolderStub{body: body}
	ts.failFirst.Store(int32(failFirst))
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Library/MediaFolders":
			ts.folderCalls.Add(1)
			if ts.failFirst.Load() > 0 {
				ts.failFirst.Add(-1)
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(ts.body))
		case "/Items":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"TotalRecordCount":1,"Items":[{"Id":"x","Name":"Film"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

// --- the discriminator ---

// The regression guard for the defect this phase exists to fix. A name left
// unresolved became part of a SourceID — `jellyfin-Kids Movies`, spaces included —
// which breaks the URL-safety a SourceID has to have, since it is a path segment in
// the image proxy.
func TestNamedLibraryNeverBecomesASourceID(t *testing.T) {
	cfg := Config{
		JellyfinURL:       "http://nas:8096",
		JellyfinAPIKey:    "k",
		JellyfinLibraries: []libraryRef{{ID: "Kids Movies"}},
	}

	set := buildSourceSet(cfg)

	for _, id := range set.order {
		if strings.ContainsAny(string(id), " \t/?#") {
			t.Errorf("source id %q is not URL-safe", id)
		}
	}
	if len(set.sources) != 0 {
		t.Errorf("sources = %v, want none until the name resolves", set.order)
	}
	if len(set.pending) != 1 || set.pending[0].name != "Kids Movies" {
		t.Errorf("pending = %+v, want the unresolved name", set.pending)
	}
}

func TestIsPendingName(t *testing.T) {
	cases := []struct {
		service SourceID
		ref     libraryRef
		want    bool
	}{
		{SourceJellyfin, libraryRef{}, false},                             // unscoped
		{SourceJellyfin, libraryRef{ID: jfLibA}, false},                   // 32 hex chars
		{SourceJellyfin, libraryRef{ID: "Movies"}, true},                  // a name
		{SourceJellyfin, libraryRef{ID: "Movies", Name: "Movies"}, false}, // from the file
		{SourceJellyfin, libraryRef{ID: strings.Repeat("z", 32)}, true},   // right length, not hex
		{SourcePlex, libraryRef{ID: "3"}, false},                          // an integer key
		{SourcePlex, libraryRef{ID: "Films"}, true},                       // a name
	}
	for _, tc := range cases {
		if got := isPendingName(tc.service, tc.ref); got != tc.want {
			t.Errorf("isPendingName(%s, %+v) = %v, want %v", tc.service, tc.ref, got, tc.want)
		}
	}
}

// --- enumeration ---

// A Jellyfin server also has music, show and mixed folders. Offering one as a
// movie source would produce a source whose every query comes back empty.
func TestJellyfinLibrariesReturnsOnlyMovieFolders(t *testing.T) {
	stub := newMediaFolderStub(t, twoMovieFolders, 0)
	c := NewJellyfinClient(Config{JellyfinURL: stub.URL, JellyfinAPIKey: "k"}, libraryRef{})

	got, err := c.Libraries(context.Background())
	if err != nil {
		t.Fatalf("Libraries: %v", err)
	}
	want := []libraryRef{{ID: jfLibA, Name: "Movies"}, {ID: jfLibB, Name: "Kids Movies"}}
	if len(got) != len(want) {
		t.Fatalf("libraries = %+v, want %+v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("library[%d] = %+v, want %+v", i, got[i], w)
		}
	}
}

// --- resolution timing ---

func resolveServer(t *testing.T, cfg Config) *Server {
	t.Helper()
	clearConfigEnv(t)
	t.Setenv("CACHE_DIR", t.TempDir())
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("publicUrl: http://nas:8080\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg.ConfigPath = path
	cfg.SessionTTL = "4h"
	cfg.PublicURL = "http://nas:8080"
	return New(cfg)
}

// Nothing is enumerated at startup. An application and a media server that start
// together can race, and losing it must not cost the deployment its sources.
func TestNamedLibrariesAreNotResolvedAtStartup(t *testing.T) {
	stub := newMediaFolderStub(t, twoMovieFolders, 0)
	s := resolveServer(t, Config{
		JellyfinURL:       stub.URL,
		JellyfinAPIKey:    "k",
		JellyfinLibraries: []libraryRef{{ID: "Movies"}},
	})

	if n := stub.folderCalls.Load(); n != 0 {
		t.Fatalf("enumerated %d times at startup, want none", n)
	}

	// The first request that needs the source list triggers it.
	set := s.currentSources()

	if n := stub.folderCalls.Load(); n != 1 {
		t.Errorf("enumerated %d times on first use, want once", n)
	}
	if _, ok := set.sources[SourceID("jellyfin-"+jfLibA)]; !ok {
		t.Errorf("sources = %v, want the resolved library", set.order)
	}
	if len(set.pending) != 0 {
		t.Errorf("pending = %+v, want nothing left", set.pending)
	}
}

// A library configured by identifier costs no enumeration at all. That is what
// makes a deterministic cold start available to anyone who wants one.
func TestIdentifiedLibrariesNeedNoEnumeration(t *testing.T) {
	stub := newMediaFolderStub(t, twoMovieFolders, 0)
	s := resolveServer(t, Config{
		JellyfinURL:       stub.URL,
		JellyfinAPIKey:    "k",
		JellyfinLibraries: []libraryRef{{ID: jfLibA}},
	})

	for i := 0; i < 3; i++ {
		set := s.currentSources()
		if _, ok := set.sources[SourceID("jellyfin-"+jfLibA)]; !ok {
			t.Fatalf("sources = %v, want the configured library", set.order)
		}
	}
	if n := stub.folderCalls.Load(); n != 0 {
		t.Errorf("enumerated %d times, want none for an identified library", n)
	}
}

// An unreachable server costs the named libraries and nothing else: the ones
// configured by identifier are already registered and keep working.
func TestUnreachableServerStillRegistersIdentifiedLibraries(t *testing.T) {
	stub := newMediaFolderStub(t, twoMovieFolders, 99)
	s := resolveServer(t, Config{
		JellyfinURL:       stub.URL,
		JellyfinAPIKey:    "k",
		JellyfinLibraries: []libraryRef{{ID: jfLibA}, {ID: "Kids Movies"}},
	})

	set := s.currentSources()

	if _, ok := set.sources[SourceID("jellyfin-"+jfLibA)]; !ok {
		t.Errorf("sources = %v, want the identified library despite the failure", set.order)
	}
	if len(set.pending) != 1 {
		t.Errorf("pending = %+v, want the named library still waiting", set.pending)
	}
}

// A failed resolution is retried once the floor passes, and then succeeds. Without
// this the process would have to be restarted to pick up a server that came back.
func TestFailedResolutionIsRetried(t *testing.T) {
	stub := newMediaFolderStub(t, twoMovieFolders, 1)
	s := resolveServer(t, Config{
		JellyfinURL:       stub.URL,
		JellyfinAPIKey:    "k",
		JellyfinLibraries: []libraryRef{{ID: "Movies"}},
	})
	clock := newFakeClock()
	set := s.sources.Load()
	set.resolve.now = clock.now
	set.resolve.retryAfter = time.Minute

	if got := s.currentSources(); len(got.sources) != 0 {
		t.Fatalf("first attempt resolved against a failing server: %v", got.order)
	}

	clock.advance(2 * time.Minute)

	after := s.currentSources()
	if _, ok := after.sources[SourceID("jellyfin-"+jfLibA)]; !ok {
		t.Errorf("sources = %v, want the library after a retry", after.order)
	}
}

// Several devices opening the app against a server that is still down cost one
// request, not one each.
func TestResolutionDoesNotRetryInsideTheWindow(t *testing.T) {
	stub := newMediaFolderStub(t, twoMovieFolders, 99)
	s := resolveServer(t, Config{
		JellyfinURL:       stub.URL,
		JellyfinAPIKey:    "k",
		JellyfinLibraries: []libraryRef{{ID: "Movies"}},
	})
	clock := newFakeClock()
	set := s.sources.Load()
	set.resolve.now = clock.now
	set.resolve.retryAfter = time.Minute

	for i := 0; i < 5; i++ {
		s.currentSources()
		clock.advance(5 * time.Second)
	}
	if n := stub.folderCalls.Load(); n != 1 {
		t.Errorf("enumerated %d times inside the retry window, want once", n)
	}
}

// A success is never recomputed. Callers build source identifiers out of the
// result and live sessions already point at them.
func TestSuccessfulResolutionIsNotRepeated(t *testing.T) {
	stub := newMediaFolderStub(t, twoMovieFolders, 0)
	s := resolveServer(t, Config{
		JellyfinURL:       stub.URL,
		JellyfinAPIKey:    "k",
		JellyfinLibraries: []libraryRef{{ID: "Movies"}},
	})

	for i := 0; i < 6; i++ {
		s.currentSources()
	}
	if n := stub.folderCalls.Load(); n != 1 {
		t.Errorf("enumerated %d times, want once", n)
	}
}

// Two libraries with the same title resolve to the same one every run. The
// upstream order is not guaranteed, so without a deterministic rule the same
// configuration would pick a different library between restarts — and the source
// id would change under saved host selections.
func TestDuplicateLibraryNameResolvesDeterministically(t *testing.T) {
	const sameName = `{"Items":[
	  {"Id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","Name":"Movies","CollectionType":"movies"},
	  {"Id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","Name":"Movies","CollectionType":"movies"}
	]}`
	for i := 0; i < 3; i++ {
		stub := newMediaFolderStub(t, sameName, 0)
		got, _, err := resolveLibraryNames(context.Background(),
			Config{JellyfinURL: stub.URL, JellyfinAPIKey: "k"},
			SourceJellyfin, []libraryRef{{ID: "Movies"}})
		if err != nil {
			t.Fatalf("resolveLibraryNames: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("resolved to %+v, want one library", got)
		}
		if got[0].ID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
			t.Errorf("run %d resolved to %q, want the deterministic winner", i, got[0].ID)
		}
	}
}

// A name that matches nothing is a configuration error, not a transient one. It
// registers no source rather than failing the whole resolution, so a typo in one
// library name does not cost the ones spelled correctly — and it is returned as
// unmatched so the host can be told, instead of seeing a short picker and
// guessing.
func TestUnknownLibraryNameIsReportedNotFatal(t *testing.T) {
	stub := newMediaFolderStub(t, twoMovieFolders, 0)
	got, unmatched, err := resolveLibraryNames(context.Background(),
		Config{JellyfinURL: stub.URL, JellyfinAPIKey: "k"},
		SourceJellyfin, []libraryRef{{ID: "Movies"}, {ID: "Nonexistent"}})
	if err != nil {
		t.Fatalf("resolveLibraryNames: %v", err)
	}
	if len(got) != 1 || got[0].ID != jfLibA {
		t.Errorf("resolved to %+v, want only the library that exists", got)
	}
	if !slices.Equal(unmatched, []string{"Nonexistent"}) {
		t.Errorf("unmatched = %v, want [Nonexistent]", unmatched)
	}
}

// Names are matched case-insensitively, which is the one place folding case is
// correct. The stored value is never folded, because the same list holds opaque
// identifiers.
func TestLibraryNameMatchingFoldsCase(t *testing.T) {
	stub := newMediaFolderStub(t, twoMovieFolders, 0)
	got, _, err := resolveLibraryNames(context.Background(),
		Config{JellyfinURL: stub.URL, JellyfinAPIKey: "k"},
		SourceJellyfin, []libraryRef{{ID: "  kids MOVIES "}})
	if err != nil {
		t.Fatalf("resolveLibraryNames: %v", err)
	}
	if len(got) != 1 || got[0].ID != jfLibB {
		t.Errorf("resolved to %+v, want the Kids Movies library", got)
	}
}

// The distinction this whole design turns on. applyConfig ends sessions because the
// operator changed the configuration; a resolution is the same configuration
// finally becoming resolvable, and ending a movie night for that would be a bug
// wearing consistency as a disguise.
func TestResolutionDoesNotEndSessions(t *testing.T) {
	stub := newMediaFolderStub(t, twoMovieFolders, 0)
	s := resolveServer(t, Config{
		JellyfinURL:       stub.URL,
		JellyfinAPIKey:    "k",
		JellyfinLibraries: []libraryRef{{ID: "Movies"}},
	})

	sess := s.store.Create("Alex")

	s.currentSources()

	got, ok := s.store.Get(sess.Code)
	if !ok {
		t.Fatal("the session was removed by a library resolution")
	}
	if got.Status == StatusEnded {
		t.Error("the session was ended by a library resolution")
	}
}

// Concurrent first requests share one enumeration. Run under -race.
func TestConcurrentResolutionEnumeratesOnce(t *testing.T) {
	stub := newMediaFolderStub(t, twoMovieFolders, 0)
	s := resolveServer(t, Config{
		JellyfinURL:       stub.URL,
		JellyfinAPIKey:    "k",
		JellyfinLibraries: []libraryRef{{ID: "Movies"}},
	})

	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.currentSources()
		}()
	}
	wg.Wait()

	if n := stub.folderCalls.Load(); n != 1 {
		t.Errorf("enumerated %d times concurrently, want once", n)
	}
}
