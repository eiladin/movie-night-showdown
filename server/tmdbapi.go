package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// tmdbAPI is the authenticated half of this application's TMDB access: the base
// URL of api.themoviedb.org, the v4 Read Access Token, and the client used to
// reach them.
//
// It is embedded by every type that calls the API — TMDBSource for Discover,
// providerResolver for the watch-provider list — so the bearer header exists in
// exactly one place. The two built it separately once and drifted: only one of
// them classified a non-200, so a rejected token was indistinguishable from an
// unreachable host on the settings screen.
//
// The image CDN is deliberately not part of this. See TMDBSource.fetchPoster.
type tmdbAPI struct {
	token   string
	baseURL string
	http    *http.Client
}

// newRequest builds an authenticated GET against the TMDB API. path is an
// absolute path such as "/discover/movie"; q may be nil.
//
// This is the only place the bearer scheme is written.
func (a tmdbAPI) newRequest(ctx context.Context, path string, q url.Values) (*http.Request, error) {
	target := a.baseURL + path
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.token)
	return req, nil
}

// do performs an authenticated GET and returns the response on 200 only. The
// caller owns the body.
//
// A non-200 is wrapped by statusFailure so sourceerror.go can classify 401 and
// 403 as a rejected token rather than an unreachable host. The status is all
// that goes into the message: the token must never reach a log line.
func (a tmdbAPI) do(ctx context.Context, path string, q url.Values) (*http.Response, error) {
	req, err := a.newRequest(ctx, path, q)
	if err != nil {
		return nil, err
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tmdb: GET %s: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, statusFailure(resp.StatusCode, fmt.Errorf("tmdb: GET %s returned %s", path, resp.Status))
	}
	return resp, nil
}

// getJSON performs an authenticated GET and decodes the body into out.
func (a tmdbAPI) getJSON(ctx context.Context, path string, q url.Values, out any) error {
	resp, err := a.do(ctx, path, q)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("tmdb: decode %s response: %w", path, err)
	}
	return nil
}
