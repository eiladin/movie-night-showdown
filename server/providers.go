package server

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"sort"
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

// lookupKnownProviderByID finds a table entry by its TMDB provider id.
//
// The id is the join key between the table and TMDB's list, because the names do
// not match: TMDB calls provider 9 "Amazon Prime Video" and this table calls it
// "prime". Matching on the id is what lets a service TMDB enumerated be offered
// under the identifier this application already shipped.
func lookupKnownProviderByID(tmdbID int) (knownProvider, bool) {
	for _, p := range knownProviders {
		if p.tmdbID == tmdbID {
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

// providerCatalog is the deduplicated view of a region's TMDB watch providers.
//
// It exists because TMDB's list is not a set of distinct ids, and two separate
// problems fall out of that:
//
//   - Names differing only in punctuation collapse onto one id under
//     slugifyProvider — "Apple TV" and "Apple TV+" both reduce to "apple-tv" —
//     because the id has to survive being a path segment in the image proxy. Two
//     options with one id is a duplicate React key in the picker, which
//     misrenders the moment the list is filtered and unfiltered; and resolution
//     can map an id to exactly one provider, so offering both promises a choice
//     the server cannot keep.
//   - TMDB's name for a service is not always the identifier this application
//     shipped with. Provider 9 is "Amazon Prime Video" upstream and "prime"
//     here. Slugifying the upstream name would offer a second id for a service
//     that already has one, and the two would never merge into one deck entry.
//
// Both are resolved once, in the single place the picker and resolution both read
// from. They have to agree: a picker offering an option that resolution maps to a
// different service is worse than not offering it at all.
type providerCatalog struct {
	// options are the selectable services: unique by id, ordered by name.
	options []StreamingProvider
	// byName indexes each provider's full name, lowercased. It is consulted
	// before bySlug so an exact name always outranks a slug match — that is what
	// keeps "apple tv+" reaching Apple TV+ even though the slug "apple-tv"
	// belongs to Apple TV.
	byName map[string]StreamingProvider
	// bySlug indexes each provider's canonical id and, separately, the slug of
	// its raw upstream name. The second is an alias and load-bearing for
	// compatibility: a config file written before the table was consulted holds
	// "amazon-prime-video", and that entry has to keep resolving.
	bySlug map[string]StreamingProvider
	byID   map[int]StreamingProvider
}

func newProviderCatalog(list tmdbProviderList) providerCatalog {
	// entry pairs a resolved provider with the upstream spelling it came from,
	// so the raw name and raw slug can be registered as aliases of the canonical
	// identifier the table pinned.
	type entry struct {
		provider StreamingProvider
		rawName  string
		rawSlug  string
	}

	all := make([]entry, 0, len(list.Results))
	for _, p := range list.Results {
		rawSlug := slugifyProvider(p.ProviderName)
		if rawSlug == "" {
			continue
		}
		sp := StreamingProvider{
			ID:     SourceID(rawSlug),
			Name:   p.ProviderName,
			TMDBID: p.ProviderID,
		}
		// The table wins on both id and display name. Taking the id alone would
		// leave one service labelled "Prime Video" when configured from the
		// environment and "Amazon Prime Video" when picked from this list, for
		// the same source.
		if known, ok := lookupKnownProviderByID(p.ProviderID); ok {
			sp.ID = known.slug
			sp.Name = known.name
		}
		all = append(all, entry{provider: sp, rawName: p.ProviderName, rawSlug: rawSlug})
	}

	// Sort before deduplicating. TMDB's array order is not stable between
	// requests, so keeping the first of a collision in the order received would
	// hand one id to a different provider on a later call — and that id is what
	// gets written to the config file and persisted in a host's saved selection.
	// Ordering by name makes the winner deterministic and picks the plainer name
	// ("AMC" before "AMC+"). The TMDB id breaks a tie on equal names, so the
	// order is total and two providers can never swap places between calls.
	sort.Slice(all, func(i, j int) bool {
		if all[i].provider.Name != all[j].provider.Name {
			return all[i].provider.Name < all[j].provider.Name
		}
		return all[i].provider.TMDBID < all[j].provider.TMDBID
	})

	cat := providerCatalog{
		options: make([]StreamingProvider, 0, len(all)),
		byName:  make(map[string]StreamingProvider, len(all)*2),
		bySlug:  make(map[string]StreamingProvider, len(all)*2),
		byID:    make(map[int]StreamingProvider, len(all)),
	}
	// alias registers a lookup key only when it is free, so an alias can never
	// shadow another provider's canonical identifier.
	alias := func(m map[string]StreamingProvider, key string, sp StreamingProvider) {
		if key == "" {
			return
		}
		if _, taken := m[key]; !taken {
			m[key] = sp
		}
	}

	for _, e := range all {
		sp := e.provider
		cat.byID[sp.TMDBID] = sp

		if existing, clash := cat.bySlug[string(sp.ID)]; clash && existing.TMDBID != sp.TMDBID {
			// Only the shared id is spoken for; the loser stays reachable by its
			// exact name and by its TMDB id. Logged because an operator who
			// cannot find a service in the picker should learn why from the log
			// rather than by guessing.
			log.Printf("tmdb: %q and %q both reduce to the id %q; offering %q — "+
				"select %q by its exact name or its TMDB id %d",
				existing.Name, sp.Name, sp.ID, existing.Name, sp.Name, sp.TMDBID)
			alias(cat.byName, strings.ToLower(sp.Name), sp)
			alias(cat.byName, strings.ToLower(e.rawName), sp)
			continue
		}

		cat.bySlug[string(sp.ID)] = sp
		cat.options = append(cat.options, sp)
		alias(cat.byName, strings.ToLower(sp.Name), sp)
		alias(cat.byName, strings.ToLower(e.rawName), sp)
		alias(cat.bySlug, e.rawSlug, sp)
	}
	return cat
}

// lookup finds a provider by exact name or by id slug, in that order.
func (c providerCatalog) lookup(entry string) (StreamingProvider, bool) {
	if sp, ok := c.byName[entry]; ok {
		return sp, true
	}
	sp, ok := c.bySlug[entry]
	return sp, ok
}

// providerResolver resolves configured provider entries against TMDB. baseURL
// is a field rather than the package constant so tests can point it at a stub.
type providerResolver struct {
	region string

	// tmdbAPI carries the token, the API base URL and the HTTP client, shared
	// with TMDBSource so the bearer header has one construction.
	tmdbAPI
}

func newProviderResolver(cfg Config) *providerResolver {
	return &providerResolver{
		region: cfg.TMDBWatchRegion,
		tmdbAPI: tmdbAPI{
			token:   cfg.TMDBReadToken,
			baseURL: cfg.tmdbBaseURL,
			http:    &http.Client{Timeout: 10 * time.Second},
		},
	}
}

// fetch retrieves every movie watch provider TMDB lists for the configured
// region.
func (r *providerResolver) fetch(ctx context.Context) (tmdbProviderList, error) {
	q := url.Values{}
	q.Set("watch_region", r.region)

	// A non-200 arrives classified, so the settings screen can tell a rejected
	// token from an unreachable TMDB. The token is never in the error: only the
	// path and the status are, and both reach the log.
	var list tmdbProviderList
	if err := r.getJSON(ctx, "/watch/providers/movie", q, &list); err != nil {
		return tmdbProviderList{}, err
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

	// The catalog is what the settings screen's picker is built from too, so an
	// entry saved from the picker resolves to exactly the service that was
	// offered. Building a second index here is how those two drifted apart.
	cat := newProviderCatalog(list)

	for _, entry := range leftover {
		// A numeric entry is a TMDB provider id. It stays usable even when the
		// name lookup failed: the id is all the Discover query needs.
		if id, err := strconv.Atoi(entry); err == nil {
			if sp, ok := cat.byID[id]; ok {
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
		if sp, ok := cat.lookup(entry); ok {
			resolved = append(resolved, sp)
			continue
		}
		log.Printf("config: ignoring unknown STREAMING_PROVIDERS entry %q (no TMDB watch provider of that name in region %s)", entry, cfg.TMDBWatchRegion)
	}
	return resolved
}
