package server

import (
	"encoding/json"
	"net/http"
)

// setupResponse is the JSON body of GET /api/setup. It reports only whether
// each half of the configuration is usable, never any credential — the values
// of JELLYFIN_API_KEY and TMDB_READ_TOKEN must not reach a browser.
type setupResponse struct {
	// Configured is false when no source at all can be queried, which is the
	// state of a fresh install. The frontend sends the host to the setup guide
	// rather than to a picker with nothing in it.
	Configured bool `json:"configured"`
	// Jellyfin reports whether a local library is available.
	Jellyfin bool `json:"jellyfin"`
	// Streaming reports whether any streaming service is available.
	Streaming bool `json:"streaming"`
	// Sources is the resolved source list, the same one the picker renders.
	Sources []SourceDescriptor `json:"sources"`
}

// handleSetup reports what this deployment is able to do, so a fresh install
// can explain itself instead of failing at the first query.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	sources := configuredSources(s.sources, s.order)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(setupResponse{
		Configured: len(sources) > 0,
		Jellyfin:   s.cfg.JellyfinConfigured(),
		Streaming:  s.cfg.StreamingConfigured(),
		Sources:    sources,
	})
}
