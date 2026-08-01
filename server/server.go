package server

import (
	"log"
	"net/http"
	"time"
)

type Server struct {
	mux      *http.ServeMux
	cfg      Config
	jellyfin *JellyfinClient
	store    *Store
	cache    *posterCache
	fetchers map[SourceID]PosterFetcher
	sources  map[SourceID]MovieSource
	version  string
	commit   string
}

func New(cfg Config) *Server {
	ttl, err := time.ParseDuration(cfg.SessionTTL)
	if err != nil {
		log.Printf("server: invalid SESSION_TTL %q, defaulting to 4h: %v", cfg.SessionTTL, err)
		ttl = 4 * time.Hour
	}

	s := &Server{
		mux:      http.NewServeMux(),
		cfg:      cfg,
		jellyfin: NewJellyfinClient(cfg),
		store:    NewStore(ttl),
		cache:    newPosterCache(cfg.CacheDir),
	}
	s.fetchers = map[SourceID]PosterFetcher{
		SourceJellyfin: s.jellyfin,
	}
	s.sources = map[SourceID]MovieSource{SourceJellyfin: s.jellyfin}
	// NewTMDBSource returns nil without a read token, so an unset
	// TMDB_READ_TOKEN leaves s.sources Jellyfin-only and the streaming sources
	// are never advertised to clients.
	for _, id := range cfg.StreamingProviders {
		if src := NewTMDBSource(cfg, id); src != nil {
			s.sources[id] = src
			s.fetchers[id] = src
		}
	}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

// SetBuildInfo records the version and commit baked in at build time.
func (s *Server) SetBuildInfo(version, commit string) {
	s.version = version
	s.commit = commit
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("GET /api/library/preview", s.handleLibraryPreview)
	s.mux.HandleFunc("GET /api/library/filters", s.handleLibraryFilters)
	s.mux.HandleFunc("POST /api/library/warm", s.handleLibraryWarm)
	s.mux.HandleFunc("GET /api/images/{source}/{id}", s.handleImage)
	s.mux.HandleFunc("POST /api/sessions", s.handleCreateSession)
	s.mux.HandleFunc("GET /ws", s.handleWS)
	// static handler is registered in main.go via SetStatic (needs the embed.FS)
}
