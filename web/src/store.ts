import { create } from 'zustand'
import { persist } from 'zustand/middleware'
import type { Movie, PreviewFilters } from './api'
import type { LeaderboardEntry } from './ws'

// MAX_REMEMBERED_FILTERS caps how many sessions' filter picks are kept. This is
// persisted storage, so without a cap it would grow one entry for every session
// ever started in this browser.
const MAX_REMEMBERED_FILTERS = 5

// RememberedFilters is one session's picks plus when they were last touched.
// The timestamp is what pruning orders by; object key order is not something to
// rely on for that.
interface RememberedFilters {
    filters: PreviewFilters
    updatedAt: number
}

// Status mirrors server.Status (see server/session.go).
export type Status = 'lobby' | 'active' | 'matched' | 'ended'

// Participant mirrors server.Participant's JSON shape. Token is never sent
// to other participants, so it never appears here.
export interface Participant {
    id: string
    name: string
    isHost: boolean
    connected: boolean
}

export type Vote = 'yes' | 'no'

interface SessionSnapshot {
    status: Status
    code: string
    requiredCount: number
    participants: Participant[]
    yourParticipantId: string
    yourVotes?: Record<string, Vote>
}

interface SessionStore {
    code: string | null
    status: Status
    requiredCount: number
    participants: Participant[]
    deck: Movie[]
    myParticipantId: string | null
    // myVoteState is local UI state only (never another participant's votes):
    // movieID -> the vote I last cast, so the swipe screen can render the
    // right state after an undo or a reconnect replay.
    myVoteState: Record<string, Vote>
    // filtersByCode holds the host's chosen library filters per session code,
    // carried from HostSetup (where they're picked) to Lobby (where "Begin"
    // sends them in host:start). Not session state from the server — purely
    // local UI state, and the only part of this store that is persisted.
    //
    // Keyed by code rather than held in a single slot because otherwise a
    // second session inherits the first one's filters. Keying also means
    // returning to an earlier session restores its own picks rather than
    // whatever was chosen most recently.
    filtersByCode: Record<string, RememberedFilters>
    winner: Movie | null
    leaderboard: LeaderboardEntry[] | null

    applySessionState: (snapshot: SessionSnapshot) => void
    setParticipants: (participants: Participant[]) => void
    setDeck: (deck: Movie[]) => void
    setStatus: (status: Status) => void
    recordVote: (movieId: string, vote: Vote) => void
    clearVote: (movieId: string) => void
    setFilters: (code: string, filters: PreviewFilters) => void
    setWinner: (movie: Movie) => void
    setLeaderboard: (lb: LeaderboardEntry[]) => void
    reset: () => void
}

const initialState = {
    code: null,
    status: 'lobby' as Status,
    requiredCount: 0,
    participants: [],
    deck: [],
    myParticipantId: null,
    myVoteState: {},
    winner: null,
    leaderboard: null,
}

// filtersKey normalizes a session code before it is used as a storage key.
// Codes reach this store from two places — the ?code= query param on /host and
// the :code path param on /join, the latter already uppercased for hand-typed
// entry. Normalizing here means the two can never disagree and store a
// session's filters under one key while reading them back from another.
function filtersKey(code: string): string {
    return code.trim().toUpperCase()
}

// prune keeps only the most recently updated entries, so persisted storage
// cannot grow without bound.
function prune(entries: Record<string, RememberedFilters>): Record<string, RememberedFilters> {
    const codes = Object.keys(entries)
    if (codes.length <= MAX_REMEMBERED_FILTERS) return entries
    const kept = codes
        .sort((a, b) => entries[b].updatedAt - entries[a].updatedAt)
        .slice(0, MAX_REMEMBERED_FILTERS)
    return Object.fromEntries(kept.map((c) => [c, entries[c]]))
}

export const useSessionStore = create<SessionStore>()(
    persist(
        (set) => ({
            ...initialState,
            filtersByCode: {},

            applySessionState: (snapshot) =>
                set({
                    status: snapshot.status,
                    code: snapshot.code,
                    requiredCount: snapshot.requiredCount,
                    participants: snapshot.participants,
                    myParticipantId: snapshot.yourParticipantId,
                    myVoteState: snapshot.yourVotes || {},
                }),

            setParticipants: (participants) => set({ participants }),
            setDeck: (deck) => set({ deck }),
            setStatus: (status) => set({ status }),

            recordVote: (movieId, vote) =>
                set((s) => ({ myVoteState: { ...s.myVoteState, [movieId]: vote } })),

            clearVote: (movieId) =>
                set((s) => {
                    const next = { ...s.myVoteState }
                    delete next[movieId]
                    return { myVoteState: next }
                }),

            setFilters: (code, filters) =>
                set((s) => ({
                    filtersByCode: prune({
                        ...s.filtersByCode,
                        [filtersKey(code)]: { filters, updatedAt: Date.now() },
                    }),
                })),

            setWinner: (winner) => set({ winner, status: 'matched' }),
            setLeaderboard: (leaderboard) => set({ leaderboard, status: 'ended' }),

            // Everything in initialState is server-derived and must not survive
            // a move to a different session. filtersByCode is deliberately left
            // alone: it is the host's own picks, keyed by the session they
            // belong to, so it outlives the lobby unmount without leaking into
            // a different session.
            reset: () => set({ ...initialState }),
        }),
        {
            name: 'mns-filters',
            // Only the host's own picks are persisted. Everything else is
            // server-derived and must be re-fetched on reconnect: a stale deck
            // or participant list restored from disk would disagree with the
            // session and could not be corrected.
            partialize: (s) => ({ filtersByCode: s.filtersByCode }),
        },
    ),
)

// useFiltersFor reads one session's remembered picks. A code with nothing
// stored yields empty filters, which is what a newly created session shows.
export function useFiltersFor(code: string | null): PreviewFilters {
    return (
        useSessionStore((s) => (code ? s.filtersByCode[filtersKey(code)]?.filters : undefined)) ?? {}
    )
}
