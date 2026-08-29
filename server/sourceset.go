package server

import (
	"context"
	"log"
	"time"
)

// sourceSet is the set of movie sources a deployment currently offers, held as
// one value so it can be replaced atomically.
//
// It is immutable once built. A configuration change produces a new set and
// swaps the pointer; nothing ever mutates the maps in place. That is what lets
// readers work without a lock: a request loads the pointer once and holds a
// consistent view for its whole duration, even while a reload is happening.
type sourceSet struct {
	sources  map[SourceID]MovieSource
	fetchers map[SourceID]PosterFetcher
	// order is this deployment's canonical source order: local libraries first
	// in alphabetical order, then the streaming services as configured.
	order []SourceID

	// pending are libraries configured by name rather than by identifier. A name
	// has to be turned into an identifier before anything can be queried, and that
	// needs the media server, so these are not sources yet.
	//
	// They are deliberately absent from the maps above. An unresolved name would
	// otherwise become a SourceID containing that name — spaces and all — which
	// breaks the URL-safety this application requires of a SourceID, since the id
	// is a path segment in the image proxy.
	pending []pendingLibrary
	// resolve turns this set into one with nothing pending. It is nil when there
	// is nothing to resolve, and each set gets its own so a configuration change
	// starts a fresh resolution rather than inheriting a stale answer.
	resolve   *lazyValue[*sourceSet]
	resolveFn func(context.Context) (*sourceSet, error)
}

// pendingLibrary is one library named rather than identified.
//
// serviceName is the service's display name ("Jellyfin", "Plex"), carried here
// because a pending library has no source object to ask for a label and nothing
// may derive a label by branching on a SourceID.
type pendingLibrary struct {
	service     SourceID
	serviceName string
	name        string
}

// currentSources returns the live source set.
//
// Call it once per request and use the returned value throughout. Loading it
// twice within one request can straddle a reload and yield two different
// configurations, which is how a poster proxy ends up looking for a fetcher
// that the search it is serving never used.
func (s *Server) currentSources() *sourceSet {
	set := s.sources.Load()
	if set == nil || set.resolve == nil || len(set.pending) == 0 {
		return set
	}

	// Resolve the named libraries now, on the first request that needs the source
	// list. Doing it at startup instead would mean an application and a media
	// server that start together can race: lose it and the process comes up with
	// no sources at all, when it could have come up holding the ones configured by
	// identifier and picked the rest up moments later.
	//
	// The context is bounded and detached from the caller. A request that is
	// abandoned mid-resolution must not cancel the work for everyone behind it,
	// and lazyValue deliberately does not record a cancellation as a failure.
	ctx, cancel := context.WithTimeout(context.Background(), libraryResolveTimeout)
	defer cancel()

	resolved, err := set.resolve.get(ctx, set.resolveFn)
	if err != nil {
		// Return what works. The sources configured by identifier are already
		// registered in this set, so a media server that is still starting costs
		// the named libraries and nothing else.
		//
		// The failure is not silent: the set still carries its pending libraries,
		// and every handler reports those through pendingProblems. A wrong API key
		// with libraries configured by name registers no source for that service,
		// and an empty picker with no explanation is the bug this exists to avoid.
		return set
	}

	// Swap, and swap only if nothing else has. A configuration save may have
	// replaced the set while this resolution was in flight, and that set is newer.
	//
	// Sessions are deliberately not ended here. applyConfig ends them because the
	// operator changed the configuration; this is the same configuration finally
	// becoming resolvable, and ending a movie night for that would be a bug
	// dressed as consistency. Do not "tidy" this into a call to applyConfig.
	s.sources.CompareAndSwap(set, resolved)
	return resolved
}

// libraryRefsOrAll turns a configured library list into the refs to build clients
// for.
//
// An empty list yields one zero ref, which is an unscoped source over every
// library on the server. That is what a deployment which has never chosen a
// library has always had, so upgrading changes nothing until somebody picks one.
// Reading it as "no libraries" instead would leave a configured service with
// nothing to query, which is not what anyone means by leaving a setting blank.
func libraryRefsOrAll(refs []libraryRef) []libraryRef {
	if len(refs) == 0 {
		return []libraryRef{{}}
	}
	return refs
}

