package server

import (
	"context"
	"errors"
	"testing"
)

type fakeSource struct {
	id     SourceID
	movies []Movie
	err    error
	gotLim int
	gotUnw bool
}

func (f *fakeSource) ID() SourceID { return f.id }
func (f *fakeSource) Search(ctx context.Context, flt Filters) ([]Movie, error) {
	f.gotLim = flt.Limit
	f.gotUnw = flt.Unwatched
	return f.movies, f.err
}

func TestSelectSourcesDefaultsToJellyfin(t *testing.T) {
	avail := map[SourceID]MovieSource{
		SourceJellyfin: &fakeSource{id: SourceJellyfin},
		SourceNetflix:  &fakeSource{id: SourceNetflix},
	}
	got := selectSources(avail, nil)
	if len(got) != 1 || got[0].ID() != SourceJellyfin {
		t.Fatalf("empty selection should fall back to Jellyfin alone, got %d sources", len(got))
	}
}

func TestSelectSourcesSkipsUnconfigured(t *testing.T) {
	avail := map[SourceID]MovieSource{SourceJellyfin: &fakeSource{id: SourceJellyfin}}
	got := selectSources(avail, []SourceID{SourceJellyfin, SourceDisney})
	if len(got) != 1 || got[0].ID() != SourceJellyfin {
		t.Fatalf("unconfigured sources must be skipped, got %d sources", len(got))
	}
}

func TestGatherShoeMergesAndSetsPerSourceDepth(t *testing.T) {
	jf := &fakeSource{id: SourceJellyfin, movies: []Movie{
		{ID: "tmdb:603", Availability: []Availability{{Source: SourceJellyfin}}},
	}}
	nf := &fakeSource{id: SourceNetflix, movies: []Movie{
		{ID: "tmdb:603", Availability: []Availability{{Source: SourceNetflix}}},
		{ID: "tmdb:78", Availability: []Availability{{Source: SourceNetflix}}},
	}}

	movies, failed, err := gatherShoe(context.Background(), []MovieSource{jf, nf}, Filters{Unwatched: true})
	if err != nil {
		t.Fatalf("gatherShoe: %v", err)
	}
	if len(failed) != 0 {
		t.Fatalf("expected no failures, got %v", failed)
	}
	if len(movies) != 2 {
		t.Fatalf("expected 2 merged movies, got %d", len(movies))
	}
	if jf.gotLim != jellyfinFetchDepth {
		t.Fatalf("jellyfin fetch depth = %d, want %d", jf.gotLim, jellyfinFetchDepth)
	}
	if nf.gotLim != streamingFetchDepth {
		t.Fatalf("streaming fetch depth = %d, want %d", nf.gotLim, streamingFetchDepth)
	}
	if !jf.gotUnw {
		t.Fatalf("unwatched must be passed through to Jellyfin")
	}
	if nf.gotUnw {
		t.Fatalf("unwatched must never be passed to a streaming source")
	}
}

func TestGatherShoeDegradesOnPartialFailure(t *testing.T) {
	jf := &fakeSource{id: SourceJellyfin, movies: []Movie{{ID: "jf:a"}}}
	nf := &fakeSource{id: SourceNetflix, err: errors.New("upstream down")}

	movies, failed, err := gatherShoe(context.Background(), []MovieSource{jf, nf}, Filters{})
	if err != nil {
		t.Fatalf("partial failure must not be fatal, got %v", err)
	}
	if len(movies) != 1 {
		t.Fatalf("expected the surviving source's movies, got %d", len(movies))
	}
	if len(failed) != 1 || failed[0] != SourceNetflix {
		t.Fatalf("failed = %v, want [netflix]", failed)
	}
}

func TestGatherShoeFailsWhenAllSourcesFail(t *testing.T) {
	a := &fakeSource{id: SourceJellyfin, err: errors.New("down")}
	b := &fakeSource{id: SourceNetflix, err: errors.New("down")}

	_, failed, err := gatherShoe(context.Background(), []MovieSource{a, b}, Filters{})
	if !errors.Is(err, errAllSourcesFailed) {
		t.Fatalf("expected errAllSourcesFailed, got %v", err)
	}
	if len(failed) != 2 {
		t.Fatalf("expected both sources reported failed, got %v", failed)
	}
}

func TestGatherShoeEmptyIsNotAFailure(t *testing.T) {
	empty := &fakeSource{id: SourceNetflix, movies: nil}

	movies, failed, err := gatherShoe(context.Background(), []MovieSource{empty}, Filters{})
	if err != nil {
		t.Fatalf("a source returning no movies is not a failure, got %v", err)
	}
	if len(failed) != 0 {
		t.Fatalf("failed = %v, want empty", failed)
	}
	if len(movies) != 0 {
		t.Fatalf("got %d movies, want 0", len(movies))
	}
}

