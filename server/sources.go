package server

import (
	"context"
	"errors"
	"log"
	"sync"
)

// errAllSourcesFailed is returned when no selected source produced results, so
// there is nothing to deal.
var errAllSourcesFailed = errors.New("every selected source failed")

// sourceResult is one source's contribution to the shoe, or its failure.
type sourceResult struct {
	source SourceID
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
			out = append(out, SourceDescriptor{ID: id, Label: sourceLabel(s)})
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
}

// fetchDepth is how many candidates a source contributes to the shoe.
func fetchDepth(id SourceID) int {
	if id == SourceJellyfin {
		return jellyfinFetchDepth
	}
	return streamingFetchDepth
}

// gatherShoe queries every source concurrently and merges their results into
// one shoe. It returns the merged movies and the ids of any sources that
// failed.
//
// Partial failure degrades rather than aborting: a movie night should not be
// blocked because one upstream is down. The caller is responsible for telling
// the host which sources are missing. An error is returned only when every
// source failed, since an empty shoe has nothing to deal.
func gatherShoe(ctx context.Context, sources []MovieSource, f Filters) ([]Movie, []SourceID, error) {
	results := make([]sourceResult, len(sources))
	var wg sync.WaitGroup
	for i, src := range sources {
		wg.Add(1)
		go func(i int, src MovieSource) {
			defer wg.Done()
			sf := f
			sf.Limit = fetchDepth(src.ID())
			if src.ID() != SourceJellyfin {
				// "Unwatched" is a Jellyfin concept: it needs a Jellyfin user's
				// play state and has no meaning for a streaming catalog.
				sf.Unwatched = false
			}
			movies, err := src.Search(ctx, sf)
			results[i] = sourceResult{source: src.ID(), movies: movies, err: err}
		}(i, src)
	}
	wg.Wait()

	sets := make([][]Movie, 0, len(results))
	failed := make([]SourceID, 0)
	for _, r := range results {
		if r.err != nil {
			log.Printf("source %s failed: %v", r.source, r.err)
			failed = append(failed, r.source)
			continue
		}
		sets = append(sets, r.movies)
	}
	if len(sets) == 0 {
		return nil, failed, errAllSourcesFailed
	}
	return MergeMovies(sets...), failed, nil
}
