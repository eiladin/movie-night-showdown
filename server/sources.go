package server

import (
	"context"
	"errors"
	"log"
	"strconv"
	"sync"
)

// errAllSourcesFailed is returned when no selected source produced results, so
// there is nothing to deal.
var errAllSourcesFailed = errors.New("every selected source failed")

// sourceResult is one source's contribution to the shoe, or its failure.
type sourceResult struct {
	source MovieSource
	movies []Movie
	err    error
}

// selectSources returns the sources the host asked for, in a stable order,
// skipping any that are not configured on this deployment. An empty or
// unrecognized selection falls back to the first configured source in canonical
// order — Jellyfin when this deployment has it, which preserves the behaviour
// the app had before streaming sources existed, and the leading streaming
// service on a streaming-only deployment.
//
// order is the deployment's canonical source order (see Server.order). It
// cannot be a package-level list: which streaming services exist is decided by
// configuration and resolved at startup.
func selectSources(available map[SourceID]MovieSource, requested []SourceID, order []SourceID) []MovieSource {
	want := make(map[SourceID]bool, len(requested))
	for _, r := range requested {
		want[r] = true
	}
	out := make([]MovieSource, 0, len(order))
	for _, id := range order {
		if !want[id] {
			continue
		}
		if s, ok := available[id]; ok {
			out = append(out, s)
		}
	}
	// An empty or entirely-unavailable selection falls back to the first
	// configured source. Do not guard the loop above on len(requested): that
	// makes an empty selection match every source instead of none, and the
	// fallback never runs.
	if len(out) == 0 {
		for _, id := range order {
			if s, ok := available[id]; ok {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

// configuredSources returns the ids of every source this deployment has
// credentials for, in the canonical picker order. A source absent from this
// list cannot be selected: it would be dropped silently at query time.
func configuredSources(available map[SourceID]MovieSource, order []SourceID) []SourceDescriptor {
	out := make([]SourceDescriptor, 0, len(order))
	for _, id := range order {
		if s, ok := available[id]; ok {
			u, ok := s.(UnwatchedSource)
			unwatched := ok && u.SupportsUnwatched()
			out = append(out, SourceDescriptor{
				ID:                id,
				Label:             sourceLabel(s),
				SupportsUnwatched: unwatched,
			})
		}
	}
	return out
}

// SourceDescriptor names one selectable source for clients. The label travels
// with the id because the streaming set is open: the frontend cannot hold a
// table of display names for providers it has never heard of.
type SourceDescriptor struct {
	ID    SourceID `json:"id"`
	Label string   `json:"label"`
	// SupportsUnwatched reports whether this source can filter on play state.
	// It is declared here rather than inferred from the id because the source
	// set is open and the frontend must not hold a table of capabilities for
	// providers it has never heard of.
	SupportsUnwatched bool `json:"supportsUnwatched"`
}

// gatherVocabulary unions the filter values of every selected source, returning
// the merged vocabulary and a problem for any source that failed.
//
// The union is deliberate: a value only one selected source recognizes is still
// worth offering, because selecting it yields that source's matches rather than
// nothing. An intersection would silently remove genres from a host's own
// library the moment they added a streaming service.
//
// Sources are visited in the deployment's canonical order, and the first name
// for a genre wins. Since that order puts Jellyfin first, the library's own
// vocabulary is canonical — "Sci-Fi" from Jellyfin suppresses TMDB's "Science
// Fiction" rather than both appearing, because they are the same genre.
//
// Partial failure degrades like gatherShoe: a picker missing one source's
// values beats a picker that renders nothing. An error is returned only when
// every source failed.
func gatherVocabulary(ctx context.Context, sources []MovieSource) (AvailableFilters, []SourceProblem, error) {
	type result struct {
		src     MovieSource
		filters AvailableFilters
		err     error
	}
	results := make([]result, len(sources))
	var wg sync.WaitGroup
	for i, src := range sources {
		v, ok := src.(VocabularySource)
		if !ok {
			// Not a failure: a source with no vocabulary simply contributes
			// no values, so it must not be reported as unavailable.
			results[i] = result{src: src}
			continue
		}
		wg.Add(1)
		go func(i int, src MovieSource, v VocabularySource) {
			defer wg.Done()
			f, err := v.Vocabulary(ctx)
			results[i] = result{src: src, filters: f, err: err}
		}(i, src, v)
	}
	wg.Wait()

	merged := AvailableFilters{Genres: []string{}, OfficialRatings: []string{}}
	failed := make([]SourceProblem, 0)
	ok := false
	seenGenre := make(map[string]bool)
	seenRating := make(map[string]bool)
	for _, r := range results {
		if r.err != nil {
			log.Printf("source %s vocabulary failed: %v", r.src.ID(), r.err)
			failed = append(failed, sourceProblem(r.src, r.err))
			continue
		}
		ok = true
		for _, g := range r.filters.Genres {
			key := canonicalGenreKey(g)
			if seenGenre[key] {
				continue
			}
			seenGenre[key] = true
			merged.Genres = append(merged.Genres, g)
		}
		for _, c := range r.filters.OfficialRatings {
			if seenRating[c] {
				continue
			}
			seenRating[c] = true
			merged.OfficialRatings = append(merged.OfficialRatings, c)
		}
	}
	if !ok {
		return AvailableFilters{}, failed, errAllSourcesFailed
	}
	return merged, failed, nil
}

// canonicalGenreKey collapses genre names that mean the same thing. Names
// sharing a TMDB genre id are one genre under different labels, so they key on
// that id; anything TMDB does not recognize is library-specific and keys on its
// own name.
func canonicalGenreKey(name string) string {
	if id, ok := tmdbGenreIDs[name]; ok {
		return strconv.Itoa(id)
	}
	return name
}

// fetchDepth is how many candidates a source contributes to the shoe. A source
// that declares its own depth decides for itself; the rest use the streaming
// default.
func fetchDepth(s MovieSource) int {
	if d, ok := s.(DepthedSource); ok {
		return d.FetchDepth()
	}
	return streamingFetchDepth
}

// gatherShoe queries every source concurrently and merges their results into
// one shoe. It returns the merged movies and a problem for any source that
// failed, naming what went wrong with it.
//
// Partial failure degrades rather than aborting: a movie night should not be
// blocked because one upstream is down. The caller is responsible for telling
// the host which sources are missing. An error is returned only when every
// source failed, since an empty shoe has nothing to deal.
func gatherShoe(ctx context.Context, sources []MovieSource, f Filters) ([]Movie, []SourceProblem, error) {
	results := make([]sourceResult, len(sources))
	var wg sync.WaitGroup
	for i, src := range sources {
		wg.Add(1)
		go func(i int, src MovieSource) {
			defer wg.Done()
			// Filters pass through unchanged apart from the depth: a source
			// that cannot honour a filter ignores it itself.
			sf := f
			sf.Limit = fetchDepth(src)
			movies, err := src.Search(ctx, sf)
			results[i] = sourceResult{source: src, movies: movies, err: err}
		}(i, src)
	}
	wg.Wait()

	sets := make([][]Movie, 0, len(results))
	failed := make([]SourceProblem, 0)
	for _, r := range results {
		if r.err != nil {
			log.Printf("source %s failed: %v", r.source.ID(), r.err)
			failed = append(failed, sourceProblem(r.source, r.err))
			continue
		}
		sets = append(sets, r.movies)
	}
	if len(sets) == 0 {
		return nil, failed, errAllSourcesFailed
	}
	return MergeMovies(sets...), failed, nil
}
