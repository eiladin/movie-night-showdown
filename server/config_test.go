package server

import (
	"strings"
	"testing"
)

func equalSourceIDs(a, b []SourceID) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestParseStreamingProviders(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []SourceID
	}{
		{"unset falls back to the default set", "", defaultStreamingProviders},
		{"whitespace only falls back too", "   ", defaultStreamingProviders},
		{"custom subset", "netflix,disney", []SourceID{SourceNetflix, SourceDisney}},
		{"single entry", "prime", []SourceID{SourcePrime}},
		{"whitespace and case are normalised", " Netflix , PRIME ", []SourceID{SourceNetflix, SourcePrime}},
		{"empty entries are skipped", "netflix,,,disney,", []SourceID{SourceNetflix, SourceDisney}},
		{"unknown entries are ignored", "netflix,hulu,disney", []SourceID{SourceNetflix, SourceDisney}},
		{"only unknown entries yields none", "hulu,peacock", []SourceID{}},
		{"duplicates collapse", "netflix,netflix", []SourceID{SourceNetflix}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseStreamingProviders(tc.raw)
			if !equalSourceIDs(got, tc.want) {
				t.Fatalf("parseStreamingProviders(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// The config's String form is written to the log at startup, so it must never
// contain the token itself.
func TestConfigStringMasksTMDBToken(t *testing.T) {
	cfg := Config{TMDBReadToken: "super-secret-token", StreamingProviders: defaultStreamingProviders}

	s := cfg.String()

	if strings.Contains(s, "super-secret-token") {
		t.Fatalf("config String leaked the TMDB token: %s", s)
	}
	if !strings.Contains(s, "netflix,prime,disney") {
		t.Fatalf("config String omitted the resolved providers: %s", s)
	}
}
