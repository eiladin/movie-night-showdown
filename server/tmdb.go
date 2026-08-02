package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	tmdbAPIBase   = "https://api.themoviedb.org/3"
	tmdbImageBase = "https://image.tmdb.org/t/p/w500"

	// tmdbWatchRegion is hardcoded: the deployment serves one household.
	tmdbWatchRegion = "US"

	// tmdbVoteCountFloor keeps unrated obscurities out of the deck. Measured
	// against the live API on 2026-07-31: at 50 the sampled pages contain
	// titles with fewer than 150 votes; at 200 every sampled title is a
	// recognizable film and the remaining pools are 74/110/35 pages across the
	// three providers - ample depth for random page sampling.
	tmdbVoteCountFloor = 200

	// tmdbPageSize is fixed by the API and is not configurable.
	tmdbPageSize = 20

	// tmdbPagesPerProvider is how many random pages each provider contributes.
	tmdbPagesPerProvider = 3

	// tmdbMaxSampledPages bounds the sampling window. Sampling deeper than this
	// reaches films with so little presence that the vote floor is the only
	// thing keeping them viable.
	tmdbMaxSampledPages = 25

	// tmdbMaxPosterBytes bounds a single poster fetch. The image proxy is
	// unauthenticated, so an unbounded read is a memory and disk amplification
	// vector.
	tmdbMaxPosterBytes = 8 << 20 // 8 MiB
)

// tmdbProviderIDs maps each supported source to its TMDB watch provider id.
var tmdbProviderIDs = map[SourceID]int{
	SourceNetflix: 8,
	SourcePrime:   9,
	SourceDisney:  337,
}

// tmdbGenreIDs maps Jellyfin genre names to TMDB genre ids. Jellyfin is the
// canonical vocabulary: the picker is built from the host's actual library
// genres, and a genre with no entry here simply contributes nothing from
// streaming sources rather than being an error.
var tmdbGenreIDs = map[string]int{
	"Action":           28,
	"Adventure":        12,
	"Animation":        16,
	"Comedy":           35,
	"Crime":            80,
	"Documentary":      99,
	"Drama":            18,
	"Family":           10751,
	"Fantasy":          14,
	"History":          36,
	"Horror":           27,
	"Music":            10402,
	"Musical":          10402,
	"Mystery":          9648,
	"Romance":          10749,
	"Science Fiction":  878,
	"Sci-Fi":           878,
	"Sci-Fi & Fantasy": 878,
	"TV Movie":         10770,
	"Thriller":         53,
	"War":              10752,
	"Western":          37,
}

// tmdbCertificationOrder lists the US certifications TMDB recognizes, from
// most to least permissive. The order is what the picker offers when there is
// no Jellyfin library to enumerate.
var tmdbCertificationOrder = []string{"G", "PG", "PG-13", "R", "NC-17", "NR"}

// tmdbCertifications is the set form of tmdbCertificationOrder. A Jellyfin
// OfficialRating outside this set is dropped from the streaming query rather
// than being approximated.
var tmdbCertifications = func() map[string]bool {
	m := make(map[string]bool, len(tmdbCertificationOrder))
	for _, c := range tmdbCertificationOrder {
		m[c] = true
	}
	return m
}()

// defaultAvailableFilters is the filter vocabulary offered when there is no
// Jellyfin library to enumerate — a streaming-only deployment. It is derived
// from the TMDB maps rather than hand-written, so the picker can only offer
// values a streaming query actually honors.
func defaultAvailableFilters() AvailableFilters {
	genres := make([]string, 0, len(tmdbGenreNames))
	for _, name := range tmdbGenreNames {
		genres = append(genres, name)
	}
	sort.Strings(genres)
	ratings := make([]string, len(tmdbCertificationOrder))
	copy(ratings, tmdbCertificationOrder)
	return AvailableFilters{Genres: genres, OfficialRatings: ratings}
}

// TMDBSource queries one streaming provider's catalog through TMDB's Discover
// endpoint. One instance is constructed per supported provider; all of them
// share an HTTP client and a token.
type TMDBSource struct {
	source   SourceID
	provider int
	token    string
	baseURL  string
	imageURL string
	http     *http.Client
}

// NewTMDBSource returns a source for one provider. It returns nil if the
// provider is not supported or no token is configured, so callers can build the
// available sources by filtering nils.
func NewTMDBSource(cfg Config, source SourceID) *TMDBSource {
	provider, ok := tmdbProviderIDs[source]
	if !ok || cfg.TMDBReadToken == "" {
		return nil
	}
	return &TMDBSource{
		source:   source,
		provider: provider,
		token:    cfg.TMDBReadToken,
		baseURL:  tmdbAPIBase,
		imageURL: tmdbImageBase,
		http:     &http.Client{Timeout: 15 * time.Second},
	}
}

