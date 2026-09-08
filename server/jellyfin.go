package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Movie is the shape of a Jellyfin movie exposed to clients.
type Movie struct {
	ID              string         `json:"id"`
	Title           string         `json:"title"`
	Year            int            `json:"year"`
	Genres          []string       `json:"genres"`
	Overview        string         `json:"overview"`
	Runtime         int            `json:"runtime"` // minutes
	CommunityRating float64        `json:"communityRating"`
	OfficialRating  string         `json:"officialRating"`
	PosterURL       string         `json:"posterURL"` // always proxied, never the raw Jellyfin URL
	Availability    []Availability `json:"availability"`
}

// JellyfinClient talks to a Jellyfin server's REST API.
//
// One client is one library. A deployment that names several libraries registers
// several clients against the same server, each its own movie source, so a host
// can deal from one of them alone.
type JellyfinClient struct {
	baseURL string
	apiKey  string
	userID  string
	// library is the library this client is scoped to. An empty ref means every
	// library on the server, which is what a deployment that has chosen none has
	// always had.
	library libraryRef
	id      SourceID
	name    string
	http    *http.Client
}

// ID identifies this source. JellyfinClient implements MovieSource.
func (c *JellyfinClient) ID() SourceID { return c.id }

// Name implements NamedSource, returning the qualified library name.
func (c *JellyfinClient) Name() string { return c.name }

// FetchDepth implements DepthedSource. A local library is cheap to page
// through, so it contributes more candidates than a remote catalog.
func (c *JellyfinClient) FetchDepth() int { return jellyfinFetchDepth }

// Search implements MovieSource by delegating to Movies and discarding the
// total count, which only the library preview endpoint needs.
func (c *JellyfinClient) Search(ctx context.Context, f Filters) ([]Movie, error) {
	movies, _, err := c.Movies(ctx, f)
	return movies, err
}

// NewJellyfinClient builds a client for one library. A zero libraryRef queries
// every library, which is the behaviour of a deployment that has chosen none.
func NewJellyfinClient(cfg Config, library libraryRef) *JellyfinClient {
	return &JellyfinClient{
		baseURL: strings.TrimRight(cfg.JellyfinURL, "/"),
		apiKey:  cfg.JellyfinAPIKey,
		userID:  cfg.JellyfinUserID,
		library: library,
		id:      libraryScopedID(SourceJellyfin, library),
		name:    libraryScopedName("Jellyfin", library),
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

// The identity this application presents to Jellyfin. Jellyfin's MediaBrowser
// scheme carries a client identity beside the token; the values are cosmetic to
// the server but appear in its device list, so they are fixed rather than
// derived.
const (
	jellyfinClientName    = "Movie Night Showdown"
	jellyfinDeviceName    = "server"
	jellyfinDeviceID      = "movie-night-showdown"
	jellyfinClientVersion = "1"
)

// jellyfinAuthHeader builds the value of the Authorization header Jellyfin
// expects.
//
// This is the only place the scheme is written. The legacy X-Emby-Token header
// it replaced is deprecated, can be switched off from Jellyfin 10.11 onwards,
// and is slated for removal; a server with it disabled answers it with 401.
// Every Jellyfin version that accepted the legacy header also accepts this one,
// so there is no version to branch on and no fallback to keep.
//
// %q is load-bearing: an API key is operator-supplied and must not be able to
// break out of its quoted field.
func jellyfinAuthHeader(apiKey string) string {
	return fmt.Sprintf("MediaBrowser Token=%q, Client=%q, Device=%q, DeviceId=%q, Version=%q",
		apiKey, jellyfinClientName, jellyfinDeviceName, jellyfinDeviceID, jellyfinClientVersion)
}

// newRequest builds an authenticated GET against this client's server.
//
// It is the one place that knows how this application talks to Jellyfin: base
// URL joining, the auth header, and Accept. path is an absolute path such as
// "/Items"; q may be nil.
func (c *JellyfinClient) newRequest(ctx context.Context, path string, q url.Values) (*http.Request, error) {
	target := c.baseURL + path
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", jellyfinAuthHeader(c.apiKey))
	return req, nil
}

// do performs an authenticated GET and returns the response on 200 only. The
// caller owns the body.
//
// A non-200 is wrapped by statusFailure so sourceerror.go can classify 401 and
// 403 as a credential problem rather than an unreachable host.
func (c *JellyfinClient) do(ctx context.Context, path string, q url.Values) (*http.Response, error) {
	req, err := c.newRequest(ctx, path, q)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jellyfin: GET %s: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, statusFailure(resp.StatusCode, fmt.Errorf("jellyfin: GET %s returned %s", path, resp.Status))
	}
	return resp, nil
}

// getJSON performs an authenticated GET and decodes the body into out.
func (c *JellyfinClient) getJSON(ctx context.Context, path string, q url.Values, out any) error {
	resp, err := c.do(ctx, path, q)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("jellyfin: decode %s response: %w", path, err)
	}
	return nil
}

// jellyfinItemsResponse is the shape of GET /Items.
type jellyfinItemsResponse struct {
	Items            []jellyfinItem `json:"Items"`
	TotalRecordCount int            `json:"TotalRecordCount"`
}

type jellyfinItem struct {
	ID              string            `json:"Id"`
	Name            string            `json:"Name"`
	ProductionYear  int               `json:"ProductionYear"`
	Genres          []string          `json:"Genres"`
	Overview        string            `json:"Overview"`
	RunTimeTicks    int64             `json:"RunTimeTicks"`
	CommunityRating float64           `json:"CommunityRating"`
	OfficialRating  string            `json:"OfficialRating"`
	ImageTags       map[string]string `json:"ImageTags"`
	ProviderIds     struct {
		Tmdb string `json:"Tmdb"`
	} `json:"ProviderIds"`
}