// add registers one source under its own id, in canonical order.
//
// It refuses a duplicate rather than overwriting: two configured entries naming
// the same library would otherwise register the same source twice, deal the same
// movies twice into the shoe, and leave the picker with two identical chips.
func (set *sourceSet) add(src MovieSource) {
	id := src.ID()
	if _, clash := set.sources[id]; clash {
		log.Printf("server: skipping duplicate source %q", id)
		return
	}
	set.sources[id] = src
	if f, ok := src.(PosterFetcher); ok {
		set.fetchers[id] = f
	}
	set.order = append(set.order, id)
}

// buildSourceSet constructs the sources a configuration calls for.
//
// Startup and reload share this one path deliberately. Two construction paths
// drift, and the failure mode is a deployment whose behaviour depends on
// whether a setting was present at boot or saved later.
func buildSourceSet(cfg Config) *sourceSet {
	set := &sourceSet{
		sources:  map[SourceID]MovieSource{},
		fetchers: map[SourceID]PosterFetcher{},
	}

	addLocalSources(set, cfg)
	addStreamingSources(set, cfg)

	if len(set.sources) == 0 && len(set.pending) == 0 {
		log.Print("server: no movie source is configured — set JELLYFIN_URL and " +
			"JELLYFIN_API_KEY or PLEX_URL and PLEX_TOKEN for a local library, " +
			"and/or TMDB_READ_TOKEN for streaming services. Open the app for " +
			"setup instructions.")
	}
	if len(set.pending) > 0 {
		set.resolve = &lazyValue[*sourceSet]{}
		set.resolveFn = func(ctx context.Context) (*sourceSet, error) {
			return resolvePendingLibraries(ctx, cfg, set)
		}
	}
	return set
}

// addLocalSources registers one source per configured Jellyfin library and Plex
// section, recording any that is named rather than identified as pending.
//
// Each source is gated on its own credentials. Registering one unconditionally
// would advertise a source every query fails against.
func addLocalSources(set *sourceSet, cfg Config) {
	if cfg.JellyfinConfigured() {
		for _, ref := range libraryRefsOrAll(cfg.JellyfinLibraries) {
			if isPendingName(SourceJellyfin, ref) {
				set.pending = append(set.pending, pendingLibrary{SourceJellyfin, "Jellyfin", ref.ID})
				continue
			}
			set.add(NewJellyfinClient(cfg, ref))
		}
	}
	// Plex sits after Jellyfin: the canonical order decides whose genre names
	// win in the merged vocabulary (see gatherVocabulary), and Jellyfin's stay
	// canonical so adding Plex cannot relabel an existing deployment's picker.
	if cfg.PlexConfigured() {
		for _, ref := range libraryRefsOrAll(cfg.PlexLibraries) {
			if isPendingName(SourcePlex, ref) {
				set.pending = append(set.pending, pendingLibrary{SourcePlex, "Plex", ref.ID})
				continue
			}
			set.add(NewPlexClient(cfg, ref))
		}
	}
}

// addStreamingSources registers the configured streaming services.
func addStreamingSources(set *sourceSet, cfg Config) {
	// Resolution needs the network for anything outside the built-in table, so
	// it is bounded and non-fatal: whatever resolves is offered, and the rest
	// is logged. It is skipped entirely without a read token, so an unset TMDB
	// token never advertises a streaming source at all.
	ctx, cancel := context.WithTimeout(context.Background(), providerResolveTimeout)
	defer cancel()
	for _, p := range resolveStreamingProviders(ctx, cfg, cfg.StreamingProviders) {
		src := NewTMDBSource(cfg, p)
		if src == nil {
			continue
		}
		if _, clash := set.sources[p.ID]; clash {
			log.Printf("server: skipping duplicate source %q", p.ID)
			continue
		}
		set.sources[p.ID] = src
		set.fetchers[p.ID] = src
		set.order = append(set.order, p.ID)
	}
}

// reloadOutcome is what a saved configuration did to the running server.
type reloadOutcome string

const (
	// reloadNoChange means nothing that affects behaviour differed.
	reloadNoChange reloadOutcome = "no_change"
	// reloadApplied means the change took effect without a restart.
	reloadApplied reloadOutcome = "applied"
	// reloadRestartRequired means the change is persisted but cannot take
	// effect until the process restarts.
	reloadRestartRequired reloadOutcome = "restart_required"
)

