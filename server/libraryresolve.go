package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// libraryResolveTimeout bounds one round of name resolution. It is generous
// compared with a query timeout because this runs once and a media server that has
// just started can be slow to answer its first request.
const libraryResolveTimeout = 20 * time.Second

// jellyfinLibraryIDLength is the length of a Jellyfin library identifier in hex.
const jellyfinLibraryIDLength = 32

// isPendingName reports whether a configured library entry is a display name that
// still has to be resolved to an identifier.
//
// An entry from the config file always carries a real identifier — the settings
// screen enumerated the libraries to render its picker and writes both fields — so
// only an environment-supplied entry is ever pending.
//
// The test is per service because the two identifier formats differ, and an
// identifier is the thing that can be recognized on sight while a name is anything
// at all:
//
//   - A Plex section key is an integer.
//   - A Jellyfin library id is a 32-character hexadecimal string. No operator names
//     a library "f0e1d2c3b4a5968778695a4b3c2d1e0f", so treating that shape as an
//     identifier cannot misread a real name. The reverse — treating a name as an
//     identifier — is the failure this exists to prevent: it produced SourceIDs
//     containing spaces, which the image proxy path cannot carry.
func isPendingName(service SourceID, ref libraryRef) bool {
	if ref.ID == "" {
		// The unscoped source. Nothing to resolve.
		return false
	}
	if ref.Name != "" {
		// Both fields set means the identifier came from the config file, which
		// only ever writes resolved pairs.
		return false
	}
	switch service {
	case SourcePlex:
		_, err := strconv.Atoi(ref.ID)
		return err != nil
	case SourceJellyfin:
		return !looksLikeJellyfinLibraryID(ref.ID)
	default:
		return false
	}
}

func looksLikeJellyfinLibraryID(s string) bool {
	if len(s) != jellyfinLibraryIDLength {
		return false
	}
	for _, r := range s {
		hex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
		if !hex {
			return false
		}
	}
	return true
}

// resolvePendingLibraries turns a set's named libraries into identified ones and
// returns a set with nothing pending.
//
// It rebuilds only the local sources. The streaming sources are carried over from
// the set being resolved rather than resolved again, because that would repeat a
// TMDB round trip for an answer already held.
func resolvePendingLibraries(ctx context.Context, cfg Config, previous *sourceSet) (*sourceSet, error) {
	next := cfg

	jellyfin, jfUnmatched, jfErr := resolveLibraryNames(ctx, cfg, SourceJellyfin, cfg.JellyfinLibraries)
	plex, pxUnmatched, pxErr := resolveLibraryNames(ctx, cfg, SourcePlex, cfg.PlexLibraries)
	if err := errors.Join(jfErr, pxErr); err != nil {
		// A failure here is transient by assumption — the media server is starting,
		// or briefly unreachable — so it is reported and retried on the retry floor
		// rather than recorded as a permanent answer.
		return nil, err
	}
	next.JellyfinLibraries = jellyfin
	next.PlexLibraries = plex

	resolved := &sourceSet{
		sources:  map[SourceID]MovieSource{},
		fetchers: map[SourceID]PosterFetcher{},
	}
	addLocalSources(resolved, next)

	// Carry the streaming sources over in their existing order, so the canonical
	// order stays local-libraries-then-streaming. Anything addLocalSources just
	// produced is skipped rather than duplicated.
	for _, id := range previous.order {
		if _, alreadyBuilt := resolved.sources[id]; alreadyBuilt {
			continue
		}
		src, ok := previous.sources[id]
		if !ok {
			continue
		}
		resolved.sources[id] = src
		if f, ok := previous.fetchers[id]; ok {
			resolved.fetchers[id] = f
		}
		resolved.order = append(resolved.order, id)
	}

	if len(resolved.pending) > 0 {
		// Every name either resolved or came back in the unmatched list, so
		// addLocalSources should have found nothing left to defer. Reaching here
		// means the discriminator and the resolver disagree about what an
		// identifier looks like.
		return nil, fmt.Errorf("server: %d librar(ies) still pending after resolution", len(resolved.pending))
	}

	// A name the service does not have stays pending, which registers no source
	// for it and reports it as unresolved. Dropping it here instead would leave a
	// typo in a library name looking exactly like a library that is simply empty.
	// These never resolve on a retry, so the set keeps no resolve function.
	resolved.pending = append(pendingFor("Jellyfin", SourceJellyfin, jfUnmatched),
		pendingFor("Plex", SourcePlex, pxUnmatched)...)
	return resolved, nil
}

// pendingFor turns a service's unmatched library names into pending entries.
func pendingFor(serviceName string, service SourceID, names []string) []pendingLibrary {
	out := make([]pendingLibrary, 0, len(names))
	for _, n := range names {
		out = append(out, pendingLibrary{service: service, serviceName: serviceName, name: n})
	}
	return out
}

