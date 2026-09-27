import { act, cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import Lobby from './Lobby'
import { useSessionStore, type Participant } from '../store'

// The socket is replaced with a recorder: these tests drive the lobby by
// emitting server messages and inspect what the lobby sends back.
const sent: { type: string; payload: unknown }[] = []
let emit: (type: string, payload: unknown) => void = () => {}

vi.mock('../ws', () => {
    class FakeSocket {
        private listeners = new Map<string, (p: unknown) => void>()
        constructor() {
            emit = (type, payload) => this.listeners.get(type)?.(payload)
        }
        static getToken() {
            return 'token'
        }
        on(type: string, fn: (p: unknown) => void) {
            this.listeners.set(type, fn)
            return () => this.listeners.delete(type)
        }
        connect() {}
        close() {}
        send(type: string, payload: unknown) {
            sent.push({ type, payload })
        }
    }
    return { SessionSocket: FakeSocket }
})

const host: Participant = { id: 'h', name: 'Host', isHost: true, connected: true, ready: false }
const guest = (ready: boolean, connected = true): Participant => ({
    id: 'g',
    name: 'Guest',
    isHost: false,
    connected,
    ready,
})

function renderLobby() {
    render(
        <MemoryRouter initialEntries={['/join/ABCD']}>
            <Routes>
                <Route path="/join/:code" element={<Lobby />} />
            </Routes>
        </MemoryRouter>,
    )
}

function state(you: string, participants: Participant[], waitForReady: boolean) {
    act(() =>
        emit('session_state', {
            status: 'lobby',
            code: 'ABCD',
            requiredCount: 0,
            waitForReady,
            participants,
            yourParticipantId: you,
            yourToken: 'token',
        }),
    )
}

beforeEach(() => {
    sent.length = 0
    useSessionStore.setState({ filtersByCode: {} })
})

afterEach(() => cleanup())

describe('ready gate', () => {
    it('disables Begin while a guest is not ready, including an offline one', () => {
        renderLobby()
        state('h', [host, guest(false, false)], true)
        expect(screen.getByRole('button', { name: 'Begin' })).toBeDisabled()
        expect(screen.getByText(/1 guest is not ready/)).toBeInTheDocument()
        expect(screen.getByText('not ready')).toBeInTheDocument()
        expect(screen.queryByRole('button', { name: /^ready/i })).toBeNull()
    })

    it('enables Begin once every guest is ready', () => {
        renderLobby()
        state('h', [host, guest(false)], true)
        act(() => emit('participant_update', { participants: [host, guest(true)], waitForReady: true }))
        expect(screen.getByRole('button', { name: 'Begin' })).toBeEnabled()
    })

    it('enables Begin with no guests', () => {
        renderLobby()
        state('h', [host], true)
        expect(screen.getByRole('button', { name: 'Begin' })).toBeEnabled()
    })

    it('ignores ready flags when the gate is off', () => {
        renderLobby()
        state('h', [host, guest(false)], false)
        expect(screen.getByRole('button', { name: 'Begin' })).toBeEnabled()
        expect(screen.queryByText('not ready')).toBeNull()
    })

    it('sends the host’s remembered pick on join', () => {
        useSessionStore.getState().setFilters('ABCD', {}, true)
        renderLobby()
        state('h', [host], false)
        expect(sent).toContainEqual({ type: 'host:options', payload: { waitForReady: true } })
    })

    it('gives a guest a ready toggle only when the gate is on', async () => {
        renderLobby()
        state('g', [host, guest(false)], false)
        expect(screen.queryByRole('button', { name: /^ready/i })).toBeNull()
        expect(sent.some((m) => m.type === 'host:options')).toBe(false)

        act(() => emit('participant_update', { participants: [host, guest(false)], waitForReady: true }))
        await userEvent.click(screen.getByRole('button', { name: /^ready/i }))
        expect(sent).toContainEqual({ type: 'ready', payload: { ready: true } })
    })
})
