package server

import "context"

// SourceID identifies one movie source. These values are part of the
// client-facing JSON contract (see web/src/api.ts) and are used as the
// namespace prefix in Movie.ID and in the image proxy path, so they must stay
// URL-safe.
//
// The set is open: streaming sources are whatever TMDB watch providers this
// deployment configured, so clients must treat an unrecognized id as ordinary
// data rather than an error. The constants below are the ids this app has
// always shipped; they are pinned so existing configurations and saved host
// selections keep working (see knownProviders in providers.go).
type SourceID string

const (
	SourceJellyfin SourceID = "jellyfin"
	SourceNetflix  SourceID = "netflix"
	SourcePrime    SourceID = "prime"
	SourceDisney   SourceID = "disney"
)

// Availability records that a movie can be watched on one particular source.
// A movie present both in the local library and on a streaming service carries
// one entry per source.
//
// Label travels with the entry because the source set is open: a client cannot
// hold a table of display names for providers it has never heard of, so the
// server names each one at the point of use.
type Availability struct {
	Source SourceID `json:"source"`
	Label  string   `json:"label,omitempty"`
}

// NamedSource is a source that carries its own display name. Sources without
// one fall back to their id.
type NamedSource interface {
	Name() string
}

// sourceLabel is the display name for a source, falling back to its id.
func sourceLabel(s MovieSource) string {
	if n, ok := s.(NamedSource); ok && n.Name() != "" {
		return n.Name()
	}
	return string(s.ID())
}

// MovieSource is one queryable catalog of movies. Jellyfin and each supported
// streaming provider are peers; none is special-cased by the deck builder.
type MovieSource interface {
	// ID returns the source this implementation queries.
	ID() SourceID
	// Search returns movies matching the filters. The returned movies must
	// already carry an Availability entry for this source and a namespaced ID.
	Search(ctx context.Context, f Filters) ([]Movie, error)
}

var _ MovieSource = (*JellyfinClient)(nil)

// PosterFetcher fetches one poster's bytes from a source's upstream. It is
// separate from MovieSource because the image proxy resolves a fetcher by
// SourceID without needing to run a search.
type PosterFetcher interface {
	fetchPoster(ctx context.Context, id, tag string) ([]byte, error)
}

var _ PosterFetcher = (*JellyfinClient)(nil)

var _ MovieSource = (*TMDBSource)(nil)
var _ PosterFetcher = (*TMDBSource)(nil)

// MergeMovies unions several per-source result sets into one, merging rather
// than discarding duplicates: a movie returned by more than one source appears
// once, carrying every source's Availability entry.
//
// Merging (rather than keeping whichever copy was seen first) is deliberate.
// Discarding would make the surviving record's badges depend on which source
// happened to be sampled first, so the same film would show different badges
// on different nights.
//
// Identity is Movie.ID, which is namespaced by source except for movies with a
// known TMDB id, which use "tmdb:<id>" regardless of which source produced
// them. That shared namespace is what lets a library item and a streaming
// result collapse into one card.
func MergeMovies(sets ...[]Movie) []Movie {
	merged := make([]Movie, 0)
	index := make(map[string]int)
	for _, set := range sets {
		for _, m := range set {
			i, seen := index[m.ID]
			if !seen {
				index[m.ID] = len(merged)
				merged = append(merged, m)
				continue
			}
			merged[i].Availability = appendAvailability(merged[i].Availability, m.Availability)
		}
	}
	return merged
}

// appendAvailability adds every entry of extra to base that is not already
// present, preserving base's order.
func appendAvailability(base, extra []Availability) []Availability {
	for _, e := range extra {
		found := false
		for _, b := range base {
			if b.Source == e.Source {
				found = true
				break
			}
		}
		if !found {
			base = append(base, e)
		}
	}
	return base
}
