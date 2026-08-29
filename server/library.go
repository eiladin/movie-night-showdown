package server

import (
	"encoding/json"
	"log"
	"net/http"
)

// libraryPreviewResponse is the JSON body of GET /api/library/preview.
// Problems names the selected sources that failed this query and why, so the
// host can correct the problem before creating a room. A source id alone said
// only that something was wrong, and a rejected credential and an unreachable
// host have different fixes.
type libraryPreviewResponse struct {
	Count    int             `json:"count"`
	Movies   []Movie         `json:"movies"`
	Problems []SourceProblem `json:"problems"`
}

// handleLibraryPreview lets the host preview the filtered Jellyfin library
// (count + a capped list of movies for poster thumbnails) before starting a
// session.
func (s *Server) handleLibraryPreview(w http.ResponseWriter, r *http.Request) {
	filters := ParseFilters(r.URL.Query())

	set := s.currentSources()
	sources := selectSources(set.sources, filters.Sources, set.order)

	// No source at all is not a query failure. It is what a deployment whose
	// libraries are all configured by unresolvable names has, and answering 502
	// with an empty body is how that came to look like nothing being wrong.
	// An empty preview plus the problems below says what actually happened.
	var movies []Movie
	var failed []SourceProblem
	if len(sources) > 0 {
		var err error
		movies, failed, err = gatherShoe(r.Context(), sources, filters)
		if err != nil {
			log.Printf("library preview: %v", err)
			http.Error(w, "failed to query any selected source", http.StatusBadGateway)
			return
		}
	}
	if movies == nil {
		movies = []Movie{}
	}
	problems := appendProblems(failed, set.pendingProblems())
	if len(problems) > 0 {
		log.Printf("library preview: unusable sources: %s", problemLabels(problems))
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(libraryPreviewResponse{
		Count:    len(movies),
		Movies:   movies,
		Problems: problems,
	})
}

// libraryFiltersResponse is the JSON body of GET /api/library/filters: the
// filter values the selected sources recognize, plus which movie sources this
// deployment has credentials for. AvailableFilters is embedded, so the JSON
// keeps its existing shape and only gains "sources" and "problems".
type libraryFiltersResponse struct {
	AvailableFilters
	Sources []SourceDescriptor `json:"sources"`
	// Problems names the selected sources whose vocabulary could not be
	// fetched and why, so the host can be told the picker is incomplete instead
	// of reading a short list as the truth. Libraries configured by a name that
	// could not be resolved appear here too: they register no source, so nothing
	// else would ever mention them.
	Problems []SourceProblem `json:"problems"`
	// Streaming reports whether this deployment has a TMDB token at all, so the
	// picker can offer the "add a token to unlock streaming" hint. It is not
	// derivable from the source list: an empty streaming set and an
	// unconfigured one look identical from there.
	Streaming bool `json:"streaming"`
}

// handleLibraryFilters returns the filter options (genres, ratings) offered for
// the sources named in the ?sources= query, unioned across them.
//
// The selection matters: the vocabulary must follow what the host actually
// picked, not what the deployment happens to have credentials for. Keying it on
// configuration instead meant a host who deselected Jellyfin still saw a picker
// enumerated from the library they had just excluded, and any library-specific
// genre in it returned nothing.
//
// An absent or unrecognized selection falls back through selectSources exactly
// as the deck does, so a client that sends no sources still gets a usable
// picker.
func (s *Server) handleLibraryFilters(w http.ResponseWriter, r *http.Request) {
	requested := ParseFilters(r.URL.Query()).Sources
	set := s.currentSources()
	sources := selectSources(set.sources, requested, set.order)

	// As in the preview: a deployment with no usable source still gets a working
	// picker and a list of problems, rather than a 502 that says nothing.
	filters := defaultAvailableFilters()
	var failed []SourceProblem
	if len(sources) > 0 {
		var err error
		filters, failed, err = gatherVocabulary(r.Context(), sources)
		if err != nil {
			log.Printf("library filters: %v", err)
			http.Error(w, "failed to fetch filter options from any selected source", http.StatusBadGateway)
			return
		}
	}
	problems := appendProblems(failed, set.pendingProblems())
	if len(problems) > 0 {
		log.Printf("library filters: unusable sources: %s", problemLabels(problems))
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(libraryFiltersResponse{
		AvailableFilters: filters,
		Sources:          configuredSources(set.sources, set.order),
		Problems:         problems,
		Streaming:        s.config().StreamingConfigured(),
	})
}

// handleLibraryWarm pre-fetches every poster for the filtered library into the
// on-disk cache so the session starts warm. It returns the candidate count
// immediately and warms in the background.
func (s *Server) handleLibraryWarm(w http.ResponseWriter, r *http.Request) {
	filters := ParseFilters(r.URL.Query())

	set := s.currentSources()
	sources := selectSources(set.sources, filters.Sources, set.order)
	movies, _, err := gatherShoe(r.Context(), sources, filters)
	if err != nil {
		log.Printf("library warm: %v", err)
		http.Error(w, "failed to query any selected source", http.StatusBadGateway)
		return
	}

	if s.cache.enabled() {
		go s.cache.warm(movies, set.fetchers)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int{"count": len(movies)})
}
