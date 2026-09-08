package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// plexTestServer stands in for a Plex Media Server, serving canned JSON per
// path and recording the query of the last request so filter mapping can be
// asserted.
type plexTestServer struct {
	*httptest.Server
	lastQuery url.Values
	lastPath  string
	token     string
}

func newPlexTestServer(t *testing.T, bodies map[string]string) *plexTestServer {
	t.Helper()
	ts := &plexTestServer{}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts.lastPath = r.URL.Path
		ts.lastQuery = r.URL.Query()
		ts.token = r.Header.Get("X-Plex-Token")
		body, ok := bodies[r.URL.Path]
		if !ok {
			// Plex answers an unauthenticated or unknown request with HTML,
			// not JSON — the case that must never reach the decoder.
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("<html><head><title>Unauthorized</title></head></html>"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return ts
}

const plexTwoMovies = `{"MediaContainer":{"totalSize":42,"Metadata":[
{"ratingKey":"8014","title":"3 Idiots","year":2009,"contentRating":"PG-13",
 "rating":10.0,"audienceRating":9.3,"duration":10267300,"summary":"Two friends search.",
 "thumb":"/library/metadata/8014/thumb/1782791305",
 "Genre":[{"tag":"Comedy"},{"tag":"Drama"}],
 "Guid":[{"id":"imdb://tt1187043"},{"id":"tmdb://20453"},{"id":"tvdb://3849"}]},
{"ratingKey":"9001","title":"Home Video","year":2001,"contentRating":"NR",
 "rating":0,"audienceRating":0,"duration":600000,"thumb":"",
 "Genre":[],"Guid":[]}]}}`

func plexClientFor(ts *plexTestServer, section string) *PlexClient {
	return NewPlexClient(Config{
		PlexURL:   ts.URL,
		PlexToken: "test-token",
	}, libraryRef{ID: section})
}

func TestPlexMoviesMapsItems(t *testing.T) {
	ts := newPlexTestServer(t, map[string]string{
		"/library/sections/2/all": plexTwoMovies,
	})
	c := plexClientFor(ts, "2")

	movies, total, err := c.Movies(context.Background(), Filters{})
	if err != nil {
		t.Fatalf("Movies: %v", err)
	}
	if total != 42 {
		t.Errorf("total = %d, want 42 (Plex reports totalSize uncapped)", total)
	}
	if len(movies) != 2 {
		t.Fatalf("got %d movies, want 2", len(movies))
	}
	if ts.token != "test-token" {
		t.Errorf("X-Plex-Token header = %q, want the configured token", ts.token)
	}

	got := movies[0]
	// The TMDB id must win over the Plex rating key: it is the join key that
	// lets this item merge with the same film from a streaming source.
	if got.ID != "tmdb:20453" {
		t.Errorf("ID = %q, want tmdb:20453", got.ID)
	}
	// AudienceRating, not the 10.0 critic rating: only the audience score is
	// comparable with Jellyfin's CommunityRating.
	if got.CommunityRating != 9.3 {
		t.Errorf("CommunityRating = %v, want 9.3 (audienceRating, not rating)", got.CommunityRating)
	}
	if got.Runtime != 171 {
		t.Errorf("Runtime = %d, want 171 (duration is milliseconds)", got.Runtime)
	}
	if got.OfficialRating != "PG-13" {
		t.Errorf("OfficialRating = %q, want PG-13", got.OfficialRating)
	}
	if len(got.Genres) != 2 || got.Genres[0] != "Comedy" || got.Genres[1] != "Drama" {
		t.Errorf("Genres = %v, want [Comedy Drama]", got.Genres)
	}
	// The poster path names the source that can serve the image, which under one
	// source per library is the library-scoped id, not the bare service id.
	if got.PosterURL != "/api/images/plex-2/8014?tag=1782791305" {
		t.Errorf("PosterURL = %q, want the proxied path with the thumb version as tag", got.PosterURL)
	}
	if len(got.Availability) != 1 || got.Availability[0].Source != "plex-2" {
		t.Errorf("Availability = %v, want one entry for this library's source", got.Availability)
	}
	if got.Availability[0].Label != "Plex" {
		t.Errorf("Availability label = %q, want Plex", got.Availability[0].Label)
	}
}

func TestPlexMoviesFallsBackWhenUnmatched(t *testing.T) {
	ts := newPlexTestServer(t, map[string]string{
		"/library/sections/2/all": plexTwoMovies,
	})
	c := plexClientFor(ts, "2")

	movies, _, err := c.Movies(context.Background(), Filters{})
	if err != nil {
		t.Fatalf("Movies: %v", err)
	}
	// An item with no TMDB guid keeps a Plex-namespaced id and simply never
	// merges, rather than colliding with another source's item.
	if movies[1].ID != "plex:9001" {
		t.Errorf("ID = %q, want plex:9001 for an item with no TMDB guid", movies[1].ID)
	}
	// No thumb means no tag, so the poster URL carries no cache-pinning query.
	if movies[1].PosterURL != "/api/images/plex-2/9001" {
		t.Errorf("PosterURL = %q, want the untagged proxied path", movies[1].PosterURL)
	}
}

func TestPlexMoviesAppliesRatingMinClientSide(t *testing.T) {
	ts := newPlexTestServer(t, map[string]string{
		"/library/sections/2/all": plexTwoMovies,
	})
	c := plexClientFor(ts, "2")

	movies, _, err := c.Movies(context.Background(), Filters{RatingMin: 5})
	if err != nil {
		t.Fatalf("Movies: %v", err)
	}
	if len(movies) != 1 || movies[0].ID != "tmdb:20453" {
		t.Fatalf("got %v, want only the 9.3-rated movie: Plex has no server-side rating filter", movies)
	}
	if ts.lastQuery.Has("rating") {
		t.Error("query carries a rating param; Plex has no equivalent and it must be applied after the fetch")
	}
}

// TestPlexDecodesBothGuidFields pins the shape of a real Plex response, which
// carries two differently-cased guid fields on the same item: "guid", a string
// holding Plex's own identifier, and "Guid", the array of external ids.
//
// encoding/json falls back to a case-insensitive field match, so without an
// exact destination for the string form it is decoded into the []plexGUID
// field and the entire response fails. Nothing in a hand-written fixture
// reproduces that unless the fixture includes both fields, so this one does.
func TestPlexDecodesBothGuidFields(t *testing.T) {
	ts := newPlexTestServer(t, map[string]string{
		"/library/sections/2/all": `{"MediaContainer":{"totalSize":1,"Metadata":[
			{"ratingKey":"8014","title":"3 Idiots","year":2009,
			 "guid":"plex://movie/5d7768255af944001f1f9efe",
			 "Guid":[{"id":"tmdb://20453"}]}]}}`,
	})
	c := plexClientFor(ts, "2")

	movies, _, err := c.Movies(context.Background(), Filters{})
	if err != nil {
		t.Fatalf("Movies: %v", err)
	}
	if len(movies) != 1 {
		t.Fatalf("got %d movies, want 1", len(movies))
	}
	if movies[0].ID != "tmdb:20453" {
		t.Errorf("ID = %q, want tmdb:20453 from the Guid array", movies[0].ID)
	}
}

// TestPlexSearchMakesOneRequest pins the cost of dealing from Plex.
//
// Plex truncates each item's Genre array to two tags in a list response, and
// the only way to recover the rest is a detail request per movie — 151
// requests for a 150-card shoe. That trade was rejected deliberately: genres
// are display data, while filtering happens server-side against the
// untruncated tags. This test fails if someone adds the fan-out later.
func TestPlexSearchMakesOneRequest(t *testing.T) {
	ts := newPlexTestServer(t, map[string]string{
		"/library/sections/2/all": plexTwoMovies,
	})
	requests := 0
	base := ts.Config.Handler
	ts.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		base.ServeHTTP(w, r)
	})
	c := plexClientFor(ts, "2")

	if _, err := c.Search(context.Background(), Filters{}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if requests != 1 {
		t.Errorf("made %d requests, want 1: a per-item detail fetch would make dealing a deck O(deck size)", requests)
	}
}

func TestPlexApplyFilters(t *testing.T) {
	q := url.Values{}
	Filters{
		Genres:          []string{"Comedy", "Drama"},
		YearMin:         2000,
		YearMax:         2002,
		OfficialRatings: []string{"PG", "PG-13"},
		Unwatched:       true,
		Limit:           150,
	}.applyPlex(q)

	want := map[string]string{
		"genre":                 "Comedy,Drama",
		"year":                  "2000,2001,2002",
		"contentRating":         "PG,PG-13",
		"unwatched":             "1",
		"sort":                  "random",
		"X-Plex-Container-Size": "150",
	}
	for k, v := range want {
		if got := q.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

func TestPlexApplyFiltersOmitsEmpties(t *testing.T) {
	q := url.Values{}
	Filters{}.applyPlex(q)
	for _, k := range []string{"genre", "year", "contentRating", "unwatched", "sort"} {
		if q.Has(k) {
			t.Errorf("%s is set for empty filters; an unset filter must not narrow the query", k)
		}
	}
}

func TestPlexSearchSetsGuidAndType(t *testing.T) {
	ts := newPlexTestServer(t, map[string]string{
		"/library/sections/2/all": plexTwoMovies,
	})
	c := plexClientFor(ts, "2")

	if _, err := c.Search(context.Background(), Filters{}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := ts.lastQuery.Get("type"); got != "1" {
		t.Errorf("type = %q, want 1 (movies only)", got)
	}
	// Without includeGuids the TMDB id is absent and nothing can merge.
	if got := ts.lastQuery.Get("includeGuids"); got != "1" {
		t.Errorf("includeGuids = %q, want 1", got)
	}
}

func TestPlexDiscoversMovieSection(t *testing.T) {
	ts := newPlexTestServer(t, map[string]string{
		"/library/sections": `{"MediaContainer":{"Directory":[
			{"key":"1","type":"show","title":"TV Shows"},
			{"key":"2","type":"movie","title":"Movies"}]}}`,
		"/library/sections/2/all": plexTwoMovies,
	})
	c := plexClientFor(ts, "") // unset PLEX_LIBRARY_SECTION

	if _, _, err := c.Movies(context.Background(), Filters{}); err != nil {
		t.Fatalf("Movies: %v", err)
	}
	if ts.lastPath != "/library/sections/2/all" {
		t.Errorf("queried %q, want the movie section, not the show section", ts.lastPath)
	}
}

func TestPlexDiscoveryFailsWithoutMovieSection(t *testing.T) {
	ts := newPlexTestServer(t, map[string]string{
		"/library/sections": `{"MediaContainer":{"Directory":[
			{"key":"1","type":"show","title":"TV Shows"}]}}`,
	})
	c := plexClientFor(ts, "")

	if _, _, err := c.Movies(context.Background(), Filters{}); err == nil {
		t.Fatal("want an error when no section holds movies, got nil")
	}
}

func TestPlexLibraryIDOverridesSection(t *testing.T) {
	ts := newPlexTestServer(t, map[string]string{
		"/library/sections/7/all": plexTwoMovies,
	})
	c := plexClientFor(ts, "2")

	if _, _, err := c.Movies(context.Background(), Filters{LibraryID: "7"}); err != nil {
		t.Fatalf("Movies: %v", err)
	}
	if ts.lastPath != "/library/sections/7/all" {
		t.Errorf("queried %q, want the host's selected section", ts.lastPath)
	}
}

func TestPlexErrorBodyIsNotDecoded(t *testing.T) {
	// The unknown path makes the stub answer 401 with an HTML body, which is
	// what a real Plex server does. The status must be checked before the
	// decoder sees it, or the error reads "invalid character '<'".
	ts := newPlexTestServer(t, map[string]string{})
	c := plexClientFor(ts, "2")

	_, _, err := c.Movies(context.Background(), Filters{})
	if err == nil {
		t.Fatal("want an error on 401, got nil")
	}
	if !containsAll(err.Error(), "plex", "401") {
		t.Errorf("error = %q, want it to name the source and the status", err)
	}
}

func TestPlexVocabulary(t *testing.T) {
	ts := newPlexTestServer(t, map[string]string{
		"/library/sections/2/genre": `{"MediaContainer":{"Directory":[
			{"key":"1","title":"Comedy"},{"key":"2","tag":"Science Fiction"}]}}`,
		"/library/sections/2/contentRating": `{"MediaContainer":{"Directory":[
			{"key":"1","tag":"PG"},{"key":"2","tag":"PG-13"}]}}`,
	})
	c := plexClientFor(ts, "2")

	got, err := c.Vocabulary(context.Background())
	if err != nil {
		t.Fatalf("Vocabulary: %v", err)
	}
	if len(got.Genres) != 2 || got.Genres[0] != "Comedy" || got.Genres[1] != "Science Fiction" {
		t.Errorf("Genres = %v, want [Comedy Science Fiction] (title and tag both name a value)", got.Genres)
	}
	if len(got.OfficialRatings) != 2 || got.OfficialRatings[1] != "PG-13" {
		t.Errorf("OfficialRatings = %v, want [PG PG-13]", got.OfficialRatings)
	}
}

func TestPlexSupportsUnwatchedAlways(t *testing.T) {
	// Unlike Jellyfin, no user id is needed: a Plex token identifies a user,
	// so play state is always answerable.
	c := NewPlexClient(Config{PlexURL: "http://plex", PlexToken: "t"}, libraryRef{})
	if !c.SupportsUnwatched() {
		t.Error("SupportsUnwatched = false, want true whenever Plex is configured")
	}
}

func TestPlexFetchPoster(t *testing.T) {
	ts := newPlexTestServer(t, map[string]string{
		"/library/metadata/8014/thumb/1782791305": "PNGBYTES",
	})
	c := plexClientFor(ts, "2")

	data, err := c.fetchPoster(context.Background(), "8014", "1782791305")
	if err != nil {
		t.Fatalf("fetchPoster: %v", err)
	}
	if string(data) != "PNGBYTES" {
		t.Errorf("got %q, want the upstream bytes", data)
	}
}

func TestPlexConfigured(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"both set", Config{PlexURL: "http://plex", PlexToken: "t"}, true},
		{"url only", Config{PlexURL: "http://plex"}, false},
		{"token only", Config{PlexToken: "t"}, false},
		{"neither", Config{}, false},
	}
	for _, tc := range cases {
		if got := tc.cfg.PlexConfigured(); got != tc.want {
			t.Errorf("%s: PlexConfigured = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestPlexTokenNeverLogged(t *testing.T) {
	cfg := Config{PlexURL: "http://plex", PlexToken: "super-secret-token"}
	if containsAll(cfg.String(), "super-secret-token") {
		t.Error("Config.String leaks PLEX_TOKEN; it must be masked like the other credentials")
	}
}

// containsAll reports whether s contains every substring.
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

// --- section discovery ---

// flakyPlexServer fails the first n requests to /library/sections and serves the
// sections after that, which is what a Plex server that was still starting looks
// like from here.
type flakyPlexServer struct {
	*httptest.Server
	calls     int
	failFirst int
}

func newFlakyPlexServer(t *testing.T, failFirst int) *flakyPlexServer {
	t.Helper()
	ts := &flakyPlexServer{failFirst: failFirst}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/library/sections" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		ts.calls++
		if ts.calls <= ts.failFirst {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(plexSections))
	}))
	t.Cleanup(ts.Close)
	return ts
}

const plexSections = `{"MediaContainer":{"Directory":[
{"key":"1","type":"movie","title":"Films"},
{"key":"2","type":"show","title":"TV"},
{"key":"3","type":"movie","title":"Kids Films"}]}}`

// A configured section is used as-is, with no discovery request at all. That is
// what lets a deployment start deterministically without the server being up.
func TestPlexConfiguredSectionSkipsDiscovery(t *testing.T) {
	ts := newFlakyPlexServer(t, 0)
	c := NewPlexClient(Config{PlexURL: ts.URL, PlexToken: "t"}, libraryRef{ID: "7"})

	got, err := c.movieSection(context.Background())
	if err != nil {
		t.Fatalf("movieSection: %v", err)
	}
	if got != "7" {
		t.Errorf("section = %q, want the configured value", got)
	}
	if ts.calls != 0 {
		t.Errorf("made %d discovery requests, want none", ts.calls)
	}
}

func TestPlexDiscoversTheFirstMovieSectionOnce(t *testing.T) {
	ts := newFlakyPlexServer(t, 0)
	c := NewPlexClient(Config{PlexURL: ts.URL, PlexToken: "t"}, libraryRef{})

	for i := 0; i < 4; i++ {
		got, err := c.movieSection(context.Background())
		if err != nil {
			t.Fatalf("movieSection %d: %v", i, err)
		}
		if got != "1" {
			t.Errorf("section = %q, want the first movie section", got)
		}
	}
	if ts.calls != 1 {
		t.Errorf("made %d discovery requests, want one", ts.calls)
	}
}

// The regression test for the sync.Once bug. Discovery used to cache its failure
// for the life of the process, so one unreachable moment on the first query meant
// Plex never worked again until the container was recreated. It presented days
// later as "Plex worked yesterday", with the explaining log line long gone.
func TestPlexRetriesFailedDiscovery(t *testing.T) {
	ts := newFlakyPlexServer(t, 1)
	c := NewPlexClient(Config{PlexURL: ts.URL, PlexToken: "t"}, libraryRef{})
	clock := newFakeClock()
	c.discovered.now = clock.now
	c.discovered.retryAfter = time.Minute

	if _, err := c.movieSection(context.Background()); err == nil {
		t.Fatal("first discovery succeeded against a failing server")
	}

	clock.advance(2 * time.Minute)

	got, err := c.movieSection(context.Background())
	if err != nil {
		t.Fatalf("discovery was never retried: %v", err)
	}
	if got != "1" {
		t.Errorf("section = %q, want the discovered section", got)
	}
}

// A retry storm against a server that is still down costs one request, not one
// per caller.
func TestPlexDoesNotRetryDiscoveryInsideTheWindow(t *testing.T) {
	ts := newFlakyPlexServer(t, 99)
	c := NewPlexClient(Config{PlexURL: ts.URL, PlexToken: "t"}, libraryRef{})
	clock := newFakeClock()
	c.discovered.now = clock.now
	c.discovered.retryAfter = time.Minute

	for i := 0; i < 5; i++ {
		if _, err := c.movieSection(context.Background()); err == nil {
			t.Fatalf("attempt %d succeeded against a failing server", i)
		}
		clock.advance(5 * time.Second)
	}
	if ts.calls != 1 {
		t.Errorf("made %d requests inside the retry window, want one", ts.calls)
	}
}

// A server with no movie library is a configuration problem, not a transient one,
// but it is still reported rather than guessed around.
func TestPlexReportsNoMovieSection(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"MediaContainer":{"Directory":[{"key":"2","type":"show","title":"TV"}]}}`))
	}))
	t.Cleanup(ts.Close)
	c := NewPlexClient(Config{PlexURL: ts.URL, PlexToken: "t"}, libraryRef{})

	if _, err := c.movieSection(context.Background()); err == nil {
		t.Fatal("a server with no movie library discovered a section")
	}
}

func TestPlexMovieSectionsFiltersNonMovieLibraries(t *testing.T) {
	ts := newFlakyPlexServer(t, 0)
	c := NewPlexClient(Config{PlexURL: ts.URL, PlexToken: "t"}, libraryRef{})

	got, err := c.movieSections(context.Background())
	if err != nil {
		t.Fatalf("movieSections: %v", err)
	}
	if len(got) != 2 || got[0].Key != "1" || got[1].Key != "3" {
		t.Errorf("sections = %+v, want only the two movie libraries", got)
	}
}

// Both Plex call sites build their request one way, so the token travels on
// each of them.
func TestPlexSendsTokenOnSearchAndPoster(t *testing.T) {
	ts := newPlexTestServer(t, map[string]string{
		"/library/sections/3/all": plexTwoMovies,
	})
	c := plexClientFor(ts, "3")

	if _, err := c.Search(context.Background(), Filters{}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if ts.token != "test-token" {
		t.Fatalf("search token = %q, want test-token", ts.token)
	}

	ts.token = ""
	poster := newPlexTestServer(t, map[string]string{
		"/library/metadata/8014/thumb/1782791305": "image-bytes",
	})
	pc := plexClientFor(poster, "3")
	if _, err := pc.fetchPoster(context.Background(), "8014", "1782791305"); err != nil {
		t.Fatalf("fetchPoster: %v", err)
	}
	if poster.token != "test-token" {
		t.Fatalf("poster token = %q, want test-token", poster.token)
	}
}

// A stale token used to classify as unauthorized when listing movies and as an
// unclassified failure when fetching art. Both now go through PlexClient.do.
func TestPlexPosterClassifiesUnauthorized(t *testing.T) {
	// The stub answers an unknown path with 401 and an HTML body, which is what
	// Plex itself does for a rejected token.
	ts := newPlexTestServer(t, nil)
	c := plexClientFor(ts, "3")

	_, err := c.fetchPoster(context.Background(), "8014", "1")
	if err == nil {
		t.Fatalf("expected an error from a 401 poster fetch")
	}
	if got := failureReason(err); got != FailureUnauthorized {
		t.Fatalf("reason = %q, want %q", got, FailureUnauthorized)
	}
}
