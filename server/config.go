package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config holds server configuration loaded from environment variables.
type Config struct {
	JellyfinURL    string
	JellyfinAPIKey string
	JellyfinUserID string
	PublicURL      string
	Port           string
	SessionTTL     string
	CacheDir       string
	TMDBReadToken  string
	// TMDBWatchRegion is the ISO 3166-1 region streaming availability is
	// judged against. Which services exist, and what they carry, both depend
	// on it.
	TMDBWatchRegion string
	// StreamingProviders is the list of streaming services this deployment
	// asks for, as written in the environment: provider names or numeric TMDB
	// provider ids, normalized but not yet resolved. Resolution needs the
	// network, so it happens in New rather than here. The list is inert
	// without TMDBReadToken, since every streaming source is queried via TMDB.
	StreamingProviders []string

	// tmdbBaseURL is the TMDB API root. It is unexported and not read from the
	// environment: it exists only so tests can point resolution at a stub.
	tmdbBaseURL string
}

// defaultStreamingProviders is the set offered when STREAMING_PROVIDERS is
// unset, preserving the behaviour deployments had before it existed.
var defaultStreamingProviders = []string{
	string(SourceNetflix), string(SourcePrime), string(SourceDisney),
}

// parseStreamingProviders reads the comma-separated STREAMING_PROVIDERS value.
// Entries are trimmed, lowercased, and de-duplicated; empty entries are
// skipped. An unset (or whitespace-only) value yields the default set.
//
// Entries are deliberately not validated here. Any TMDB watch provider may be
// named, and knowing which names exist requires asking TMDB, so an unrecognized
// entry is reported (and skipped) during resolution rather than at parse time.
func parseStreamingProviders(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return defaultStreamingProviders
	}
	seen := make(map[string]bool)
	out := make([]string, 0, 4)
	for _, part := range strings.Split(raw, ",") {
		name := strings.ToLower(strings.TrimSpace(part))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// LoadConfig reads configuration from environment variables, applying
// defaults for the optional ones.
func LoadConfig() Config {
	cfg := Config{
		JellyfinURL:    os.Getenv("JELLYFIN_URL"),
		JellyfinAPIKey: os.Getenv("JELLYFIN_API_KEY"),
		JellyfinUserID: os.Getenv("JELLYFIN_USER_ID"),
		PublicURL:      os.Getenv("PUBLIC_URL"),
		Port:           os.Getenv("PORT"),
		SessionTTL:     os.Getenv("SESSION_TTL"),
		CacheDir:       os.Getenv("CACHE_DIR"),
		TMDBReadToken:  os.Getenv("TMDB_READ_TOKEN"),

		TMDBWatchRegion:    os.Getenv("TMDB_WATCH_REGION"),
		StreamingProviders: parseStreamingProviders(os.Getenv("STREAMING_PROVIDERS")),
		tmdbBaseURL:        tmdbAPIBase,
	}
	if cfg.TMDBWatchRegion == "" {
		cfg.TMDBWatchRegion = defaultWatchRegion
	}
	cfg.TMDBWatchRegion = strings.ToUpper(cfg.TMDBWatchRegion)
	if cfg.Port == "" {
		cfg.Port = "8080"
	}
	if cfg.PublicURL == "" {
		cfg.PublicURL = "http://localhost:8080"
	}
	if cfg.SessionTTL == "" {
		cfg.SessionTTL = "4h"
	}
	if cfg.CacheDir == "" {
		cfg.CacheDir = filepath.Join(os.TempDir(), "mns-posters")
	}
	return cfg
}

// JellyfinConfigured reports whether this deployment can query Jellyfin. Both
// values are needed: a URL without a key cannot authenticate, and a key without
// a URL has nowhere to go.
func (c Config) JellyfinConfigured() bool {
	return c.JellyfinURL != "" && c.JellyfinAPIKey != ""
}

// StreamingConfigured reports whether this deployment can query any streaming
// service. Every streaming source goes through TMDB, so the token is required;
// STREAMING_PROVIDERS can also narrow the list to nothing.
func (c Config) StreamingConfigured() bool {
	return c.TMDBReadToken != "" && len(c.StreamingProviders) > 0
}

// String renders the config for logging with the API key masked.
func (c Config) String() string {
	masked := "(unset)"
	if c.JellyfinAPIKey != "" {
		masked = "***"
	}
	maskedTMDB := "(unset)"
	if c.TMDBReadToken != "" {
		maskedTMDB = "***"
	}
	return fmt.Sprintf(
		"JellyfinURL=%s JellyfinAPIKey=%s JellyfinUserID=%s PublicURL=%s Port=%s SessionTTL=%s CacheDir=%s TMDBReadToken=%s TMDBWatchRegion=%s StreamingProviders=%s",
		c.JellyfinURL, masked, c.JellyfinUserID, c.PublicURL, c.Port, c.SessionTTL, c.CacheDir, maskedTMDB,
		c.TMDBWatchRegion, strings.Join(c.StreamingProviders, ","),
	)
}
