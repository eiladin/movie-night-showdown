package server

import (
	"strings"
	"testing"
)

func equalStrings(a, b []string) bool {
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
		want []string
	}{
		{"unset falls back to the default set", "", defaultStreamingProviders},
		{"whitespace only falls back too", "   ", defaultStreamingProviders},
		{"custom subset", "netflix,disney", []string{"netflix", "disney"}},
		{"single entry", "prime", []string{"prime"}},
		{"whitespace and case are normalised", " Netflix , PRIME ", []string{"netflix", "prime"}},
		{"empty entries are skipped", "netflix,,,disney,", []string{"netflix", "disney"}},
		{"duplicates collapse", "netflix,netflix", []string{"netflix"}},
		// Entries outside the built-in table are kept: they are resolved
		// against TMDB later, not judged here.
		{"unfamiliar names survive parsing", "hulu,peacock", []string{"hulu", "peacock"}},
		{"numeric ids survive parsing", "15, 386", []string{"15", "386"}},
		{"configured order is preserved", "disney,netflix", []string{"disney", "netflix"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseStreamingProviders(tc.raw)
			if !equalStrings(got, tc.want) {
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
