import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { getSetupStatus, type SetupStatus } from '../api'
import '../styles/setup.css'

const INSTALL_DOCS = 'https://github.com/eiladin/movie-night-showdown/blob/main/docs/INSTALL.md'
const TMDB_API_SETTINGS = 'https://www.themoviedb.org/settings/api'
const TMDB_PROVIDER_LIST = 'https://api.themoviedb.org/3/watch/providers/movie'

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
                    {/* The resolved list, so a name that failed to match TMDB
                        is visible here rather than only in the server log. */}
                    {status.sources.length > 0 && (
                        <li className="ok">
                            Sources in use: {status.sources.map((s) => s.label).join(', ')}
                        </li>
                    )}
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
                    No local library needed — the deck comes from streaming catalogs. Any service
                    TMDB tracks can be used, not just the three offered by default.
                </p>
                <pre>
                    <code>
                        {'TMDB_READ_TOKEN=your-tmdb-v4-read-token\n'}
                        {'STREAMING_PROVIDERS=netflix,prime,disney   # optional\n'}
                        {'TMDB_WATCH_REGION=US                      # optional'}
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

            <section className="setup-option">
                <h2>Choosing streaming services</h2>
                <p>
                    <code>STREAMING_PROVIDERS</code> is a comma-separated list. Leave it unset for
                    Netflix, Prime Video, and Disney+. Name any other service TMDB tracks and it
                    is looked up at startup:
                </p>
                <pre>
                    <code>
                        {'STREAMING_PROVIDERS=hulu,peacock,max\n'}
                        {'STREAMING_PROVIDERS=15,386,1899        # the same three, by id'}
                    </code>
                </pre>
                <p className="setup-note">
                    Names are matched case-insensitively against TMDB's list, so{' '}
                    <code>hbo max</code> and <code>hbo-max</code> both work. A numeric entry is a
                    TMDB provider id, which skips name matching entirely — useful when a service's
                    name is ambiguous. To find an id, open{' '}
                    <a href={TMDB_PROVIDER_LIST} target="_blank" rel="noreferrer">
                        TMDB's watch-provider list
                    </a>{' '}
                    in a browser (add your region, e.g. <code>&amp;watch_region=US</code>) and read
                    the <code>provider_id</code> next to the service you want. An entry that
                    matches nothing is logged and skipped; it never stops the app from starting.
                </p>
                <p className="setup-note">
                    <code>TMDB_WATCH_REGION</code> is an ISO 3166-1 country code and defaults to{' '}
                    <code>US</code>. It decides which services exist and what they carry, so set it
                    if you are not in the US.
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
