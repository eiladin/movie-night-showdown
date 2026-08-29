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
    // supportsUnwatched reports whether this source can answer "unwatched
    // only" — it needs a per-user watch state, which a local library has and a
    // streaming catalogue does not. Optional because older servers omit it;
    // absent is read as "not supported", so the capability has to be declared
    // rather than inferred from the source id.
    supportsUnwatched?: boolean
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

// SourceProblem is one source this deployment could not use, and why.
//
// reason is machine-readable and the client owns the wording (see
// components/SourceProblems.tsx). It is typed as a union plus string so an
// older client keeps compiling against a server that adds a reason.
//
// source is absent for a library configured by a name the server could not
// resolve: such a library deliberately has no SourceID, so the label is all
// there is to name it by.
export interface SourceProblem {
    label: string
    source?: SourceID
    reason: 'unauthorized' | 'unreachable' | 'unresolved' | string
}

export interface PreviewResponse {
    count: number
    movies: Movie[]
    problems: SourceProblem[]
}

// Exported for tests: the query-param encoding is the contract with
// server.ParseFilters and is worth pinning independently of the fetch calls.
export function buildPreviewParams(filters: PreviewFilters): URLSearchParams {
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
    // problems lists selected sources whose vocabulary could not be fetched,
    // and why, plus any library configured by a name the server could not
    // resolve. The response is still usable — it holds the union of whatever
    // did answer — so this is a completeness warning, not an error.
    problems: SourceProblem[]
    // streaming reports whether the deployment has a TMDB token at all. It
    // cannot be derived from `sources`: a deployment with no streaming
    // services configured and one with no token look identical from there.
    // Optional because older servers omit it.
    streaming?: boolean
}

// Exported for tests: the filters endpoint takes the same repeated `sources`
// encoding as the preview endpoint, and that contract is worth pinning.
export function buildFiltersParams(sources: SourceID[]): URLSearchParams {
    const params = new URLSearchParams()
    for (const source of sources) {
        params.append('sources', source)
    }
    return params
}

