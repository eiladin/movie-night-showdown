import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useSessionStore } from './store'

const STORAGE_KEY = 'mns-filters'

function persisted(): { state?: Record<string, unknown> } {
    const raw = localStorage.getItem(STORAGE_KEY)
    return raw ? JSON.parse(raw) : {}
}

function setFilters(code: string, at: number) {
    vi.setSystemTime(at)
    useSessionStore.getState().setFilters(code, { genres: [code] })
}

beforeEach(() => {
    vi.useFakeTimers()
    localStorage.clear()
    useSessionStore.setState({ filtersByCode: {} })
    useSessionStore.getState().reset()
})

afterEach(() => {
    vi.useRealTimers()
})

describe('filter persistence', () => {
    it('stores a session’s filters under its code and reads them back', () => {
        useSessionStore.getState().setFilters('abcd', { genres: ['Action'], yearMin: 1990 })

        // Codes are normalized, so the lowercase code above is stored uppercase
        // and the two entry points (?code= and /join/:code) cannot disagree.
        const stored = useSessionStore.getState().filtersByCode['ABCD']
        expect(stored.filters).toEqual({ genres: ['Action'], yearMin: 1990 })
        expect(persisted().state?.filtersByCode).toHaveProperty('ABCD')
    })

    it('persists only filtersByCode', () => {
        useSessionStore.getState().setFilters('ABCD', { genres: ['Action'] })
        useSessionStore.getState().setDeck([
            {
                id: 'm1',
                title: 'A Movie',
                year: 2001,
                genres: [],
                overview: '',
                runtime: 90,
                communityRating: 7,
                officialRating: 'PG',
                posterURL: '',
                availability: [],
            },
        ])
        useSessionStore.getState().setStatus('active')
        useSessionStore.getState().recordVote('m1', 'yes')

        expect(Object.keys(persisted().state ?? {})).toEqual(['filtersByCode'])
    })

    it('evicts the least recently updated code past the five-entry cap', () => {
        setFilters('AAAA', 1_000)
        setFilters('BBBB', 2_000)
        setFilters('CCCC', 3_000)
        setFilters('DDDD', 4_000)
        setFilters('EEEE', 5_000)
        expect(Object.keys(useSessionStore.getState().filtersByCode).sort()).toEqual([
            'AAAA',
            'BBBB',
            'CCCC',
            'DDDD',
            'EEEE',
        ])

        setFilters('FFFF', 6_000)

        const kept = Object.keys(useSessionStore.getState().filtersByCode).sort()
        expect(kept).toEqual(['BBBB', 'CCCC', 'DDDD', 'EEEE', 'FFFF'])
        expect(kept).toHaveLength(5)
    })

    it('keeps a code alive by re-saving it, evicting the next-oldest instead', () => {
        setFilters('AAAA', 1_000)
        setFilters('BBBB', 2_000)
        setFilters('CCCC', 3_000)
        setFilters('DDDD', 4_000)
        setFilters('EEEE', 5_000)
        // Touching the oldest entry makes it the newest, so the cap must drop
        // what is now the oldest instead.
        setFilters('AAAA', 6_000)
        setFilters('FFFF', 7_000)

        expect(Object.keys(useSessionStore.getState().filtersByCode).sort()).toEqual([
            'AAAA',
            'CCCC',
            'DDDD',
            'EEEE',
            'FFFF',
        ])
    })

    it('stores the ready-gate pick in the same entry, defaulting to off', () => {
        useSessionStore.getState().setFilters('ABCD', { genres: ['Action'] }, true)
        useSessionStore.getState().setFilters('EFGH', { genres: ['Drama'] })

        const stored = persisted().state?.filtersByCode as Record<string, { waitForReady?: boolean }>
        expect(stored['ABCD'].waitForReady).toBe(true)
        expect(stored['EFGH'].waitForReady).toBe(false)
        expect(Object.keys(persisted().state ?? {})).toEqual(['filtersByCode'])
    })

    it('leaves remembered filters in place when the session state is reset', () => {
        useSessionStore.getState().setFilters('ABCD', { genres: ['Action'] })
        useSessionStore.getState().setStatus('active')

        useSessionStore.getState().reset()

        expect(useSessionStore.getState().status).toBe('lobby')
        expect(useSessionStore.getState().filtersByCode).toHaveProperty('ABCD')
    })
})
