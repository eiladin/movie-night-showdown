package server

import (
	"encoding/json"
	"log"
	"net/http"
)

// libraryPreviewResponse is the JSON body of GET /api/library/preview.
// Unavailable names the selected sources that failed this query, so the host
// can correct the problem before creating a room.
type libraryPreviewResponse struct {
	Count       int        `json:"count"`
	Movies      []Movie    `json:"movies"`
	Unavailable []SourceID `json:"unavailable"`
}

// handleLibraryPreview lets the host preview the filtered Jellyfin library
// (count + a capped list of movies for poster thumbnails) before starting a
// session.
func (s *Server) handleLibraryPreview(w http.ResponseWriter, r *http.Request) {
	filters := ParseFilters(r.URL.Query())

	sources := selectSources(s.sources, filters.Sources)
	movies, failed, err := gatherShoe(r.Context(), sources, filters)
	if err != nil {
		log.Printf("library preview: %v", err)
		http.Error(w, "failed to query any selected source", http.StatusBadGateway)
		return
	}
	for _, f := range failed {
		log.Printf("library preview: source %s unavailable", f)
	}
	if failed == nil {
		failed = []SourceID{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(libraryPreviewResponse{
		Count:       len(movies),
		Movies:      movies,
		Unavailable: failed,
	})
}

// libraryFiltersResponse is the JSON body of GET /api/library/filters: the
// filter values present in the Jellyfin library, plus which movie sources this
// deployment has credentials for. AvailableFilters is embedded, so the JSON
// keeps its existing shape and only gains "sources".
type libraryFiltersResponse struct {
	AvailableFilters
	Sources []SourceID `json:"sources"`
}

// handleLibraryFilters returns the available filter options (genres, ratings).
// With a Jellyfin library they are enumerated from it, so the picker offers
// exactly what is on the shelf. Without one — a streaming-only deployment —
// they fall back to the default vocabulary, since a TMDB catalog is far too
// large to enumerate and the picker would otherwise be empty.
func (s *Server) handleLibraryFilters(w http.ResponseWriter, r *http.Request) {
	var filters AvailableFilters
	if s.cfg.JellyfinConfigured() {
		var err error
		filters, err = s.jellyfin.GetAvailableFilters(r.Context())
		if err != nil {
			log.Printf("library filters: %v", err)
			http.Error(w, "failed to fetch available filters from Jellyfin", http.StatusBadGateway)
			return
		}
	} else {
		filters = defaultAvailableFilters()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(libraryFiltersResponse{
		AvailableFilters: filters,
		Sources:          configuredSources(s.sources),
	})
}

// handleLibraryWarm pre-fetches every poster for the filtered library into the
// on-disk cache so the session starts warm. It returns the candidate count
// immediately and warms in the background.
func (s *Server) handleLibraryWarm(w http.ResponseWriter, r *http.Request) {
	filters := ParseFilters(r.URL.Query())

	sources := selectSources(s.sources, filters.Sources)
	movies, _, err := gatherShoe(r.Context(), sources, filters)
	if err != nil {
		log.Printf("library warm: %v", err)
		http.Error(w, "failed to query any selected source", http.StatusBadGateway)
		return
	}

	if s.cache.enabled() {
		go s.cache.warm(movies, s.fetchers)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int{"count": len(movies)})
}
