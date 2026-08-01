package server

import (
	"fmt"
	"log"
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
	// StreamingProviders is the resolved list of streaming sources this
	// deployment offers. It is inert without
	// TMDBReadToken, since every streaming source is queried through TMDB.
	StreamingProviders []SourceID
}

// defaultStreamingProviders is the set offered when STREAMING_PROVIDERS is
// unset, preserving the behaviour deployments had before it existed.
var defaultStreamingProviders = []SourceID{SourceNetflix, SourcePrime, SourceDisney}

// parseStreamingProviders reads the comma-separated STREAMING_PROVIDERS value.
// Entries are trimmed and matched case-insensitively; empty entries are
// skipped. An unset (or whitespace-only) value yields the default set. Unknown
// names are logged and ignored rather than failing startup, so a typo degrades
// the picker instead of taking the deployment down.
func parseStreamingProviders(raw string) []SourceID {
	if strings.TrimSpace(raw) == "" {
		return defaultStreamingProviders
	}
	known := map[string]SourceID{
		string(SourceNetflix): SourceNetflix,
		string(SourcePrime):   SourcePrime,
		string(SourceDisney):  SourceDisney,
	}
	seen := make(map[SourceID]bool, len(known))
	out := make([]SourceID, 0, len(known))
	for _, part := range strings.Split(raw, ",") {
		name := strings.ToLower(strings.TrimSpace(part))
		if name == "" {
			continue
		}
		id, ok := known[name]
		if !ok {
			log.Printf("config: ignoring unknown STREAMING_PROVIDERS entry %q", name)
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
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

		StreamingProviders: parseStreamingProviders(os.Getenv("STREAMING_PROVIDERS")),
	}
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
	providers := make([]string, 0, len(c.StreamingProviders))
	for _, id := range c.StreamingProviders {
		providers = append(providers, string(id))
	}
	return fmt.Sprintf(
		"JellyfinURL=%s JellyfinAPIKey=%s JellyfinUserID=%s PublicURL=%s Port=%s SessionTTL=%s CacheDir=%s TMDBReadToken=%s StreamingProviders=%s",
		c.JellyfinURL, masked, c.JellyfinUserID, c.PublicURL, c.Port, c.SessionTTL, c.CacheDir, maskedTMDB,
		strings.Join(providers, ","),
	)
}
