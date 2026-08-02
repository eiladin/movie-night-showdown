package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// StreamingProvider is one resolved streaming service this deployment offers.
//
// ID is the stable, URL-safe identifier used everywhere a source is named: the
// image proxy path, the host's saved selection, and the badge on a card. Name
// is what people see. TMDBID is what the Discover query filters on.
type StreamingProvider struct {
	ID     SourceID
	Name   string
	TMDBID int
}

// knownProvider is one entry of the built-in table.
type knownProvider struct {
	slug   SourceID
	name   string
	tmdbID int
}

// knownProviders is the offline table of widely-used services. It exists for
// two reasons:
//
//   - It pins the identifiers of the services this app shipped with. TMDB calls
//     provider 9 "Amazon Prime Video"; deployments and saved host selections
//     already call it "prime", and that must not change under them.
//   - It is the fallback when TMDB cannot be reached at startup, so a network
//     blip does not cost a deployment its sources.
//
// It is not a limit on what can be configured. Any TMDB watch provider can be
// named in STREAMING_PROVIDERS, by name or by numeric id, and is resolved
// against TMDB's provider list; this table only short-circuits the common case.
var knownProviders = []knownProvider{
	{SourceNetflix, "Netflix", 8},
	{SourcePrime, "Prime Video", 9},
	{SourceDisney, "Disney+", 337},
	{"hulu", "Hulu", 15},
	{"peacock", "Peacock", 386},
	{"max", "HBO Max", 1899},
	{"apple", "Apple TV+", 350},
	{"paramount", "Paramount+", 531},
}

// lookupKnownProvider finds a table entry by its slug.
func lookupKnownProvider(slug string) (knownProvider, bool) {
	for _, p := range knownProviders {
		if string(p.slug) == slug {
			return p, true
		}
	}
	return knownProvider{}, false
}

// slugifyProvider turns a provider name into a stable, URL-safe id. The id
// appears in the image proxy path, so anything outside [a-z0-9-] is folded to a
// separator.
func slugifyProvider(name string) string {
	var b strings.Builder
	lastDash := true // leading separators are dropped
	for _, r := range strings.ToLower(name) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastDash = false
		case !lastDash:
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// tmdbProviderList is the shape of GET /watch/providers/movie.
type tmdbProviderList struct {
	Results []struct {
		ProviderID   int    `json:"provider_id"`
		ProviderName string `json:"provider_name"`
	} `json:"results"`
}

// providerResolver resolves configured provider entries against TMDB. baseURL
// is a field rather than the package constant so tests can point it at a stub.
type providerResolver struct {
	token   string
	region  string
	baseURL string
	http    *http.Client
}

func newProviderResolver(cfg Config) *providerResolver {
	return &providerResolver{
		token:   cfg.TMDBReadToken,
		region:  cfg.TMDBWatchRegion,
		baseURL: cfg.tmdbBaseURL,
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

// fetch retrieves every movie watch provider TMDB lists for the configured
// region.
func (r *providerResolver) fetch(ctx context.Context) (tmdbProviderList, error) {
	q := url.Values{}
	q.Set("watch_region", r.region)
	endpoint := r.baseURL + "/watch/providers/movie?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return tmdbProviderList{}, err
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Accept", "application/json")

	resp, err := r.http.Do(req)
	if err != nil {
		return tmdbProviderList{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// The token is never included in the error: it would reach the log.
		return tmdbProviderList{}, fmt.Errorf("tmdb: GET /watch/providers/movie: unexpected status %d", resp.StatusCode)
	}

	var list tmdbProviderList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return tmdbProviderList{}, fmt.Errorf("tmdb: decoding provider list: %w", err)
	}
	return list, nil
}

// resolveStreamingProviders turns the configured entries into concrete
// providers. Each entry is a provider name (matched case-insensitively, and
// also against its slug) or a numeric TMDB provider id.
//
// The built-in table is consulted first, so the common configuration resolves
// with no network call at all and keeps its established identifiers. TMDB is
// queried only when something is left over, and a failed query is not fatal:
// whatever the table resolved still works, and the rest are logged and skipped.
// An unresolvable entry never fails startup, matching how unknown entries
// behaved when the provider set was fixed.
func resolveStreamingProviders(ctx context.Context, cfg Config, requested []string) []StreamingProvider {
	if cfg.TMDBReadToken == "" || len(requested) == 0 {
		return nil
	}

	resolved := make([]StreamingProvider, 0, len(requested))
	var leftover []string
	for _, entry := range requested {
		if p, ok := lookupKnownProvider(entry); ok {
			resolved = append(resolved, StreamingProvider{ID: p.slug, Name: p.name, TMDBID: p.tmdbID})
			continue
		}
		leftover = append(leftover, entry)
	}
	if len(leftover) == 0 {
		return resolved
	}

	list, err := newProviderResolver(cfg).fetch(ctx)
	if err != nil {
		log.Printf("config: could not reach TMDB to resolve %v: %v", leftover, err)
	}

	// Index by both the provider's name and its slug, so "hbo max", "HBO Max",
	// and "hbo-max" all find the same service.
	byName := make(map[string]StreamingProvider, len(list.Results)*2)
	byID := make(map[int]StreamingProvider, len(list.Results))
	for _, p := range list.Results {
		sp := StreamingProvider{ID: SourceID(slugifyProvider(p.ProviderName)), Name: p.ProviderName, TMDBID: p.ProviderID}
		if sp.ID == "" {
			continue
		}
		byName[strings.ToLower(p.ProviderName)] = sp
		byName[string(sp.ID)] = sp
		byID[p.ProviderID] = sp
	}

	for _, entry := range leftover {
		// A numeric entry is a TMDB provider id. It stays usable even when the
		// name lookup failed: the id is all the Discover query needs.
		if id, err := strconv.Atoi(entry); err == nil {
			if sp, ok := byID[id]; ok {
				resolved = append(resolved, sp)
				continue
			}
			log.Printf("config: STREAMING_PROVIDERS id %d is not in TMDB's list for region %s; offering it unnamed", id, cfg.TMDBWatchRegion)
			resolved = append(resolved, StreamingProvider{
				ID:     SourceID("tmdb-" + entry),
				Name:   "Provider " + entry,
				TMDBID: id,
			})
			continue
		}
		if sp, ok := byName[entry]; ok {
			resolved = append(resolved, sp)
			continue
		}
		log.Printf("config: ignoring unknown STREAMING_PROVIDERS entry %q (no TMDB watch provider of that name in region %s)", entry, cfg.TMDBWatchRegion)
	}
	return resolved
}
