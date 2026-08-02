import { useEffect, useRef, useState, type FormEvent } from 'react'
import { Link, useParams } from 'react-router-dom'
import QRJoin from '../components/QRJoin'
import { useFiltersFor, useSessionStore } from '../store'
import {
    SessionSocket,
    type DeckPayload,
    type ErrorPayload,
    type MatchPayload,
    type ParticipantUpdatePayload,
    type SessionStatePayload,
    type SessionEndedPayload,
} from '../ws'
import Swipe from './Swipe'
import Result from './Result'
import '../styles/lobby.css'

// Lobby is reached at /join/:code (guest link, and the host arrives here
// too via a link from /host). It connects over WebSocket, joins (either
// resuming via a saved token or by submitting a name), and shows the live
// roster. The host additionally sees the join code + QR.
export default function Lobby() {
    const { code = '' } = useParams<{ code: string }>()
    const upperCode = code.toUpperCase()

    const status = useSessionStore((s) => s.status)
    const participants = useSessionStore((s) => s.participants)
    const myParticipantId = useSessionStore((s) => s.myParticipantId)
    const filters = useFiltersFor(upperCode)
    const applySessionState = useSessionStore((s) => s.applySessionState)
    const setParticipants = useSessionStore((s) => s.setParticipants)
    const setDeck = useSessionStore((s) => s.setDeck)
    const setWinner = useSessionStore((s) => s.setWinner)
    const setLeaderboard = useSessionStore((s) => s.setLeaderboard)
    const reset = useSessionStore((s) => s.reset)

    const [name, setName] = useState('')
    const [joined, setJoined] = useState(() => SessionSocket.getToken(upperCode) !== '')
    const [socketError, setSocketError] = useState<string | null>(null)
    const socketRef = useRef<SessionSocket | null>(null)

    const [maxMovies, setMaxMovies] = useState(50)
    const [requiredCount, setRequiredCountRaw] = useState(1)
    const [requiredTouched, setRequiredTouched] = useState(false)

    // Default "required to agree" to the current roster size until the host
    // deliberately overrides it (never raise above the roster).
    useEffect(() => {
        if (!requiredTouched) setRequiredCountRaw(Math.max(participants.length, 1))
    }, [participants.length, requiredTouched])

    function setRequiredCount(value: number) {
        setRequiredTouched(true)
        setRequiredCountRaw(value)
    }

    // Reset session state when navigating to a different code.
    useEffect(() => {
        return () => reset()
    }, [upperCode, reset])

    useEffect(() => {
        if (!joined) return

        const socket = new SessionSocket(upperCode, name)
        socketRef.current = socket

        const offState = socket.on('session_state', (payload) =>
            applySessionState(payload as SessionStatePayload),
        )
        const offParticipants = socket.on('participant_update', (payload) =>
            setParticipants((payload as ParticipantUpdatePayload).participants),
        )
        const offDeck = socket.on('deck', (payload) => setDeck((payload as DeckPayload).movies))
        const offError = socket.on('error', (payload) => setSocketError((payload as ErrorPayload).message))
        const offMatch = socket.on('match', (payload) => {
            const { movie } = payload as MatchPayload
            setWinner(movie)
        })
        const offEnded = socket.on('session_ended', (payload) => {
            const { leaderboard } = payload as SessionEndedPayload
            setLeaderboard(leaderboard)
        })

        socket.connect()

        return () => {
            offState()
            offParticipants()
            offDeck()
            offError()
            offMatch()
            offEnded()
            socket.close()
        }
        // name is intentionally captured only at the moment `joined` flips true.
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [joined, upperCode])

    function handleJoinSubmit(e: FormEvent) {
        e.preventDefault()
        if (!name.trim()) return
        setJoined(true)
    }

    function handleBegin(e: FormEvent) {
        e.preventDefault()
        socketRef.current?.send('host:start', { filters, maxMovies, requiredCount })
    }

    const me = participants.find((p) => p.id === myParticipantId)
    const isHost = me?.isHost ?? false
    const joinURL = `${window.location.origin}/join/${upperCode}`

    if (!joined) {
        return (
            <div className="lobby lobby-join-form">
                <h1>Join session {upperCode}</h1>
                <form onSubmit={handleJoinSubmit}>
                    <input
                        type="text"
                        placeholder="Your name"
                        value={name}
                        onChange={(e) => setName(e.target.value)}
                        autoFocus
                    />
                    <button type="submit" className="btn-primary">Join</button>
                </form>
            </div>
        )
    }

    // Once the host starts the session, the deck takes over the same screen
    // (same socket, same mounted component) rather than a route change — the
    // WS connection must survive the transition.
    if (socketRef.current && (status === 'matched' || status === 'ended')) {
        return <Result socket={socketRef.current} />
    }

    if (socketRef.current && status !== 'lobby') {
        return <Swipe socket={socketRef.current} />
    }

    return (
        <div className="lobby">
            <h1>Session {upperCode}</h1>
            <p className="lobby-status">Status: {status}</p>

            {isHost && <QRJoin joinURL={joinURL} />}

            {socketError && <p className="lobby-error">{socketError}</p>}
            {/* Filters stay editable for as long as the lobby is on screen.
                HostSetup resumes from the same session code, so the round trip
                costs neither the room nor the roster. Once Begin succeeds the
                status leaves 'lobby' and this whole branch is replaced by the
                deck, so no gate is needed to hide it after the start. */}
            {isHost && (
                <Link to={`/host?code=${upperCode}`} className="lobby-refilter">
                    Change filters
                </Link>
            )}
            {participants.length === 0 && !socketError && <p>Connecting…</p>}

            <ul className="participant-list">
                {participants.map((p) => (
                    <li key={p.id} className={p.connected ? 'connected' : 'disconnected'}>
                        <span className="participant-name">
                            {p.name}
                            {p.isHost ? ' (host)' : ''}
                        </span>
                        <span className="participant-status">{p.connected ? 'online' : 'offline'}</span>
                    </li>
                ))}
            </ul>

            {isHost && (
                <form className="begin-form" onSubmit={handleBegin}>
                    <label>
                        Max movies
                        <input
                            type="number"
                            min={1}
                            value={maxMovies}
                            onChange={(e) => setMaxMovies(Number(e.target.value))}
                        />
                    </label>
                    <label>
                        Required to agree
                        <input
                            type="number"
                            min={1}
                            max={Math.max(participants.length, 1)}
                            value={requiredCount}
                            onChange={(e) => setRequiredCount(Number(e.target.value))}
                        />
                    </label>
                    <button type="submit" className="btn-primary" disabled={participants.length === 0}>
                        Begin
                    </button>
                </form>
            )}
        </div>
    )
}
