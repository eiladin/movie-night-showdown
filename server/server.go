package server

import (
	"context"
	"log"
	"net/http"
	"time"
)

// providerResolveTimeout bounds the one TMDB call made at startup to resolve
// provider names. Startup must not hang on a slow upstream; on timeout the
// built-in table is all that resolves.
const providerResolveTimeout = 10 * time.Second

type Server struct {
	mux      *http.ServeMux
	cfg      Config
	jellyfin *JellyfinClient
	store    *Store
	cache    *posterCache
	fetchers map[SourceID]PosterFetcher
	sources  map[SourceID]MovieSource
	// order is this deployment's canonical source order: Jellyfin first when
	// present, then the streaming services in the order they were configured.
	// It is per-deployment rather than a fixed list because which streaming
	// services exist is resolved at startup from STREAMING_PROVIDERS.
	order   []SourceID
	version string
	commit  string
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
	// Jellyfin is gated on its credentials exactly like the streaming sources.
	// Registering it unconditionally would advertise a source every query fails
	// against, which is what a streaming-only deployment would otherwise show.
	s.fetchers = map[SourceID]PosterFetcher{}
	s.sources = map[SourceID]MovieSource{}
	if cfg.JellyfinConfigured() {
		s.fetchers[SourceJellyfin] = s.jellyfin
		s.sources[SourceJellyfin] = s.jellyfin
		s.order = append(s.order, SourceJellyfin)
	}
	// Resolution needs the network for anything outside the built-in table, so
	// it is bounded and non-fatal: whatever resolves is offered, and the rest
	// is logged. It is skipped entirely without a read token, so an unset
	// TMDB_READ_TOKEN never advertises a streaming source at all.
	ctx, cancel := context.WithTimeout(context.Background(), providerResolveTimeout)
	defer cancel()
	for _, p := range resolveStreamingProviders(ctx, cfg, cfg.StreamingProviders) {
		src := NewTMDBSource(cfg, p)
		if src == nil {
			continue
		}
		if _, clash := s.sources[p.ID]; clash {
			log.Printf("server: skipping duplicate source %q", p.ID)
			continue
		}
		s.sources[p.ID] = src
		s.fetchers[p.ID] = src
		s.order = append(s.order, p.ID)
	}
	if len(s.sources) == 0 {
		log.Print("server: no movie source is configured — set JELLYFIN_URL and " +
			"JELLYFIN_API_KEY for a local library, and/or TMDB_READ_TOKEN for " +
			"streaming services. Open the app for setup instructions.")
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
	s.mux.HandleFunc("GET /api/setup", s.handleSetup)
	s.mux.HandleFunc("GET /api/library/preview", s.handleLibraryPreview)
	s.mux.HandleFunc("GET /api/library/filters", s.handleLibraryFilters)
	s.mux.HandleFunc("POST /api/library/warm", s.handleLibraryWarm)
	s.mux.HandleFunc("GET /api/images/{source}/{id}", s.handleImage)
	s.mux.HandleFunc("POST /api/sessions", s.handleCreateSession)
	s.mux.HandleFunc("GET /ws", s.handleWS)
	// static handler is registered in main.go via SetStatic (needs the embed.FS)
}
