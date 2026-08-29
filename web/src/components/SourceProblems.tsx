import type { SourceProblem } from '../api'
import '../styles/source-problems.css'

// reasonText turns a machine-readable failure reason into a sentence.
//
// The wording lives here, not on the server: the client owns display text
// exactly as it owns source labels and end reasons. An unrecognized reason
// falls back to a neutral sentence rather than rendering nothing, because the
// reason set is allowed to grow server-side.
function reasonText(problem: SourceProblem): string {
    switch (problem.reason) {
        case 'unauthorized':
            return `${problem.label} rejected the credentials.`
        case 'unreachable':
            return `${problem.label} could not be reached.`
        case 'unresolved':
            return `${problem.label} could not be found.`
        default:
            return `${problem.label} is unavailable.`
    }
}

interface Props {
    problems: SourceProblem[]
    // context is appended to the list, saying what the host loses by it.
    context?: string
}

// SourceProblems reports the sources a request could not use, and why.
//
// It is deliberately a banner and never a replacement for the page: the other
// sources still work, and a screen that blanks itself over one bad credential
// tells the host less than one that keeps working and explains what is missing.
export default function SourceProblems({ problems, context }: Props) {
    if (problems.length === 0) return null
    return (
        <div className="source-problems" role="status">
            <ul>
                {problems.map((p) => (
                    <li key={`${p.source ?? ''}:${p.label}`}>{reasonText(p)}</li>
                ))}
            </ul>
            {context && <p className="source-problems-context">{context}</p>}
        </div>
    )
}