// sourcesDiffer reports whether two configurations would produce different
// sources.
//
// Rebuilding ends every active session, so this must be true only when
// something genuinely source-affecting changed. It compares resolved values
// rather than file contents: a reordered or reformatted config file carrying
// identical values is not a change, and ending four people's movie night over
// a whitespace edit would be indefensible.
func sourcesDiffer(a, b Config) bool {
	if a.JellyfinURL != b.JellyfinURL ||
		a.JellyfinAPIKey != b.JellyfinAPIKey ||
		a.JellyfinUserID != b.JellyfinUserID {
		return true
	}
	if a.PlexURL != b.PlexURL ||
		a.PlexToken != b.PlexToken ||
		a.PlexLibrarySection != b.PlexLibrarySection {
		return true
	}
	// The library lists decide how many sources exist and what each one is called.
	// Omitting them here would have a save report a change the server never made.
	if !librariesEqual(a.JellyfinLibraries, b.JellyfinLibraries) ||
		!librariesEqual(a.PlexLibraries, b.PlexLibraries) {
		return true
	}
	if a.TMDBReadToken != b.TMDBReadToken ||
		a.TMDBWatchRegion != b.TMDBWatchRegion {
		return true
	}
	if len(a.StreamingProviders) != len(b.StreamingProviders) {
		return true
	}
	for i := range a.StreamingProviders {
		if a.StreamingProviders[i] != b.StreamingProviders[i] {
			return true
		}
	}
	return false
}

// librariesEqual compares two library lists element by element.
//
// Order matters: it decides the order sources are offered in, and the canonical
// order decides whose genre names win in the merged vocabulary. A reordered list
// is a different configuration, not the same one written differently.
//
// The name is compared as well as the identifier. It is only a label, but it is a
// label the picker shows, and a rebuild is what makes a corrected one visible.
func librariesEqual(a, b []libraryRef) bool {
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

// harmlessDiffer reports whether settings changed that can be applied without
// touching sources or sessions. Correcting a typo in the public URL must not
// end a movie night.
func harmlessDiffer(a, b Config) bool {
	return a.PublicURL != b.PublicURL || a.SessionTTL != b.SessionTTL
}

// restartRequiredDiffer reports whether settings changed that cannot take
// effect in a running process. The listener is already bound to a port and
// cannot be rebound under live connections.
//
// Only the port qualifies. The cache directory is environment-only and can
// never differ between two resolved configurations of one process.
func restartRequiredDiffer(a, b Config) bool {
	return a.Port != b.Port
}

// applyConfig makes a saved configuration live, returning what it did.
//
// The three tiers are handled separately because rebuilding sources ends every
// active session. A save that changed nothing source-affecting must not cost
// anyone their movie night, and a save that changed only the listen port cannot
// take effect at all until the process restarts.
func (s *Server) applyConfig(next Config) reloadOutcome {
	s.cfgMu.RLock()
	current := s.cfg
	s.cfgMu.RUnlock()

	sourcesChanged := sourcesDiffer(current, next)
	harmless := harmlessDiffer(current, next)
	needsRestart := restartRequiredDiffer(current, next)

	if !sourcesChanged && !harmless && !needsRestart {
		log.Print("config: saved, but nothing that affects behaviour changed")
		return reloadNoChange
	}

	if !sourcesChanged {
		// Harmless settings apply without touching sources, so no session is
		// disturbed. The port is stored for the next start either way.
		s.setConfig(next)
		if needsRestart {
			log.Print("config: applied; a restart is needed for the listen port or cache directory")
			return reloadRestartRequired
		}
		log.Print("config: applied without rebuilding sources")
		return reloadApplied
	}

	// Build before ending anything: a failure here must not leave the server
	// with no sessions and no sources.
	set := buildSourceSet(next)

	// End sessions before the swap. Swapping first would leave in-flight
	// requests holding a deck dealt from sources that no longer exist, and the
	// posters for those cards would start returning 404 mid-swipe.
	ended := s.store.EndAll(EndReasonReconfigured)

	s.setConfig(next)
	s.sources.Store(set)
	log.Printf("config: sources rebuilt; %d session(s) ended", ended)

	if needsRestart {
		return reloadRestartRequired
	}
	return reloadApplied
}

// setConfig replaces the live configuration and propagates the settings that
// live outside it. A value stored here but not pushed to its owner would be
// reported as applied while nothing had changed.
func (s *Server) setConfig(next Config) {
	s.cfgMu.Lock()
	s.cfg = next
	s.cfgMu.Unlock()

	if ttl, err := time.ParseDuration(next.SessionTTL); err == nil {
		s.store.SetTTL(ttl)
	} else {
		log.Printf("config: invalid session TTL %q, keeping the previous value: %v", next.SessionTTL, err)
	}
}

// config returns the live configuration.
func (s *Server) config() Config {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg
}