// Movies fetches movies from Jellyfin, applying filters, and maps them onto
// the Movie type used by the rest of the app.
//
// It returns two counts on purpose: the movie list (capped server-side via
// Jellyfin's Limit param at filters.Limit) for display, and the true
// total number of items Jellyfin reports as matching the filters
// (TotalRecordCount, which Jellyfin reports uncapped regardless of Limit)
// for an accurate preview count.
func (c *JellyfinClient) Movies(ctx context.Context, filters Filters) ([]Movie, int, error) {
	q := url.Values{}
	q.Set("IncludeItemTypes", "Movie")
	q.Set("Recursive", "true")
	// ProviderIds carries the TMDB id, which is the join key used to merge a
	// library item with the same film returned by a streaming source.
	q.Set("Fields", "Genres,Overview,ProductionYear,OfficialRating,CommunityRating,RunTimeTicks,ProviderIds")
	if c.userID != "" {
		q.Set("userId", c.userID)
	}
	// The source's own library wins over anything the request asked for. Under one
	// source per library the scope *is* the source's identity, so honouring a
	// client-supplied libraryId here would let a caller make one source answer for
	// another. Setting it before apply keeps Filters.apply the only writer of
	// ParentId.
	filters.LibraryID = c.library.ID
	filters.apply(q, c.userID != "")

	var parsed jellyfinItemsResponse
	if err := c.getJSON(ctx, "/Items", q, &parsed); err != nil {
		return nil, 0, err
	}

	movies := make([]Movie, 0, len(parsed.Items))
	for _, it := range parsed.Items {
		movies = append(movies, it.toMovie(c.id))
	}

	return movies, parsed.TotalRecordCount, nil
}

// toMovie maps one Jellyfin item onto the shared Movie type.
//
// source is the id of the client that fetched it, which under one source per
// library is not the bare service id. The poster path has to name the source that
// can actually serve the image: the proxy looks a fetcher up by that id.
func (it jellyfinItem) toMovie(source SourceID) Movie {
	posterURL := "/api/images/" + string(source) + "/" + it.ID
	if tag := it.ImageTags["Primary"]; tag != "" {
		posterURL += "?tag=" + url.QueryEscape(tag)
	}
	// Prefer the TMDB id so a library item and the same film from a
	// streaming source collapse into one deck entry. Items without one
	// (direct rips, home video) fall back to a Jellyfin-namespaced id and
	// simply never merge.
	id := "jf:" + it.ID
	if it.ProviderIds.Tmdb != "" {
		id = "tmdb:" + it.ProviderIds.Tmdb
	}
	return Movie{
		ID:              id,
		Title:           it.Name,
		Year:            it.ProductionYear,
		Genres:          it.Genres,
		Overview:        it.Overview,
		Runtime:         int(it.RunTimeTicks / 10_000_000 / 60),
		CommunityRating: it.CommunityRating,
		OfficialRating:  it.OfficialRating,
		PosterURL:       posterURL,
		// The badge label is the bare service name, not the qualified library
		// name: a card badge has no room for "Jellyfin — Kids Movies", and the
		// source list carries the qualified form separately.
		Availability: []Availability{{Source: source, Label: "Jellyfin"}},
	}
}

// AvailableFilters represents the possible values for filtering the library.
type AvailableFilters struct {
	Genres          []string `json:"genres"`
	OfficialRatings []string `json:"officialRatings"`
}

// SupportsUnwatched reports whether this deployment can filter on play state.
// It needs a user id: "unwatched" is a question about a particular person's
// history, and without JELLYFIN_USER_ID there is nobody to ask about, so the
// filter would silently do nothing.
func (c *JellyfinClient) SupportsUnwatched() bool { return c.userID != "" }

// Vocabulary queries Jellyfin for all unique genres and official ratings
// present in the Movie library, so the picker offers exactly what is on the
// shelf.
func (c *JellyfinClient) Vocabulary(ctx context.Context) (AvailableFilters, error) {
	q := url.Values{}
	q.Set("IncludeItemTypes", "Movie")
	if c.userID != "" {
		q.Set("userId", c.userID)
	}
	// Scope the vocabulary to this source's library. Unscoped, a host filtering a
	// children's library is offered genres that only exist elsewhere on the server
	// — filters that match nothing the source can return.
	if c.library.ID != "" {
		q.Set("parentId", c.library.ID)
	}

	var parsed struct {
		Genres          []string `json:"Genres"`
		OfficialRatings []string `json:"OfficialRatings"`
	}
	if err := c.getJSON(ctx, "/Items/Filters", q, &parsed); err != nil {
		return AvailableFilters{}, err
	}

	return AvailableFilters{
		Genres:          parsed.Genres,
		OfficialRatings: parsed.OfficialRatings,
	}, nil
}

// fetchPoster downloads a movie's Primary poster from Jellyfin. A non-empty
// tag pins the exact image version so the cache key and the bytes agree.
func (c *JellyfinClient) fetchPoster(ctx context.Context, id, tag string) ([]byte, error) {
	q := url.Values{}
	q.Set("maxWidth", "600")
	if tag != "" {
		q.Set("tag", tag)
	}
	// The body here is image bytes, not JSON, so this goes through do rather
	// than getJSON.
	resp, err := c.do(ctx, "/Items/"+url.PathEscape(id)+"/Images/Primary", q)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}
