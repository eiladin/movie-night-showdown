import { useNavigate } from 'react-router-dom'
import { useSessionStore } from '../store'
import type { SessionSocket } from '../ws'
import Confetti from '../components/Confetti'
import SourceBadges from '../components/SourceBadges'
import '../styles/result.css'

interface ResultProps {
    socket: SessionSocket
}

export default function Result({ socket }: ResultProps) {
    const status = useSessionStore((s) => s.status)
    const winner = useSessionStore((s) => s.winner)
    const leaderboard = useSessionStore((s) => s.leaderboard)
    const participants = useSessionStore((s) => s.participants)
    const myParticipantId = useSessionStore((s) => s.myParticipantId)
    const navigate = useNavigate()

    const me = participants.find((p) => p.id === myParticipantId)
    const isHost = me?.isHost ?? false

    function handlePick(movieId: string) {
        if (!isHost) return
        socket.send('host:pick', { movieID: movieId })
    }

    if (status === 'matched' && winner) {
        return (
            <div className="result-screen">
                <Confetti />
                <h1>It&apos;s a match!</h1>
                <div className="winner-card">
                    <img src={winner.posterURL} alt={winner.title} />
                    <div className="winner-meta">
                        <h2>{winner.title}</h2>
                        {/* Where to watch it sits directly under the title:
                            it is the one thing this screen exists to answer,
                            so it must not sit below the wrappable metadata. */}
                        <SourceBadges availability={winner.availability} />
                        <p>{winner.year} · {winner.genres?.join(', ')}</p>
                    </div>
                </div>
                <button type="button" className="result-home-btn" onClick={() => navigate('/')}>Back to home</button>
            </div>
        )
    }

    if (status === 'ended' && leaderboard) {
        return (
            <div className="result-screen">
                <h1>No match</h1>
                <p className="leaderboard-desc">
                    {isHost ? 'Tap a movie to declare it the winner' : 'Waiting for the host to choose'}
                </p>
                <ul className="leaderboard">
                    {leaderboard.map((entry) => (
                        <li
                            key={entry.movie.id}
                            className={`leaderboard-item ${isHost ? 'clickable' : ''}`}
                            onClick={() => handlePick(entry.movie.id)}
                        >
                            <img src={entry.movie.posterURL} alt={entry.movie.title} />
                            <div className="leaderboard-meta">
                                <h3>{entry.movie.title}</h3>
                                <SourceBadges availability={entry.movie.availability} />
                                <p>
                                    {entry.yesCount} yes · ★ {entry.movie.communityRating}
                                </p>
                            </div>
                        </li>
                    ))}
                </ul>
                <button type="button" className="result-home-btn" onClick={() => navigate('/')}>Back to home</button>
            </div>
        )
    }

    return null
}
