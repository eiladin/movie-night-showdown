package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// PlexClient talks to a Plex Media Server's HTTP API.
//
// Plex is a peer of Jellyfin, not a variant of it: the two share the Movie
// type and the MovieSource interface and nothing else. Their query params,
// their JSON shapes, and their rating scales all differ, so the mapping lives
// here rather than in a shared abstraction.
// One client is one library section. A deployment that names several sections
// registers several clients against the same server, each its own movie source.
type PlexClient struct {
	baseURL string
	token   string
	// section is the library section key holding movies. When empty it is
	// discovered on first use, since a server may have several sections and
	// only one of them is type "movie".
	section string
	id      SourceID
	name    string
	http    *http.Client

	// discovered holds the lazily-found section key. Discovery needs the network,
	// so it cannot happen in the constructor without making startup depend on
	// Plex being reachable.
	//
	// It is a lazyValue rather than a sync.Once because sync.Once caches a
	// failure permanently: one unreachable moment on the first query and
	// discovery never ran again for the life of the process. That presented as
	// Plex working one day and returning nothing the next, recoverable only by
	// recreating the container.
	discovered lazyValue[string]
}

// NewPlexClient builds a client for one library section. A zero libraryRef falls
// back to discovering the first movie section, which is what a deployment that has
// chosen none has always had.
func NewPlexClient(cfg Config, library libraryRef) *PlexClient {
	return &PlexClient{
		baseURL: strings.TrimRight(cfg.PlexURL, "/"),
		token:   cfg.PlexToken,
		// For Plex the library identifier *is* the section key, so there is no
		// second lookup to do.
		section: library.ID,
		id:      libraryScopedID(SourcePlex, library),
		name:    libraryScopedName("Plex", library),
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

// ID identifies this source. PlexClient implements MovieSource.
func (c *PlexClient) ID() SourceID { return c.id }

// Name implements NamedSource, returning the qualified library name.
func (c *PlexClient) Name() string { return c.name }

// FetchDepth implements DepthedSource. Like Jellyfin, a local library is cheap
// to page through, so it contributes more candidates than a remote catalog.
func (c *PlexClient) FetchDepth() int { return jellyfinFetchDepth }

// SupportsUnwatched implements UnwatchedSource. Unlike Jellyfin, no extra
// configuration is needed: a Plex token identifies a user, so play state is
// always a question the credential can answer.
func (c *PlexClient) SupportsUnwatched() bool { return true }

// plexResponse is the envelope every Plex endpoint returns.
type plexResponse struct {
	MediaContainer struct {
		TotalSize int          `json:"totalSize"`
		Size      int          `json:"size"`
		Metadata  []plexItem   `json:"Metadata"`
		Directory []plexDirect `json:"Directory"`
	} `json:"MediaContainer"`
}

// plexDirect is one entry of a Directory listing: a library section, or a
// filter value such as a genre or content rating.
type plexDirect struct {
	Key   string `json:"key"`
	Type  string `json:"type"`
	Title string `json:"title"`
	Tag   string `json:"tag"`
}

// name returns the display value of a Directory entry, which Plex calls Title
// on sections and Tag on filter values.
func (d plexDirect) name() string {
	if d.Tag != "" {
		return d.Tag
	}
	return d.Title
}

type plexItem struct {
	RatingKey string `json:"ratingKey"`
	Title     string `json:"title"`
	Year      int    `json:"year"`
	Summary   string `json:"summary"`
	// ContentRating is already in the same vocabulary as Jellyfin's
	// OfficialRating ("PG-13"), so it needs no translation.
	ContentRating string `json:"contentRating"`
	// Rating is a critic score and AudienceRating an audience score. Only the
	// latter is comparable with Jellyfin's CommunityRating; see toMovie.
	Rating         float64 `json:"rating"`
	AudienceRating float64 `json:"audienceRating"`
	// Duration is milliseconds, where Jellyfin reports 100ns ticks.
	Duration int64  `json:"duration"`
	Thumb    string `json:"thumb"`
	// Genre is truncated by Plex to the first two tags in a list response,
	// even for a film its own detail endpoint reports five for. It is
	// therefore display data only: the genre filter is applied server-side by
	// Plex against the untruncated tags, and the picker's vocabulary comes
	// from the section's genre endpoint, so neither depends on this field
	// being complete. Filling it in would cost one detail request per movie —
	// 151 requests to deal a 150-card shoe — to decorate a poster card.
	Genre []plexDirect `json:"Genre"`
	GUID  []plexGUID   `json:"Guid"`
	// LegacyGUID is Plex's own "plex://movie/..." identifier. It exists here
	// only so it has an exact match to land in: encoding/json falls back to a
	// case-insensitive match, so without this field the string "guid" is
	// decoded into the GUID slice and the whole response fails. The two json
	// tags differ only in case, which is exactly the collision being avoided.
	LegacyGUID string `json:"guid"`
}

type plexGUID struct {
	ID string `json:"id"`
}

// newRequest builds an authenticated GET against this client's server.
//
// It is the one place that knows how this application talks to Plex: base URL
// joining and the X-Plex-Token header. path is an absolute path such as
// "/library/sections"; q may be nil.
//
// wantJSON asks for a JSON body. It is not set on every request: the poster path
// wants image bytes, and the header exists for the reason given on get.
func (c *PlexClient) newRequest(ctx context.Context, path string, q url.Values, wantJSON bool) (*http.Request, error) {
	reqURL := c.baseURL + path
	if len(q) > 0 {
		reqURL += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	if wantJSON {
		req.Header.Set("Accept", "application/json")
	}
	req.Header.Set("X-Plex-Token", c.token)
	return req, nil
}

// do performs an authenticated GET and returns the response on 200 only. The
// caller owns the body.
//
// A non-200 is wrapped by statusFailure so sourceerror.go can classify 401 and
// 403 as a credential problem rather than an unreachable host. Every call site
// goes through here, so a stale token classifies the same way whether it was
// listing movies or fetching artwork.
func (c *PlexClient) do(ctx context.Context, path string, q url.Values, wantJSON bool) (*http.Response, error) {
	req, err := c.newRequest(ctx, path, q, wantJSON)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("plex: GET %s: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, statusFailure(resp.StatusCode, fmt.Errorf("plex: GET %s returned %s", path, resp.Status))
	}
	return resp, nil
}

// get issues an authenticated GET against the Plex server and decodes the
// response.
//
// The status is checked before decoding because Plex's error path ignores the
// Accept header entirely and returns an HTML body; handing that to the decoder
// would report "invalid character '<'" instead of "unauthorized".
func (c *PlexClient) get(ctx context.Context, path string, q url.Values) (*plexResponse, error) {
	resp, err := c.do(ctx, path, q, true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var parsed plexResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("plex: decode %s response: %w", path, err)
	}
	return &parsed, nil
}

// movieSection returns the library section key to query, discovering it on
// first use when PLEX_LIBRARY_SECTION is unset.
//
// Discovery picks the first section of type "movie". A server with several
// movie sections therefore needs the setting; guessing would silently deal
// from whichever section Plex happened to list first.
func (c *PlexClient) movieSection(ctx context.Context) (string, error) {
	// A configured section needs no discovery and no locking.
	if c.section != "" {
		return c.section, nil
	}
	return c.discovered.get(ctx, func(ctx context.Context) (string, error) {
		sections, err := c.movieSections(ctx)
		if err != nil {
			return "", err
		}
		if len(sections) == 0 {
			return "", fmt.Errorf("plex: no library section of type %q; set PLEX_LIBRARY_SECTIONS", "movie")
		}
		return sections[0].Key, nil
	})
}

// movieSections lists the server's movie libraries.
//
// One reader, so the parsing of /library/sections lives in one place: the check
// route and section discovery both need it, and two callers with their own filter
// is how they come to disagree about what counts as a movie library.
func (c *PlexClient) movieSections(ctx context.Context) ([]plexDirect, error) {
	parsed, err := c.get(ctx, "/library/sections", nil)
	if err != nil {
		return nil, err
	}
	out := make([]plexDirect, 0, len(parsed.MediaContainer.Directory))
	for _, d := range parsed.MediaContainer.Directory {
		if d.Type == "movie" {
			out = append(out, d)
		}
	}
	return out, nil
}

// Search implements MovieSource by delegating to Movies and discarding the
// total count, which only the library preview endpoint needs.
func (c *PlexClient) Search(ctx context.Context, f Filters) ([]Movie, error) {
	movies, _, err := c.Movies(ctx, f)
	return movies, err
}

// Movies fetches movies from Plex, applying filters, and maps them onto the
// Movie type used by the rest of the app.
//
// Like the Jellyfin client it returns both the (capped) list and the true
// total matching the filters, which Plex reports as totalSize independently of
// the container size.
func (c *PlexClient) Movies(ctx context.Context, filters Filters) ([]Movie, int, error) {
	section := filters.LibraryID
	if section == "" {
		var err error
		section, err = c.movieSection(ctx)
		if err != nil {
			return nil, 0, err
		}
	}

	q := url.Values{}
	q.Set("type", "1") // 1 = movie
	// Without this the Guid array is omitted, and with it the TMDB id that
	// lets a Plex item merge with the same film from a streaming source.
	q.Set("includeGuids", "1")
	filters.applyPlex(q)

	parsed, err := c.get(ctx, "/library/sections/"+url.PathEscape(section)+"/all", q)
	if err != nil {
		return nil, 0, err
	}

	movies := make([]Movie, 0, len(parsed.MediaContainer.Metadata))
	for _, it := range parsed.MediaContainer.Metadata {
		// Plex has no server-side minimum-rating filter, so it is applied here.
		// Doing it after the fetch means the rating floor narrows the sample
		// rather than the query, which is acceptable for a filter the host
		// uses to exclude a tail.
		m := it.toMovie(c.id)
		if filters.RatingMin > 0 && m.CommunityRating < filters.RatingMin {
			continue
		}
		movies = append(movies, m)
	}

	total := parsed.MediaContainer.TotalSize
	if total == 0 {
		total = len(movies)
	}
	return movies, total, nil
}

// applyPlex maps Filters onto a Plex /library/sections/{key}/all query.
//
// It is deliberately separate from apply (the Jellyfin form) rather than
// generalized: the two servers agree on no param name, no delimiter, and no
// range syntax, so a shared applier would be a switch statement wearing an
// interface. RatingMin is absent because Plex has no equivalent param; Movies
// applies it after the fetch.
func (f Filters) applyPlex(q url.Values) {
	if len(f.Genres) > 0 {
		// Plex ORs repeated values for one field with a "," delimiter.
		q.Set("genre", strings.Join(f.Genres, ","))
	}
	if f.YearMin > 0 || f.YearMax > 0 {
		// Plex's range operators are inconsistent across server versions, but
		// an explicit value list is honored the same way Jellyfin's Years is.
		q.Set("year", yearsList(f.YearMin, f.YearMax))
	}
	if len(f.OfficialRatings) > 0 {
		q.Set("contentRating", strings.Join(f.OfficialRatings, ","))
	}
	if f.Unwatched {
		q.Set("unwatched", "1")
	}
	if f.Limit > 0 {
		// Randomize server-side for the same reason Jellyfin does: without it
		// the cap takes the first N of title order and every session is dealt
		// the same deck.
		q.Set("sort", "random")
		q.Set("X-Plex-Container-Start", "0")
		q.Set("X-Plex-Container-Size", strconv.Itoa(f.Limit))
	}
}

// toMovie maps one Plex item onto the shared Movie type.
//
// source is the id of the client that fetched it, which under one source per
// library is not the bare service id. The poster path has to name the source that
// can serve the image: the proxy looks a fetcher up by that id.
func (it plexItem) toMovie(source SourceID) Movie {
	posterURL := "/api/images/" + string(source) + "/" + it.RatingKey
	if tag := plexThumbTag(it.Thumb); tag != "" {
		posterURL += "?tag=" + url.QueryEscape(tag)
	}
	// Prefer the TMDB id so a library item and the same film from a streaming
	// source collapse into one deck entry, exactly as the Jellyfin mapper does.
	// Items without one fall back to a Plex-namespaced id and never merge.
	id := "plex:" + it.RatingKey
	if tmdb := plexTMDBID(it.GUID); tmdb != "" {
		id = "tmdb:" + tmdb
	}
	genres := make([]string, 0, len(it.Genre))
	for _, g := range it.Genre {
		genres = append(genres, g.name())
	}
	// AudienceRating is the community score and shares Jellyfin's 0-10 scale
	// and population. Rating is a critic score, so it is only a fallback: a
	// deck mixing sources must not compare critics against audiences in the
	// leaderboard tiebreak.
	community := it.AudienceRating
	if community == 0 {
		community = it.Rating
	}
	return Movie{
		ID:              id,
		Title:           it.Title,
		Year:            it.Year,
		Genres:          genres,
		Overview:        it.Summary,
		Runtime:         int(it.Duration / 1000 / 60),
		CommunityRating: community,
		OfficialRating:  it.ContentRating,
		PosterURL:       posterURL,
		// The badge label is the bare service name, not the qualified library
		// name; the source list carries the qualified form separately.
		Availability: []Availability{{Source: source, Label: "Plex"}},
	}
}

// plexTMDBID returns the numeric TMDB id from a Plex Guid list, or "" when the
// item has none (an unmatched item, or one matched by a non-TMDB agent).
func plexTMDBID(guids []plexGUID) string {
	for _, g := range guids {
		if rest, ok := strings.CutPrefix(g.ID, "tmdb://"); ok {
			return rest
		}
	}
	return ""
}

// plexThumbTag extracts the version stamp from a thumb path such as
// "/library/metadata/8014/thumb/1782791305". It is the structural twin of
// Jellyfin's Primary image tag: Plex changes it when the artwork changes, so
// using it as the cache key pins the exact bytes.
func plexThumbTag(thumb string) string {
	i := strings.LastIndex(thumb, "/")
	if i < 0 || i == len(thumb)-1 {
		return ""
	}
	return thumb[i+1:]
}

// Vocabulary implements VocabularySource, reporting the genres and content
// ratings actually present in the movie section so the picker offers exactly
// what is on the shelf.
func (c *PlexClient) Vocabulary(ctx context.Context) (AvailableFilters, error) {
	section, err := c.movieSection(ctx)
	if err != nil {
		return AvailableFilters{}, err
	}
	genres, err := c.filterValues(ctx, section, "genre")
	if err != nil {
		return AvailableFilters{}, err
	}
	ratings, err := c.filterValues(ctx, section, "contentRating")
	if err != nil {
		return AvailableFilters{}, err
	}
	return AvailableFilters{Genres: genres, OfficialRatings: ratings}, nil
}

// filterValues lists one filter field's values for a section.
func (c *PlexClient) filterValues(ctx context.Context, section, field string) ([]string, error) {
	parsed, err := c.get(ctx, "/library/sections/"+url.PathEscape(section)+"/"+field, nil)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(parsed.MediaContainer.Directory))
	for _, d := range parsed.MediaContainer.Directory {
		if name := d.name(); name != "" {
			out = append(out, name)
		}
	}
	return out, nil
}

// fetchPoster downloads a movie's thumbnail from Plex. The tag is the thumb
// path's version stamp, which pins the exact image version so the cache key
// and the bytes agree.
func (c *PlexClient) fetchPoster(ctx context.Context, id, tag string) ([]byte, error) {
	path := "/library/metadata/" + url.PathEscape(id) + "/thumb"
	if tag != "" {
		path += "/" + url.PathEscape(tag)
	}
	// No Accept header: this wants image bytes, not JSON.
	resp, err := c.do(ctx, path, nil, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}
