import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { getSetupStatus, type SetupStatus } from '../api'
import '../styles/setup.css'

const INSTALL_DOCS = 'https://github.com/eiladin/movie-night-showdown/blob/main/docs/INSTALL.md'
const TMDB_API_SETTINGS = 'https://www.themoviedb.org/settings/api'

// Setup explains how to configure a deployment that has no usable movie source
// yet. It is where the app sends a fresh install instead of showing an empty
// source picker, and stays reachable afterwards as a reference.
export default function Setup() {
    const [status, setStatus] = useState<SetupStatus | null>(null)

    useEffect(() => {
        getSetupStatus()
            .then(setStatus)
            .catch((err) => console.error('Failed to load setup status:', err))
    }, [])

    return (
        <div className="setup-page">
            <h1>Finish setting up</h1>

            <p className="setup-lede">
                Movie Night Showdown needs at least one place to get movies from. Pick whichever
                of these fits your setup, add the environment variables to your{' '}
                <code>.env</code> or <code>docker-compose.yml</code>, and restart the container.
            </p>

            {status && (
                <ul className="setup-status">
                    <li className={status.jellyfin ? 'ok' : 'missing'}>
                        Jellyfin library: {status.jellyfin ? 'configured' : 'not configured'}
                    </li>
                    <li className={status.streaming ? 'ok' : 'missing'}>
                        Streaming services: {status.streaming ? 'configured' : 'not configured'}
                    </li>
                </ul>
            )}

            <section className="setup-option">
                <h2>Jellyfin only</h2>
                <p>Draw the deck from your own library. This is the original setup.</p>
                <pre>
                    <code>
                        {'JELLYFIN_URL=http://jellyfin.local:8096\n'}
                        {'JELLYFIN_API_KEY=your-jellyfin-api-key\n'}
                        {'JELLYFIN_USER_ID=your-user-id   # optional, for "unwatched only"'}
                    </code>
                </pre>
                <p className="setup-note">
                    Create the API key in Jellyfin under Dashboard → API Keys. It stays on this
                    server and is never sent to browsers.
                </p>
            </section>

            <section className="setup-option">
                <h2>Streaming services only</h2>
                <p>
                    No local library needed — the deck comes from Netflix, Prime Video, and
                    Disney+ catalogs.
                </p>
                <pre>
                    <code>
                        {'TMDB_READ_TOKEN=your-tmdb-v4-read-token\n'}
                        {'STREAMING_PROVIDERS=netflix,prime,disney   # optional'}
                    </code>
                </pre>
                <p className="setup-note">
                    Get the token from{' '}
                    <a href={TMDB_API_SETTINGS} target="_blank" rel="noreferrer">
                        TMDB → Settings → API
                    </a>
                    . Copy the <strong>API Read Access Token</strong> (the long v4 one), not the
                    shorter v3 key.
                </p>
            </section>

            <section className="setup-option recommended">
                <h2>
                    Both <span className="setup-badge">recommended</span>
                </h2>
                <p>
                    Set all of the above. The deck covers what you own and what you can stream,
                    and a movie available in both places appears once, badged with everywhere it
                    can be watched.
                </p>
            </section>

            <p className="setup-footer">
                Every option, including <code>PUBLIC_URL</code> and the poster cache, is covered in
                the{' '}
                <a href={INSTALL_DOCS} target="_blank" rel="noreferrer">
                    install guide
                </a>
                .
            </p>

            {status?.configured && (
                <p className="setup-footer">
                    <Link to="/">Back to the app</Link>
                </p>
            )}
        </div>
    )
}
