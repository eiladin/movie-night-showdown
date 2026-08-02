// SourceID mirrors server.SourceID (see server/source.go).
//
// The set is open, not a union: streaming sources are whatever TMDB watch
// providers the deployment configured, so the frontend cannot know their ids
// ahead of time. Never narrow this to a literal union — a deployment offering
// Hulu or Starz would stop type-checking, and the picker is server-driven
// precisely so it does not need to know.
export type SourceID = string

// SourceDescriptor mirrors server.SourceDescriptor. The label is carried
// alongside the id because the frontend holds no table of provider names.
export interface SourceDescriptor {
    id: SourceID
    label: string
}

// Availability mirrors server.Availability's JSON shape. label is the display
// name of the service; it may be absent on older payloads, so render the id as
// a fallback.
export interface Availability {
    source: SourceID
    label?: string
}

// Movie mirrors server.Movie's JSON shape (see server/jellyfin.go).
export interface Movie {
    id: string
    title: string
    year: number
    genres: string[]
    overview: string
    runtime: number
    communityRating: number
    officialRating: string
    posterURL: string
    availability: Availability[]
}

// PreviewFilters mirrors the query params server.ParseFilters understands
// (see server/filters.go).
export interface PreviewFilters {
    genres?: string[]
    yearMin?: number
    yearMax?: number
    ratingMin?: number
    officialRatings?: string[]
    unwatched?: boolean
    libraryId?: string
    sources?: SourceID[]
}

export interface PreviewResponse {
    count: number
    movies: Movie[]
    unavailable: SourceID[]
}

function buildPreviewParams(filters: PreviewFilters): URLSearchParams {
    const params = new URLSearchParams()
    for (const genre of filters.genres ?? []) {
        params.append('genres', genre)
    }
    if (filters.yearMin) params.set('yearMin', String(filters.yearMin))
    if (filters.yearMax) params.set('yearMax', String(filters.yearMax))
    if (filters.ratingMin) params.set('ratingMin', String(filters.ratingMin))
    for (const rating of filters.officialRatings ?? []) {
        params.append('officialRatings', rating)
    }
    if (filters.unwatched) params.set('unwatched', 'true')
    if (filters.libraryId) params.set('libraryId', filters.libraryId)
    for (const source of filters.sources ?? []) {
        params.append('sources', source)
    }
    return params
}

// getPreview asks the server to query Jellyfin with the given filters and
// returns the matching count plus a capped list of movies for thumbnails.
export async function getPreview(filters: PreviewFilters): Promise<PreviewResponse> {
    const params = buildPreviewParams(filters)
    const res = await fetch(`/api/library/preview?${params.toString()}`)
    if (!res.ok) {
        throw new Error(`preview request failed: ${res.status} ${res.statusText}`)
    }
    return res.json() as Promise<PreviewResponse>
}

// warmLibrary asks the server to pre-fetch every poster for the filtered
// library into its cache before the session starts. Returns the candidate
// count; warming happens in the background server-side.
export async function warmLibrary(filters: PreviewFilters): Promise<number> {
    const params = buildPreviewParams(filters)
    const res = await fetch(`/api/library/warm?${params.toString()}`, { method: 'POST' })
    if (!res.ok) {
        throw new Error(`warm request failed: ${res.status} ${res.statusText}`)
    }
    const body = (await res.json()) as { count: number }
    return body.count
}

export interface AvailableFilters {
    genres: string[]
    officialRatings: string[]
    // sources lists the movie sources this deployment has credentials for,
    // with their display names, in the order they should be offered. A source
    // absent here cannot be selected — it would be dropped silently.
    sources: SourceDescriptor[]
}

export async function getAvailableFilters(): Promise<AvailableFilters> {
    const res = await fetch('/api/library/filters')
    if (!res.ok) {
        throw new Error(`filters request failed: ${res.status} ${res.statusText}`)
    }
    return res.json() as Promise<AvailableFilters>
}

// SetupStatus mirrors server.setupResponse (see server/setup.go). It reports
// only what this deployment is able to do; it never carries a credential.
export interface SetupStatus {
    // configured is false when no source can be queried at all, which is the
    // state of a fresh install.
    configured: boolean
    jellyfin: boolean
    streaming: boolean
    sources: SourceDescriptor[]
}

export async function getSetupStatus(): Promise<SetupStatus> {
    const res = await fetch('/api/setup')
    if (!res.ok) {
        throw new Error(`setup request failed: ${res.status} ${res.statusText}`)
    }
    return res.json() as Promise<SetupStatus>
}

// CreateSessionResponse mirrors server.createSessionResponse (see
// server/session.go).
export interface CreateSessionResponse {
    code: string
    joinURL: string
    participantId: string
    token: string
}

// createSession starts a new session with the given host name. The host
// becomes participant #1; the caller is responsible for persisting the
// returned token (see SessionSocket.setToken in ws.ts) before connecting.
export async function createSession(hostName: string): Promise<CreateSessionResponse> {
    const res = await fetch('/api/sessions', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ hostName: hostName }),
    })
    if (!res.ok) {
        throw new Error(`create session failed: ${res.status} ${res.statusText}`)
    }
    return res.json() as Promise<CreateSessionResponse>
}