// ID identifies this source. TMDBSource implements MovieSource.
func (t *TMDBSource) ID() SourceID { return t.source }

// tmdbDiscoverResponse is the subset of the Discover response this app reads.
// Discover returns neither runtime nor certification, so Movie.Runtime and
// Movie.OfficialRating stay zero-valued for streaming titles.
type tmdbDiscoverResponse struct {
	Page       int `json:"page"`
	TotalPages int `json:"total_pages"`
	Results    []struct {
		ID          int     `json:"id"`
		Title       string  `json:"title"`
		ReleaseDate string  `json:"release_date"`
		Overview    string  `json:"overview"`
		GenreIDs    []int   `json:"genre_ids"`
		VoteAverage float64 `json:"vote_average"`
		PosterPath  string  `json:"poster_path"`
	} `json:"results"`
}

// discoverParams builds the query for one page and one certification.
// certification is empty when the host selected none.
func (t *TMDBSource) discoverParams(f Filters, certification string, page int) url.Values {
	q := url.Values{}
	q.Set("with_watch_providers", strconv.Itoa(t.provider))
	q.Set("watch_region", tmdbWatchRegion)
	q.Set("with_watch_monetization_types", "flatrate")
	q.Set("sort_by", "popularity.desc")
	q.Set("vote_count.gte", strconv.Itoa(tmdbVoteCountFloor))
	q.Set("page", strconv.Itoa(page))

	if ids := mapGenres(f.Genres); len(ids) > 0 {
		// TMDB ORs genre ids joined with "|", matching Jellyfin's "|" OR
		// semantics for its Genres parameter.
		parts := make([]string, len(ids))
		for i, id := range ids {
			parts[i] = strconv.Itoa(id)
		}
		q.Set("with_genres", strings.Join(parts, "|"))
	}
	if f.YearMin > 0 {
		q.Set("primary_release_date.gte", fmt.Sprintf("%04d-01-01", f.YearMin))
	}
	if f.YearMax > 0 {
		q.Set("primary_release_date.lte", fmt.Sprintf("%04d-12-31", f.YearMax))
	}
	if f.RatingMin > 0 {
		q.Set("vote_average.gte", strconv.FormatFloat(f.RatingMin, 'f', -1, 64))
	}
	if certification != "" {
		// Exact match, never certification.lte: Jellyfin's OfficialRatings is a
		// set OR, so an ordered comparison would admit ratings the host did not
		// select. The caller issues one query per selected certification.
		q.Set("certification_country", tmdbWatchRegion)
		q.Set("certification", certification)
	}
	return q
}

