import type { Availability } from '../api'
import { accentStyle } from '../sourceColor'

interface SourceBadgesProps {
    availability: Availability[] | undefined
    className?: string
}

// SourceBadges renders one badge per service carrying this movie. A film in the
// local library and on a streaming service shows both: knowing a local copy
// exists changes the decision, because it starts instantly and has no ads.
//
// The display name arrives on each entry from the server rather than from a
// table here: streaming sources are whatever TMDB watch providers the
// deployment configured, so this component cannot know their names.
export default function SourceBadges({ availability, className }: SourceBadgesProps) {
    if (!availability || availability.length === 0) return null
    return (
        <ul className={`source-badges${className ? ' ' + className : ''}`}>
            {availability.map((a) => (
                <li key={a.source} className="source-badge" style={accentStyle(a.source)}>
                    {a.label || a.source}
                </li>
            ))}
        </ul>
    )
}