// resolveLibraryNames replaces every named entry in refs with the identified
// library it matches, leaving already-identified entries alone.
//
// An entry that cannot be matched is returned in the second result and registers
// no source, rather than being fatal: that mirrors how an unknown streaming
// provider entry behaves, and a typo in one library name should not cost a
// deployment the libraries that were spelled correctly. It is reported to the host
// as unresolved. The error return is reserved for a failure to enumerate at all,
// which is the transient case worth retrying.
func resolveLibraryNames(ctx context.Context, cfg Config, service SourceID, refs []libraryRef) ([]libraryRef, []string, error) {
	needed := false
	for _, ref := range refs {
		if isPendingName(service, ref) {
			needed = true
			break
		}
	}
	if !needed {
		return refs, nil, nil
	}

	available, err := enumerateLibraries(ctx, cfg, service)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: cannot list libraries to resolve names: %w", service, err)
	}

	// Index by lowercased name. Case is folded here, at the point of comparison,
	// and never on the stored value: the same configured list holds opaque
	// identifiers, and folding one of those corrupts it.
	//
	// Both Plex and Jellyfin permit two libraries with the same title, so the index
	// is built from a sorted list and the first entry keeps the name. That makes
	// the winner the same on every run even though the upstream order is not
	// guaranteed — the same rule providerCatalog uses for colliding provider slugs.
	sorted := make([]libraryRef, len(available))
	copy(sorted, available)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Name != sorted[j].Name {
			return sorted[i].Name < sorted[j].Name
		}
		return sorted[i].ID < sorted[j].ID
	})
	byName := make(map[string]libraryRef, len(sorted))
	for _, lib := range sorted {
		key := strings.ToLower(strings.TrimSpace(lib.Name))
		if key == "" {
			continue
		}
		if existing, clash := byName[key]; clash {
			log.Printf("%s: two libraries are named %q; using %s and ignoring %s",
				service, lib.Name, existing.ID, lib.ID)
			continue
		}
		byName[key] = lib
	}

	out := make([]libraryRef, 0, len(refs))
	unmatched := make([]string, 0)
	for _, ref := range refs {
		if !isPendingName(service, ref) {
			out = append(out, ref)
			continue
		}
		match, ok := byName[strings.ToLower(strings.TrimSpace(ref.ID))]
		if !ok {
			log.Printf("%s: no library named %q; it will be reported as unresolved", service, ref.ID)
			unmatched = append(unmatched, ref.ID)
			continue
		}
		out = append(out, match)
	}
	return out, unmatched, nil
}

// enumerateLibraries lists the movie libraries a service offers.
func enumerateLibraries(ctx context.Context, cfg Config, service SourceID) ([]libraryRef, error) {
	switch service {
	case SourceJellyfin:
		return NewJellyfinClient(cfg, libraryRef{}).Libraries(ctx)
	case SourcePlex:
		sections, err := NewPlexClient(cfg, libraryRef{}).movieSections(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]libraryRef, 0, len(sections))
		for _, d := range sections {
			out = append(out, libraryRef{ID: d.Key, Name: d.name()})
		}
		return out, nil
	default:
		return nil, fmt.Errorf("server: %s has no library enumeration", service)
	}
}

// jellyfinMediaFolders is the shape of GET /Library/MediaFolders.
type jellyfinMediaFolders struct {
	Items []struct {
		ID             string `json:"Id"`
		Name           string `json:"Name"`
		CollectionType string `json:"CollectionType"`
	} `json:"Items"`
}

// Libraries lists the server's movie libraries.
//
// Only folders whose CollectionType is "movies" are returned. A Jellyfin server
// also has music, shows and mixed folders, and offering one of those as a movie
// source would produce a source whose every query comes back empty.
func (c *JellyfinClient) Libraries(ctx context.Context) ([]libraryRef, error) {
	reqURL := c.baseURL + "/Library/MediaFolders"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Emby-Token", c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jellyfin: GET /Library/MediaFolders: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, statusFailure(resp.StatusCode, fmt.Errorf("jellyfin: GET /Library/MediaFolders returned %s", resp.Status))
	}

	var parsed jellyfinMediaFolders
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("jellyfin: decode /Library/MediaFolders response: %w", err)
	}

	out := make([]libraryRef, 0, len(parsed.Items))
	for _, item := range parsed.Items {
		if item.ID == "" || !strings.EqualFold(item.CollectionType, "movies") {
			continue
		}
		out = append(out, libraryRef{ID: item.ID, Name: item.Name})
	}
	return out, nil
}
