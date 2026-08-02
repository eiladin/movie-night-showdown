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
type JellyfinClient struct {
	baseURL string
	apiKey  string
	userID  string
	http    *http.Client
}

// ID identifies this source. JellyfinClient implements MovieSource.
func (c *JellyfinClient) ID() SourceID { return SourceJellyfin }

// Search implements MovieSource by delegating to Movies and discarding the
// total count, which only the library preview endpoint needs.
func (c *JellyfinClient) Search(ctx context.Context, f Filters) ([]Movie, error) {
	movies, _, err := c.Movies(ctx, f)
	return movies, err
}

// NewJellyfinClient builds a client from the server Config.
func NewJellyfinClient(cfg Config) *JellyfinClient {
	return &JellyfinClient{
		baseURL: strings.TrimRight(cfg.JellyfinURL, "/"),
		apiKey:  cfg.JellyfinAPIKey,
		userID:  cfg.JellyfinUserID,
		http:    &http.Client{Timeout: 10 * time.Second},
	}
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
	filters.apply(q, c.userID != "")

	reqURL := fmt.Sprintf("%s/Items?%s", c.baseURL, q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("X-Emby-Token", c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("jellyfin: GET /Items: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("jellyfin: GET /Items returned %s", resp.Status)
	}

	var parsed jellyfinItemsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, 0, fmt.Errorf("jellyfin: decode /Items response: %w", err)
	}

	movies := make([]Movie, 0, len(parsed.Items))
	for _, it := range parsed.Items {
		posterURL := "/api/images/" + string(SourceJellyfin) + "/" + it.ID
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
		m := Movie{
			ID:              id,
			Title:           it.Name,
			Year:            it.ProductionYear,
			Genres:          it.Genres,
			Overview:        it.Overview,
			Runtime:         int(it.RunTimeTicks / 10_000_000 / 60),
			CommunityRating: it.CommunityRating,
			OfficialRating:  it.OfficialRating,
			PosterURL:       posterURL,
			Availability:    []Availability{{Source: SourceJellyfin, Label: "Jellyfin"}},
		}
		movies = append(movies, m)
	}

	return movies, parsed.TotalRecordCount, nil
}

// AvailableFilters represents the possible values for filtering the library.
type AvailableFilters struct {
	Genres          []string `json:"genres"`
	OfficialRatings []string `json:"officialRatings"`
}

// GetAvailableFilters queries Jellyfin for all unique genres and official ratings
// present in the Movie library.
func (c *JellyfinClient) GetAvailableFilters(ctx context.Context) (AvailableFilters, error) {
	reqURL := fmt.Sprintf("%s/Items/Filters?IncludeItemTypes=Movie", c.baseURL)
	if c.userID != "" {
		reqURL += "&userId=" + url.QueryEscape(c.userID)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return AvailableFilters{}, err
	}
	req.Header.Set("X-Emby-Token", c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return AvailableFilters{}, fmt.Errorf("jellyfin: GET /Items/Filters: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return AvailableFilters{}, fmt.Errorf("jellyfin: GET /Items/Filters returned %s", resp.Status)
	}

	var parsed struct {
		Genres          []string `json:"Genres"`
		OfficialRatings []string `json:"OfficialRatings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return AvailableFilters{}, fmt.Errorf("jellyfin: decode /Items/Filters response: %w", err)
	}

	return AvailableFilters{
		Genres:          parsed.Genres,
		OfficialRatings: parsed.OfficialRatings,
	}, nil
}

// fetchPoster downloads a movie's Primary poster from Jellyfin. A non-empty
// tag pins the exact image version so the cache key and the bytes agree.
func (c *JellyfinClient) fetchPoster(ctx context.Context, id, tag string) ([]byte, error) {
	reqURL := fmt.Sprintf("%s/Items/%s/Images/Primary?maxWidth=600", c.baseURL, url.PathEscape(id))
	if tag != "" {
		reqURL += "&tag=" + url.QueryEscape(tag)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Emby-Token", c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jellyfin: fetch poster %s: %w", id, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jellyfin: poster %s returned %s", id, resp.Status)
	}
	return io.ReadAll(resp.Body)
}