// getAvailableFilters reports the filter vocabulary offered by the given
// sources (their union). An empty list asks for the server's default, which is
// its first configured source.
export async function getAvailableFilters(
    sources: SourceID[],
    signal?: AbortSignal,
): Promise<AvailableFilters> {
    const params = buildFiltersParams(sources)
    const query = params.toString()
    const res = await fetch(`/api/library/filters${query ? `?${query}` : ''}`, { signal })
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
    plex: boolean
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

// --- Settings ---
//
// Every settings endpoint requires the setup token, which the server generates
// on first start and prints to its log. See setupToken.ts for how it is held.

// SettingsProvenance says where one setting's live value came from, mirroring
// server.provenanceView. `source` is a plain string rather than a union: an
// unrecognized origin must render as text, not break the screen.
export interface SettingsProvenance {
    source: string
    envVar?: string
    // envIgnored is the case worth surfacing: an environment variable exists
    // and the config file has overridden it, so the operator may believe a
    // value is in effect when it is not.
    envIgnored?: boolean
}

// Settings mirrors server.settingsResponse. Secrets are never present — only a
// boolean saying whether one is stored, so the screen can render a placeholder.
// RuntimeSettings are fixed at container level. They are reported so the screen
// can show what is in effect; there is no way to change them from here, and no
// corresponding fields on SettingsUpdate.
export interface RuntimeSettings {
    port: string
    cacheDir: string
    configPath: string
}

export interface Settings {
    publicUrl: string
    sessionTtl: string
    runtime: RuntimeSettings
    jellyfin: {
        enabled: boolean
        url: string
        apiKeySet: boolean
        userId: string
        libraries: LibraryOption[]
    }
    plex: {
        enabled: boolean
        url: string
        tokenSet: boolean
        librarySection: string
        libraries: LibraryOption[]
    }
    streaming: {
        enabled: boolean
        tmdbReadTokenSet: boolean
        watchRegion: string
        providers: string[]
    }
    provenance: Record<string, SettingsProvenance>
    // outcome is what a save did: 'no_change', 'applied', or
    // 'restart_required'. It is a plain string for the same reason `source` is.
    outcome?: string
    restartRequired: boolean
}

// SettingsUpdate mirrors server.settingsRequest. Every field is optional, and
// an omitted field means "leave this alone" — which is what keeps a stored
// credential alive when an unrelated field is edited. Clearing one is an
// explicit flag.
export interface SettingsUpdate {
    publicUrl?: string
    sessionTtl?: string
    jellyfin?: {
        enabled?: boolean
        url?: string
        apiKey?: string
        clearApiKey?: boolean
        userId?: string
        clearUserId?: boolean
        // The chosen libraries, as id and name pairs. Both are sent because the
        // screen has both — it enumerated them to render the picker — and storing
        // the name means a later start has nothing to resolve. An empty list means
        // every library, which is a choice; omitting the field means unchanged.
        libraries?: LibraryOption[]
    }
    plex?: {
        enabled?: boolean
        url?: string
        token?: string
        clearToken?: boolean
        librarySection?: string
        libraries?: LibraryOption[]
    }
    streaming?: {
        enabled?: boolean
        tmdbReadToken?: string
        clearTmdbReadToken?: boolean
        watchRegion?: string
        providers?: string[]
    }
}

// SettingsAuthError is thrown when the setup token is missing or wrong, so the
// screen can ask for one instead of showing a generic failure.
export class SettingsAuthError extends Error {
    constructor() {
        super('a valid setup token is required')
        this.name = 'SettingsAuthError'
    }
}

// ValidationError carries the server's field-keyed messages so each one can be
// attached to the input that caused it.
export class ValidationError extends Error {
    readonly fields: Record<string, string>
    constructor(fields: Record<string, string>) {
        super('the settings were rejected')
        this.name = 'ValidationError'
        this.fields = fields
    }
}

function authHeaders(token: string): HeadersInit {
    return { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }
}

export async function getSettings(token: string): Promise<Settings> {
    const res = await fetch('/api/settings', { headers: authHeaders(token) })
    if (res.status === 401) throw new SettingsAuthError()
    if (!res.ok) throw new Error(`settings request failed: ${res.status} ${res.statusText}`)
    return res.json() as Promise<Settings>
}

export async function saveSettings(token: string, update: SettingsUpdate): Promise<Settings> {
    const res = await fetch('/api/settings', {
        method: 'POST',
        headers: authHeaders(token),
        body: JSON.stringify(update),
    })
    if (res.status === 401) throw new SettingsAuthError()
    if (res.status === 400) {
        const body = (await res.json()) as { errors?: Record<string, string> }
        throw new ValidationError(body.errors ?? {})
    }
    if (!res.ok) throw new Error(`settings save failed: ${res.status} ${res.statusText}`)
    return res.json() as Promise<Settings>
}

export interface TmdbVerification {
    valid: boolean
    message?: string
}

// verifyTmdbToken checks a candidate token before it is saved. The candidate
// travels in the body, never the query string.
export async function verifyTmdbToken(
    token: string,
    candidate: string,
    region: string,
): Promise<TmdbVerification> {
    const res = await fetch('/api/settings/verify/tmdb', {
        method: 'POST',
        headers: authHeaders(token),
        body: JSON.stringify({ token: candidate, region }),
    })
    if (res.status === 401) throw new SettingsAuthError()
    if (!res.ok) throw new Error(`verification failed: ${res.status} ${res.statusText}`)
    return res.json() as Promise<TmdbVerification>
}

// ProviderOption is one selectable streaming service. Its id is the same slug
// the source list uses, so a selection can be saved directly.
export interface ProviderOption {
    id: string
    name: string
}

export interface ProviderList {
    region: string
    providers: ProviderOption[]
}

// listProviders fetches the services offered in a region. The candidate token
// is optional: it lets the picker populate from a token that has been verified
// but not yet saved.
//
// A failure carries the server's own explanation. Swallowing it made an
// unreachable TMDB, a rejected token, and a region that genuinely lists nothing
// all render as an empty picker, which is the least actionable of the three.
export async function listProviders(
    token: string,
    region: string,
    candidate?: string,
): Promise<ProviderList> {
    const res = await fetch('/api/settings/providers', {
        method: 'POST',
        headers: authHeaders(token),
        body: JSON.stringify({ region, token: candidate ?? '' }),
    })
    if (res.status === 401) throw new SettingsAuthError()
    if (!res.ok) {
        const body = (await res.json().catch(() => ({}))) as { message?: string }
        throw new Error(body.message ?? `provider list failed: ${res.status} ${res.statusText}`)
    }
    return res.json() as Promise<ProviderList>
}

// SourceCheckRequest mirrors server.checkRequest. Every field is optional and
// falls back to what the server has stored, because the settings screen never
// receives a stored credential and so cannot submit one for a source that is
// already saved — which is the case most worth checking.
export interface SourceCheckRequest {
    url?: string
    secret?: string
    librarySection?: string
}

// LibraryOption is one selectable movie library, from either local service. The
// identifier is opaque — a numeric key for Plex, a hexadecimal id for Jellyfin — so
// it is only ever chosen from a list the server enumerated, never typed.
export interface LibraryOption {
    id: string
    name: string
}

// SourceCheck mirrors server.verifySourceResponse. `message` is populated on
// success as well as failure: the movie count or the library name is the
// confirmation the button exists to give.
export interface SourceCheck {
    valid: boolean
    message?: string
    // libraries is present on a check that reached the server. It is the only way
    // to offer a library as a choice rather than something to be transcribed, and
    // it arrives on a failing check too — an unknown configured library is exactly
    // when the available ones are needed.
    libraries?: LibraryOption[]
}

async function checkSource(
    token: string,
    path: string,
    req: SourceCheckRequest,
): Promise<SourceCheck> {
    const res = await fetch(path, {
        method: 'POST',
        headers: authHeaders(token),
        body: JSON.stringify(req),
    })
    if (res.status === 401) throw new SettingsAuthError()
    if (!res.ok) throw new Error(`check failed: ${res.status} ${res.statusText}`)
    return res.json() as Promise<SourceCheck>
}

export function verifyJellyfin(token: string, req: SourceCheckRequest): Promise<SourceCheck> {
    return checkSource(token, '/api/settings/verify/jellyfin', req)
}

export function verifyPlex(token: string, req: SourceCheckRequest): Promise<SourceCheck> {
    return checkSource(token, '/api/settings/verify/plex', req)
}

// JellyfinUser is one selectable Jellyfin account.
export interface JellyfinUser {
    id: string
    name: string
}

// listJellyfinUsers reads the accounts on a Jellyfin server so the user id
// behind the "unwatched only" filter can be chosen rather than transcribed. A
// mistyped id is never rejected at query time — it just returns nothing.
export async function listJellyfinUsers(
    token: string,
    req: SourceCheckRequest,
): Promise<JellyfinUser[]> {
    const res = await fetch('/api/settings/jellyfin/users', {
        method: 'POST',
        headers: authHeaders(token),
        body: JSON.stringify(req),
    })
    if (res.status === 401) throw new SettingsAuthError()
    if (!res.ok) throw new Error(`user list failed: ${res.status} ${res.statusText}`)
    const body = (await res.json()) as { users?: JellyfinUser[] }
    return body.users ?? []
}