func TestConfiguredSourcesUsesCanonicalOrder(t *testing.T) {
	available := map[SourceID]MovieSource{
		SourceDisney:   &fakeSource{id: SourceDisney},
		SourceJellyfin: &fakeSource{id: SourceJellyfin},
		SourceNetflix:  &fakeSource{id: SourceNetflix},
	}

	// Run repeatedly: Go randomises map iteration, so a single run could pass
	// against an implementation that ranges over the map.
	want := []SourceID{SourceJellyfin, SourceNetflix, SourceDisney}
	for i := 0; i < 20; i++ {
		got := configuredSources(available)
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
	}
}

func TestConfiguredSourcesOmitsUnregistered(t *testing.T) {
	available := map[SourceID]MovieSource{SourceJellyfin: &fakeSource{id: SourceJellyfin}}

	got := configuredSources(available)

	if len(got) != 1 || got[0] != SourceJellyfin {
		t.Fatalf("got %v, want [jellyfin]", got)
	}
}

// baseConfig is the minimum a Server needs to start for enumeration tests.
func baseConfig() Config {
	return Config{
		JellyfinURL:        "http://jellyfin.example",
		JellyfinAPIKey:     "key",
		Port:               "8080",
		PublicURL:          "http://localhost:8080",
		SessionTTL:         "4h",
		StreamingProviders: defaultStreamingProviders,
	}
}

// Without a TMDB read token the streaming sources must not be registered at
// all, so they are never advertised to clients.
func TestServerEnumeratesJellyfinOnlyWithoutTMDBToken(t *testing.T) {
	cfg := baseConfig()
	cfg.CacheDir = t.TempDir()

	got := configuredSources(New(cfg).sources)

	if len(got) != 1 || got[0] != SourceJellyfin {
		t.Fatalf("got %v, want [jellyfin]", got)
	}
}

// With a token, the resolved StreamingProviders list decides what is offered.
func TestServerEnumeratesConfiguredProvidersWithTMDBToken(t *testing.T) {
	cfg := baseConfig()
	cfg.CacheDir = t.TempDir()
	cfg.TMDBReadToken = "token"

	got := configuredSources(New(cfg).sources)

	want := []SourceID{SourceJellyfin, SourceNetflix, SourcePrime, SourceDisney}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// A narrowed STREAMING_PROVIDERS list removes the others from enumeration.
func TestServerEnumerationHonoursStreamingProviders(t *testing.T) {
	cfg := baseConfig()
	cfg.CacheDir = t.TempDir()
	cfg.TMDBReadToken = "token"
	cfg.StreamingProviders = []SourceID{SourceDisney}

	got := configuredSources(New(cfg).sources)

	want := []SourceID{SourceJellyfin, SourceDisney}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// With no Jellyfin library, the empty-selection fallback must pick the first
// configured source rather than returning nothing to deal.
func TestSelectSourcesFallsBackToFirstConfiguredWithoutJellyfin(t *testing.T) {
	available := map[SourceID]MovieSource{
		SourceDisney: &fakeSource{id: SourceDisney},
		SourcePrime:  &fakeSource{id: SourcePrime},
	}

	got := selectSources(available, nil)

	if len(got) != 1 || got[0].ID() != SourcePrime {
		t.Fatalf("got %v, want [prime]", got)
	}
}

// Jellyfin still wins the fallback when this deployment has it.
func TestSelectSourcesFallbackPrefersJellyfin(t *testing.T) {
	available := map[SourceID]MovieSource{
		SourceDisney:   &fakeSource{id: SourceDisney},
		SourceJellyfin: &fakeSource{id: SourceJellyfin},
	}

	got := selectSources(available, []SourceID{"hulu"})

	if len(got) != 1 || got[0].ID() != SourceJellyfin {
		t.Fatalf("got %v, want [jellyfin]", got)
	}
}

// With nothing configured there is no fallback to make.
func TestSelectSourcesReturnsNoneWhenNothingConfigured(t *testing.T) {
	got := selectSources(map[SourceID]MovieSource{}, []SourceID{SourceJellyfin})

	if len(got) != 0 {
		t.Fatalf("got %v, want none", got)
	}
}

// Jellyfin without credentials must not be registered, so a fresh install
// advertises no source at all.
func TestServerRegistersNoSourcesWhenNothingConfigured(t *testing.T) {
	cfg := Config{Port: "8080", SessionTTL: "4h", CacheDir: t.TempDir(),
		StreamingProviders: defaultStreamingProviders}

	got := configuredSources(New(cfg).sources)

	if len(got) != 0 {
		t.Fatalf("got %v, want none", got)
	}
}
