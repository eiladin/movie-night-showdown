package server

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// vocabFakeSource is a source that reports a fixed vocabulary, standing in for
// a real upstream where only the merge behaviour matters.
type vocabFakeSource struct {
	fakeSource
	vocab AvailableFilters
	err   error
}

func (f *vocabFakeSource) Vocabulary(context.Context) (AvailableFilters, error) {
	return f.vocab, f.err
}

func TestGatherVocabularyUnionsSources(t *testing.T) {
	a := &vocabFakeSource{
		fakeSource: fakeSource{id: SourceJellyfin},
		vocab: AvailableFilters{
			Genres:          []string{"Action", "Film Noir"},
			OfficialRatings: []string{"PG", "R"},
		},
	}
	b := &vocabFakeSource{
		fakeSource: fakeSource{id: SourceNetflix},
		vocab: AvailableFilters{
			Genres:          []string{"Action", "Western"},
			OfficialRatings: []string{"R", "NC-17"},
		},
	}

	got, failed, err := gatherVocabulary(context.Background(), []MovieSource{a, b})
	if err != nil {
		t.Fatalf("gatherVocabulary: %v", err)
	}
	if len(failed) != 0 {
		t.Errorf("failed = %v, want none", failed)
	}
	// A value only one source recognizes is still offered: "Film Noir" is
	// library-specific and "Western" is streaming-only.
	want := []string{"Action", "Film Noir", "Western"}
	if !slices.Equal(got.Genres, want) {
		t.Errorf("genres = %v, want %v", got.Genres, want)
	}
	wantRatings := []string{"PG", "R", "NC-17"}
	if !slices.Equal(got.OfficialRatings, wantRatings) {
		t.Errorf("ratings = %v, want %v", got.OfficialRatings, wantRatings)
	}
}

// The first source in canonical order names each genre, and Jellyfin leads that
// order, so the library's own label survives. "Sci-Fi" and "Science Fiction"
// are the same TMDB genre and must not both be offered.
func TestGatherVocabularyPrefersTheFirstNameForAGenre(t *testing.T) {
	jf := &vocabFakeSource{
		fakeSource: fakeSource{id: SourceJellyfin},
		vocab:      AvailableFilters{Genres: []string{"Sci-Fi"}},
	}
	stream := &vocabFakeSource{
		fakeSource: fakeSource{id: SourceNetflix},
		vocab:      AvailableFilters{Genres: []string{"Science Fiction"}},
	}

	got, _, err := gatherVocabulary(context.Background(), []MovieSource{jf, stream})
	if err != nil {
		t.Fatalf("gatherVocabulary: %v", err)
	}
	if !slices.Equal(got.Genres, []string{"Sci-Fi"}) {
		t.Errorf("genres = %v, want [Sci-Fi]", got.Genres)
	}
}

// Partial failure degrades: one source's values are better than an empty
// picker, and the caller is told which source is missing.
func TestGatherVocabularyReportsPartialFailure(t *testing.T) {
	ok := &vocabFakeSource{
		fakeSource: fakeSource{id: SourceJellyfin},
		vocab:      AvailableFilters{Genres: []string{"Action"}},
	}
	bad := &vocabFakeSource{
		fakeSource: fakeSource{id: SourceNetflix},
		err:        errors.New("upstream down"),
	}

	got, failed, err := gatherVocabulary(context.Background(), []MovieSource{ok, bad})
	if err != nil {
		t.Fatalf("gatherVocabulary: %v", err)
	}
	if !slices.Equal(got.Genres, []string{"Action"}) {
		t.Errorf("genres = %v, want [Action]", got.Genres)
	}
	if len(failed) != 1 || failed[0].Source != SourceNetflix {
		t.Errorf("failed = %v, want [netflix]", failed)
	}
}

func TestGatherVocabularyErrorsWhenEverySourceFails(t *testing.T) {
	bad := &vocabFakeSource{
		fakeSource: fakeSource{id: SourceNetflix},
		err:        errors.New("upstream down"),
	}
	_, failed, err := gatherVocabulary(context.Background(), []MovieSource{bad})
	if !errors.Is(err, errAllSourcesFailed) {
		t.Errorf("err = %v, want errAllSourcesFailed", err)
	}
	if len(failed) != 1 || failed[0].Source != SourceNetflix {
		t.Errorf("failed = %v, want [netflix]", failed)
	}
}

// A source with no vocabulary contributes nothing but is not a failure: it has
// no values to offer, which is different from being unreachable.
func TestGatherVocabularySkipsSourcesWithoutOne(t *testing.T) {
	plain := &fakeSource{id: SourceNetflix}
	withVocab := &vocabFakeSource{
		fakeSource: fakeSource{id: SourceJellyfin},
		vocab:      AvailableFilters{Genres: []string{"Action"}},
	}

	got, failed, err := gatherVocabulary(context.Background(), []MovieSource{withVocab, plain})
	if err != nil {
		t.Fatalf("gatherVocabulary: %v", err)
	}
	if len(failed) != 0 {
		t.Errorf("failed = %v, want none", failed)
	}
	if !slices.Equal(got.Genres, []string{"Action"}) {
		t.Errorf("genres = %v, want [Action]", got.Genres)
	}
}

// The whole point of the rework: the vocabulary follows the host's selection,
// not the deployment's configuration.
func TestGatherVocabularyFollowsTheSelection(t *testing.T) {
	jf := &vocabFakeSource{
		fakeSource: fakeSource{id: SourceJellyfin},
		vocab:      AvailableFilters{Genres: []string{"Film Noir"}},
	}
	nf := &vocabFakeSource{
		fakeSource: fakeSource{id: SourceNetflix},
		vocab:      AvailableFilters{Genres: []string{"Western"}},
	}
	available := map[SourceID]MovieSource{SourceJellyfin: jf, SourceNetflix: nf}

	selected := selectSources(available, []SourceID{SourceNetflix}, testOrder)
	got, _, err := gatherVocabulary(context.Background(), selected)
	if err != nil {
		t.Fatalf("gatherVocabulary: %v", err)
	}
	if !slices.Equal(got.Genres, []string{"Western"}) {
		t.Errorf("genres = %v, want [Western] (the deselected library must not leak in)", got.Genres)
	}
}

func TestCanonicalGenreKey(t *testing.T) {
	if canonicalGenreKey("Sci-Fi") != canonicalGenreKey("Science Fiction") {
		t.Error("names sharing a TMDB genre id must share a key")
	}
	if canonicalGenreKey("Action") == canonicalGenreKey("Comedy") {
		t.Error("distinct genres must not share a key")
	}
	// A library-specific genre TMDB does not know keys on its own name, so it
	// survives the merge instead of colliding with everything else unknown.
	if canonicalGenreKey("Film Noir") != "Film Noir" {
		t.Error("an unrecognized genre must key on its own name")
	}
	if canonicalGenreKey("Film Noir") == canonicalGenreKey("Holiday") {
		t.Error("distinct unrecognized genres must not share a key")
	}
}
