import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import HostSetup from './HostSetup'
import { useSessionStore } from '../store'
import { deferred, filtersResponse, jsonResponse } from '../test/fetchMock'
import type { AvailableFilters } from '../api'

// HostSetup refetches the filter vocabulary whenever the source selection
// changes. These tests drive that effect directly: every /api/library/filters
// call is recorded and left pending until the test decides what it answers
// with and in which order, which is the only way to exercise the guard against
// an out-of-order reply.

interface PendingCall {
    url: string
    sources: string[]
    resolve: (body: AvailableFilters) => void
    reject: (err: unknown) => void
}

let calls: PendingCall[] = []

function installDeferredFilters() {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
        const url = typeof input === 'string' ? input : input.toString()
        if (!url.startsWith('/api/library/filters')) {
            throw new Error(`unexpected fetch: ${url}`)
        }
        const query = new URLSearchParams(url.split('?')[1] ?? '')
        const d = deferred<AvailableFilters>()
        calls.push({
            url,
            sources: query.getAll('sources'),
            resolve: (body) => d.resolve(body),
            reject: (err) => d.reject(err),
        })
        return d.promise.then((body) => jsonResponse(body))
    })
    vi.stubGlobal('fetch', fetchMock)
}

function renderHostSetup() {
    return render(
        <MemoryRouter initialEntries={['/host?code=ABCD']}>
            <HostSetup />
        </MemoryRouter>,
    )
}

// settle answers the initial (unparameterized) fetch and the follow-up fetch
// that the reconciled selection triggers, leaving the component with Jellyfin
// selected and a known vocabulary.
async function settleInitialLoad(over: Partial<AvailableFilters> = {}) {
    await waitFor(() => expect(calls).toHaveLength(1))
    calls[0].resolve(filtersResponse(over))
    await waitFor(() => expect(calls).toHaveLength(2))
    expect(calls[1].sources).toEqual(['jellyfin'])
    calls[1].resolve(filtersResponse(over))
    await screen.findByRole('group', { name: /genres/i })
}

beforeEach(() => {
    calls = []
    localStorage.clear()
    useSessionStore.setState({ filtersByCode: {} })
    installDeferredFilters()
})

afterEach(() => {
    cleanup()
})

describe('vocabulary refetch on source change', () => {
    it('requests the vocabulary for the newly selected sources', async () => {
        const user = userEvent.setup()
        renderHostSetup()
        await settleInitialLoad()

        await user.click(screen.getByRole('checkbox', { name: 'Netflix' }))

        await waitFor(() => expect(calls).toHaveLength(3))
        expect(calls[2].sources).toEqual(['jellyfin', 'netflix'])
    })

    it('ignores a slow answer for an earlier selection', async () => {
        const user = userEvent.setup()
        renderHostSetup()
        await settleInitialLoad()

        // Selection 2: Jellyfin + Netflix. Left pending.
        await user.click(screen.getByRole('checkbox', { name: 'Netflix' }))
        await waitFor(() => expect(calls).toHaveLength(3))
        const stale = calls[2]
        expect(stale.sources).toEqual(['jellyfin', 'netflix'])

        // Selection 3: Netflix alone. Also pending.
        await user.click(screen.getByRole('checkbox', { name: 'Jellyfin' }))
        await waitFor(() => expect(calls).toHaveLength(4))
        const current = calls[3]
        expect(current.sources).toEqual(['netflix'])

        // The newer answer lands first, then the older one.
        current.resolve(filtersResponse({ genres: ['Documentary'] }))
        expect(await screen.findByRole('checkbox', { name: 'Documentary' })).toBeInTheDocument()

        stale.resolve(filtersResponse({ genres: ['Western'] }))
        await Promise.resolve()

        await waitFor(() => {
            expect(screen.getByRole('checkbox', { name: 'Documentary' })).toBeInTheDocument()
        })
        expect(screen.queryByRole('checkbox', { name: 'Western' })).not.toBeInTheDocument()
    })

    it('drops a selected genre the new vocabulary no longer offers', async () => {
        const user = userEvent.setup()
        renderHostSetup()
        await settleInitialLoad()

        await user.click(screen.getByRole('checkbox', { name: 'Action' }))
        expect(screen.getByRole('checkbox', { name: 'Action' })).toBeChecked()

        await user.click(screen.getByRole('checkbox', { name: 'Netflix' }))
        await waitFor(() => expect(calls).toHaveLength(3))
        calls[2].resolve(filtersResponse({ genres: ['Comedy'] }))

        await waitFor(() => {
            expect(screen.queryByRole('checkbox', { name: 'Action' })).not.toBeInTheDocument()
        })
        // The pick is gone from state too, not merely unrendered: the lobby
        // must not ship a genre no selected source understands.
        await user.click(screen.getByRole('link', { name: /go to the lobby/i }))
        expect(useSessionStore.getState().filtersByCode['ABCD'].filters.genres).toEqual([])
    })
})

describe('failure reporting', () => {
    it('says a source rejected the credentials rather than only naming it', async () => {
        renderHostSetup()
        await settleInitialLoad({
            problems: [{ label: 'Jellyfin', source: 'jellyfin', reason: 'unauthorized' }],
        })

        expect(await screen.findByText(/jellyfin rejected the credentials/i)).toBeInTheDocument()
    })

    it('distinguishes an unreachable host from a rejected credential', async () => {
        renderHostSetup()
        await settleInitialLoad({
            problems: [{ label: 'Plex — Films', source: 'plex-3', reason: 'unreachable' }],
        })

        expect(await screen.findByText(/plex — films could not be reached/i)).toBeInTheDocument()
    })

    // A library configured by a name the server could not resolve registers no
    // source at all, so nothing else on the screen would ever mention it.
    it('reports a library that could not be resolved, and keeps the page usable', async () => {
        renderHostSetup()
        await settleInitialLoad({
            problems: [{ label: 'Jellyfin — Kids Movies', reason: 'unresolved' }],
        })

        expect(await screen.findByText(/jellyfin — kids movies could not be found/i)).toBeInTheDocument()
        expect(screen.getByRole('group', { name: /genres/i })).toBeInTheDocument()
    })

    it('explains a failed vocabulary request instead of showing empty pickers', async () => {
        const user = userEvent.setup()
        renderHostSetup()
        await settleInitialLoad()

        await user.click(screen.getByRole('checkbox', { name: 'Netflix' }))
        await waitFor(() => expect(calls).toHaveLength(3))
        calls[2].reject(new Error('boom'))

        expect(
            await screen.findByText(/could not load filter options for the selected sources/i),
        ).toBeInTheDocument()
        expect(screen.queryByRole('group', { name: /genres/i })).not.toBeInTheDocument()
    })

    it('explains a failed initial request', async () => {
        renderHostSetup()
        await waitFor(() => expect(calls).toHaveLength(1))
        calls[0].reject(new Error('boom'))

        expect(await screen.findByText(/could not load filter options\./i)).toBeInTheDocument()
        expect(screen.queryByRole('checkbox', { name: 'Jellyfin' })).not.toBeInTheDocument()
    })
})