// mapGenres translates Jellyfin genre names to TMDB genre ids, dropping any
// with no equivalent.
func mapGenres(names []string) []int {
	ids := make([]int, 0, len(names))
	seen := make(map[int]bool)
	for _, n := range names {
		id, ok := tmdbGenreIDs[n]
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids
}

// mapCertifications keeps only the ratings TMDB recognizes for the US.
func mapCertifications(ratings []string) []string {
	out := make([]string, 0, len(ratings))
	for _, r := range ratings {
		if tmdbCertifications[r] {
			out = append(out, r)
		}
	}
	return out
}

// fetchPage issues one Discover request.
func (t *TMDBSource) fetchPage(ctx context.Context, f Filters, certification string, page int) (tmdbDiscoverResponse, error) {
	var out tmdbDiscoverResponse
	reqURL := t.baseURL + "/discover/movie?" + t.discoverParams(f, certification, page).Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("Authorization", "Bearer "+t.token)
	req.Header.Set("accept", "application/json")

	resp, err := t.http.Do(req)
	if err != nil {
		return out, fmt.Errorf("tmdb: GET /discover/movie: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("tmdb: GET /discover/movie returned %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, fmt.Errorf("tmdb: decode /discover/movie response: %w", err)
	}
	return out, nil
}

// samplePages returns which page numbers to fetch given the total available.
// It is the equivalent of the SortBy=Random the Jellyfin query uses: TMDB has
// no random sort, so variety comes from which pages are drawn.
//
// The clamp to totalPages is load-bearing. A narrow filter can return a single
// page, and an unclamped draw from a fixed window would request pages that do
// not exist and silently contribute nothing.
func (t *TMDBSource) samplePages(totalPages int) []int {
	if totalPages <= 0 {
		return nil
	}
	if totalPages <= tmdbPagesPerProvider {
		pages := make([]int, 0, totalPages)
		for p := 1; p <= totalPages; p++ {
			pages = append(pages, p)
		}
		return pages
	}
	window := totalPages
	if window > tmdbMaxSampledPages {
		window = tmdbMaxSampledPages
	}
	chosen := make(map[int]bool, tmdbPagesPerProvider)
	pages := make([]int, 0, tmdbPagesPerProvider)
	for len(pages) < tmdbPagesPerProvider {
		p := rand.Intn(window) + 1
		if chosen[p] {
			continue
		}
		chosen[p] = true
		pages = append(pages, p)
	}
	return pages
}

// Search implements MovieSource. It issues one query per selected certification
// (or one unfiltered query when none is selected), samples pages within each,
// and merges the results.
func (t *TMDBSource) Search(ctx context.Context, f Filters) ([]Movie, error) {
	// A filter the host set must never widen the result set. When every
	// selected genre or certification maps to nothing TMDB recognizes, the
	// correct contribution from this provider is none - omitting the parameter
	// would return the provider's entire catalog instead.
	if len(f.Genres) > 0 && len(mapGenres(f.Genres)) == 0 {
		return nil, nil
	}
	certs := mapCertifications(f.OfficialRatings)
	if len(f.OfficialRatings) > 0 && len(certs) == 0 {
		return nil, nil
	}
	if len(certs) == 0 {
		certs = []string{""}
	}

	sets := make([][]Movie, 0, len(certs))
	for _, cert := range certs {
		first, err := t.fetchPage(ctx, f, cert, 1)
		if err != nil {
			return nil, err
		}
		// Page 1 is fetched to learn TotalPages. Keep its results only if it is
		// among the sampled pages - otherwise every deck would always contain
		// the provider's 20 most popular titles and the sampling would be
		// decorative.
		var set []Movie
		for _, page := range t.samplePages(first.TotalPages) {
			if page == 1 {
				set = append(set, t.toMovies(first)...)
				continue
			}
			resp, err := t.fetchPage(ctx, f, cert, page)
			if err != nil {
				return nil, err
			}
			set = append(set, t.toMovies(resp)...)
		}
		sets = append(sets, set)
	}
	merged := MergeMovies(sets...)
	// Cap the provider's contribution to the shoe. Shuffle first: sets are
	// built in certification order then page order, so a bare truncation would
	// keep only the first certification's results.
	rand.Shuffle(len(merged), func(i, j int) { merged[i], merged[j] = merged[j], merged[i] })
	if f.Limit > 0 && len(merged) > f.Limit {
		merged = merged[:f.Limit]
	}
	return merged, nil
}

// toMovies converts a Discover response into the app's Movie shape. Runtime and
// OfficialRating are left zero: Discover does not return them.
func (t *TMDBSource) toMovies(resp tmdbDiscoverResponse) []Movie {
	movies := make([]Movie, 0, len(resp.Results))
	for _, r := range resp.Results {
		if r.PosterPath == "" {
			continue // a card with no poster is not worth dealing
		}
		year := 0
		if len(r.ReleaseDate) >= 4 {
			year, _ = strconv.Atoi(r.ReleaseDate[:4])
		}
		genres := make([]string, 0, len(r.GenreIDs))
		for _, id := range r.GenreIDs {
			if name, ok := tmdbGenreNames[id]; ok {
				genres = append(genres, name)
			}
		}
		movies = append(movies, Movie{
			ID:              "tmdb:" + strconv.Itoa(r.ID),
			Title:           r.Title,
			Year:            year,
			Genres:          genres,
			Overview:        r.Overview,
			CommunityRating: r.VoteAverage,
			// Poster paths arrive with a leading slash; trim it so the proxy
			// path has exactly one segment after the source.
			PosterURL:    "/api/images/" + string(t.source) + "/" + strings.TrimPrefix(r.PosterPath, "/"),
			Availability: []Availability{{Source: t.source}},
		})
	}
	return movies
}

// fetchPoster implements PosterFetcher. id is the TMDB poster path with its
// leading slash trimmed; tag is unused (TMDB poster paths are already
// content-addressed, so artwork changes produce a new path).
func (t *TMDBSource) fetchPoster(ctx context.Context, id, tag string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.imageURL+"/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	resp, err := t.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tmdb: fetch poster %s: %w", id, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tmdb: poster %s returned %s", id, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, tmdbMaxPosterBytes))
}

// tmdbGenreNames is the reverse of tmdbGenreIDs, used to render TMDB numeric
// genre ids back as the Jellyfin-vocabulary names the card displays. Where two
// Jellyfin names share an id, the canonical name is chosen here.
var tmdbGenreNames = map[int]string{
	28: "Action", 12: "Adventure", 16: "Animation", 35: "Comedy", 80: "Crime",
	99: "Documentary", 18: "Drama", 10751: "Family", 14: "Fantasy", 36: "History",
	27: "Horror", 10402: "Music", 9648: "Mystery", 10749: "Romance",
	878: "Science Fiction", 10770: "TV Movie", 53: "Thriller", 10752: "War",
	37: "Western",
}
