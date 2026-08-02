import { useEffect, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { getAvailableFilters, getPreview, warmLibrary, type AvailableFilters, type PreviewFilters, type PreviewResponse, type SourceDescriptor, type SourceID } from '../api'
import { useFiltersFor, useSessionStore } from '../store'
import { accentStyle } from '../sourceColor'
import '../styles/admin.css'

const RATING_ORDER: Record<string, number> = {
    'G': 10,
    'TV-Y': 11,
    'TV-Y7': 12,
    'TV-G': 13,
    'PG': 20,
    'TV-PG': 21,
    'PG-13': 30,
    'TV-14': 31,
    'R': 40,
    'TV-MA': 41,
    'NC-17': 50,
    'X': 51,
    'NR': 99,
    'UR': 100,
}

function sortRatings(ratings: string[]): string[] {
    return [...ratings].sort((a, b) => {
        const valA = RATING_ORDER[a.toUpperCase()] ?? 1000
        const valB = RATING_ORDER[b.toUpperCase()] ?? 1000
        if (valA !== valB) return valA - valB
        return a.localeCompare(b)
    })
}

function sortGenres(genres: string[]): string[] {
    return [...genres].sort((a, b) => a.localeCompare(b))
}

// HostSetup is the host's filter + library-preview page. The host arrives
// here with a session already created (the code comes from the ?code= query
// param), picks filters, previews the matching library, and proceeds to the
// lobby.
//
// The form is keyed by session code. Navigating from one session's setup to
// another's changes only the query param, so React Router keeps this component
// mounted and the useState initializers below never re-run — the previous
// session's picks would stay on screen. The key forces a remount instead.
export default function HostSetup() {
    const [searchParams] = useSearchParams()
    const sessionCode = searchParams.get('code')
    return <HostSetupForm key={sessionCode ?? ''} sessionCode={sessionCode} />
}

function HostSetupForm({ sessionCode }: { sessionCode: string | null }) {
    const setFilters = useSessionStore((s) => s.setFilters)
    // Seed from what was last chosen *for this session*, so arriving here via
    // "Change filters" shows the previous selection while a newly created
    // session starts blank rather than inheriting the last one's picks.
    // Only read on the first render; these are uncontrolled from here on.
    const saved = useFiltersFor(sessionCode)

    const [genres, setGenres] = useState<string[]>(saved.genres ?? [])
    const [yearMin, setYearMin] = useState(saved.yearMin ? String(saved.yearMin) : '')
    const [yearMax, setYearMax] = useState(saved.yearMax ? String(saved.yearMax) : '')
    const [officialRatings, setOfficialRatings] = useState<string[]>(saved.officialRatings ?? [])
    const [unwatched, setUnwatched] = useState(saved.unwatched ?? false)
    // Starts empty rather than assuming Jellyfin: which sources exist is the
    // server's answer, and it reconciles this below once it arrives.
    const [sources, setSources] = useState<SourceID[]>(saved.sources ?? [])
    const [preview, setPreview] = useState<PreviewResponse | null>(null)
    const [available, setAvailable] = useState<AvailableFilters | null>(null)
    // Distinguishes "no answer yet" from "answered, and the answer was an
    // error". Keying availability off `available === null` alone conflates the
    // two and leaves every source selectable forever when the fetch fails.
    const [sourcesLoaded, setSourcesLoaded] = useState(false)
    const [loading, setLoading] = useState(false)
    const [error, setError] = useState<string | null>(null)

    useEffect(() => {
        getAvailableFilters()
            .then((f) => {
                const configured = f.sources ?? []
                setAvailable({
                    genres: sortGenres(f.genres),
                    officialRatings: sortRatings(f.officialRatings),
                    sources: configured,
                })
                // Reconcile any selection made while the answer was in flight.
                // A source the server cannot query must not survive in state:
                // it would ship in host:start and be dropped silently, and it
                // renders no chip, so the host could never clear it.
                //
                // Falling back to the first configured source rather than to
                // Jellyfin: a streaming-only deployment has no Jellyfin to
                // select, and a hardcoded fallback would leave the picker
                // holding a source the server cannot query.
                const configuredIds = configured.map((s) => s.id)
                setSources((current) => {
                    const allowed = current.filter((id) => configuredIds.includes(id))
                    if (allowed.length > 0) return allowed
                    return configuredIds.length > 0 ? [configuredIds[0]] : []
                })
                setSourcesLoaded(true)
            })
            .catch((err) => {
                console.error('Failed to load available filters:', err)
                setError('Could not load filter options.')
                // The question has been answered, badly. Leave the selection
                // alone rather than asserting a source on no information; the
                // server falls back to its first configured source.
                setSources([])
                setSourcesLoaded(true)
            })
    }, [])

    function toggleGenre(genre: string) {
        setGenres((prev) => (prev.includes(genre) ? prev.filter((g) => g !== genre) : [...prev, genre]))
    }

    function toggleRating(rating: string) {
        setOfficialRatings((prev) => (prev.includes(rating) ? prev.filter((r) => r !== rating) : [...prev, rating]))
    }

    // Render exactly what the server reported, and nothing before it answers.
    // An unconfigured source never appears at all, rather than appearing greyed
    // out, and no chip flashes on the way to a deployment that lacks it.
    const offeredSources: SourceDescriptor[] = sourcesLoaded && available ? available.sources : []

    // Nothing beyond the local library is on offer, so point the host at what
    // would unlock the rest.
    const streamingUnavailable =
        sourcesLoaded && available !== null && offeredSources.every((s) => s.id === 'jellyfin')

    // sourceLabel names a source the server reported, falling back to its id.
    // Ids are not always readable — a provider configured by number resolves to
    // something like "tmdb-1899" — so prefer the label wherever one exists.
    function sourceLabel(id: SourceID): string {
        return offeredSources.find((s) => s.id === id)?.label ?? id
    }

    // At least one source must stay selected: a session with no source has no
    // deck to deal.
    function toggleSource(id: SourceID) {
        setSources((current) => {
            if (!current.includes(id)) return [...current, id]
            if (current.length === 1) return current
            return current.filter((s) => s !== id)
        })
    }

    function currentFilters(): PreviewFilters {
        return {
            sources,
            genres,
            yearMin: yearMin ? Number(yearMin) : undefined,
            yearMax: yearMax ? Number(yearMax) : undefined,
            officialRatings: officialRatings.length > 0 ? officialRatings : undefined,
            unwatched: sources.includes('jellyfin') ? unwatched : false,
        }
    }

    async function handlePreview() {
        setLoading(true)
        setError(null)
        try {
            const result = await getPreview(currentFilters())
            setPreview(result)
        } catch (err) {
            setError(err instanceof Error ? err.message : 'Preview failed')
            setPreview(null)
        } finally {
            setLoading(false)
        }
    }

    // Carry the chosen filters over to the Lobby, where "Begin" sends them in
    // host:start. Filters live in the shared session store rather than the
    // URL since they can include an arbitrary list of genres, and are stored
    // under this session's code so they never reach a different session.
    function handleGoToLobby() {
        if (sessionCode) setFilters(sessionCode, currentFilters())
        // Warm the poster cache during the lobby-fill window (fire-and-forget;
        // must never block entering the lobby).
        warmLibrary(currentFilters()).catch((err) =>
            console.error('Failed to warm poster cache:', err),
        )
    }

    return (
        <div className="admin-setup">
            <h1>Start a Showdown</h1>

            {sessionCode && (
                <p className="admin-session-banner">
                    Session <strong>{sessionCode}</strong> created
                </p>
            )}

            <fieldset className="chip-group source-picker">
                <legend>Sources</legend>
                {offeredSources.map((s) => (
                    <label
                        key={s.id}
                        className={`chip source-chip ${sources.includes(s.id) ? 'checked' : ''}`}
                        style={accentStyle(s.id)}
                    >
                        <input
                            type="checkbox"
                            className="sr-only"
                            checked={sources.includes(s.id)}
                            onChange={() => toggleSource(s.id)}
                        />
                        {s.label}
                    </label>
                ))}
                {streamingUnavailable && (
                    <p className="source-hint">
                        Add a{' '}
                        <a href="https://www.themoviedb.org/settings/api" target="_blank" rel="noreferrer">
                            TMDB API read token
                        </a>{' '}
                        to offer streaming services as sources.
                    </p>
                )}
            </fieldset>

            {available && available.genres.length > 0 && (
                <fieldset className="chip-group">
                    <legend>Genres</legend>
                    {available.genres.map((genre) => (
                        <label key={genre} className={`chip ${genres.includes(genre) ? 'checked' : ''}`}>
                            <input
                                type="checkbox"
                                className="sr-only"
                                checked={genres.includes(genre)}
                                onChange={() => toggleGenre(genre)}
                            />
                            {genre}
                        </label>
                    ))}
                </fieldset>
            )}

            <div className="year-range">
                <label>
                    Year from
                    <input type="number" value={yearMin} onChange={(e) => setYearMin(e.target.value)} placeholder="1970" />
                </label>
                <label>
                    Year to
                    <input type="number" value={yearMax} onChange={(e) => setYearMax(e.target.value)} placeholder="2026" />
                </label>
            </div>

            {available && available.officialRatings.length > 0 && (
                <fieldset className="chip-group">
                    <legend>Parental Rating</legend>
                    {available.officialRatings.map((rating) => (
                        <label key={rating} className={`chip ${officialRatings.includes(rating) ? 'checked' : ''}`}>
                            <input
                                type="checkbox"
                                className="sr-only"
                                checked={officialRatings.includes(rating)}
                                onChange={() => toggleRating(rating)}
                            />
                            {rating}
                        </label>
                    ))}
                </fieldset>
            )}

            <label className={`unwatched-toggle${sources.includes('jellyfin') ? '' : ' disabled'}`}>
                <input
                    type="checkbox"
                    checked={unwatched && sources.includes('jellyfin')}
                    disabled={!sources.includes('jellyfin')}
                    onChange={(e) => setUnwatched(e.target.checked)}
                />
                Unwatched only
                {!sources.includes('jellyfin') && <span className="hint"> (Jellyfin only)</span>}
            </label>

            {sessionCode && (
                <Link to={`/join/${sessionCode}`} onClick={handleGoToLobby} className="btn btn-primary">
                    Go to the Lobby →
                </Link>
            )}

            <button type="button" onClick={handlePreview} disabled={loading}>
                {loading ? 'Loading…' : 'Preview'}
            </button>

            {error && <p className="preview-error">{error}</p>}

            {preview && (
                <div className="preview-results">
                    {(preview.unavailable?.length ?? 0) > 0 && (
                        <p className="preview-unavailable">
                            Could not reach: {preview.unavailable.map(sourceLabel).join(', ')}. Those
                            results are missing.
                        </p>
                    )}
                    <p className="preview-count">
                        {preview.count >= 150 ? `showing ${preview.count} of many` : `${preview.count} movies match`}
                    </p>
                    <div className="poster-grid">
                        {preview.movies.map((movie) => (
                            <img
                                key={movie.id}
                                src={movie.posterURL}
                                alt={movie.title}
                                title={`${movie.title} (${movie.year})`}
                                loading="lazy"
                            />
                        ))}
                    </div>
                </div>
            )}
        </div>
    )
}
