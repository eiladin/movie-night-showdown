import { vi } from 'vitest'
import type { AvailableFilters, SetupStatus } from '../api'

// Shared helpers for tests that need `fetch`. The frontend talks to the server
// only through the handful of endpoints in api.ts, so a small table keyed by
// path prefix is enough to stand the whole backend in.

export function jsonResponse(body: unknown, init: { ok?: boolean; status?: number } = {}): Response {
    const ok = init.ok ?? true
    return {
        ok,
        status: init.status ?? (ok ? 200 : 500),
        statusText: ok ? 'OK' : 'Internal Server Error',
        json: async () => body,
    } as Response
}

export const configuredSetup: SetupStatus = {
    configured: true,
    jellyfin: true,
    plex: false,
    streaming: true,
    sources: [
        { id: 'jellyfin', label: 'Jellyfin', supportsUnwatched: true },
        { id: 'netflix', label: 'Netflix' },
    ],
}

export const unconfiguredSetup: SetupStatus = {
    configured: false,
    jellyfin: false,
    plex: false,
    streaming: false,
    sources: [],
}

export function filtersResponse(over: Partial<AvailableFilters> = {}): AvailableFilters {
    return {
        genres: ['Action', 'Comedy'],
        officialRatings: ['PG', 'R'],
        sources: configuredSetup.sources,
        problems: [],
        streaming: true,
        ...over,
    }
}

type Handler = (url: string, init?: RequestInit) => Response | Promise<Response>

// installFetchMock replaces global fetch with a router over path prefixes. An
// unhandled path fails the test loudly rather than resolving to undefined,
// which would surface much later as an unrelated render error.
export function installFetchMock(routes: Record<string, Handler>) {
    const mock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = typeof input === 'string' ? input : input.toString()
        for (const [prefix, handler] of Object.entries(routes)) {
            if (url.startsWith(prefix)) return handler(url, init)
        }
        throw new Error(`unexpected fetch: ${url}`)
    })
    vi.stubGlobal('fetch', mock)
    return mock
}

// deferred exposes a promise's resolver, so a test can decide the order two
// in-flight responses land in.
export function deferred<T>() {
    let resolve!: (value: T) => void
    let reject!: (reason?: unknown) => void
    const promise = new Promise<T>((res, rej) => {
        resolve = res
        reject = rej
    })
    return { promise, resolve, reject }
}
