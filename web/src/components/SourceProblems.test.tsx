import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import SourceProblems from './SourceProblems'

afterEach(cleanup)

describe('SourceProblems', () => {
    // The point of the whole feature: a rejected credential and an unreachable
    // host have different fixes, so they must not read the same.
    it('words each reason differently', () => {
        render(
            <SourceProblems
                problems={[
                    { label: 'Jellyfin', source: 'jellyfin', reason: 'unauthorized' },
                    { label: 'Plex — Films', source: 'plex-3', reason: 'unreachable' },
                    { label: 'Jellyfin — Kids Movies', reason: 'unresolved' },
                ]}
            />,
        )
        expect(screen.getByText(/jellyfin rejected the credentials/i)).toBeInTheDocument()
        expect(screen.getByText(/plex — films could not be reached/i)).toBeInTheDocument()
        expect(screen.getByText(/jellyfin — kids movies could not be found/i)).toBeInTheDocument()
    })

    // The reason set is open server-side; an unknown one must still name the
    // source rather than rendering nothing.
    it('falls back for an unknown reason', () => {
        render(<SourceProblems problems={[{ label: 'Netflix', source: 'netflix', reason: 'wat' }]} />)
        expect(screen.getByText(/netflix is unavailable/i)).toBeInTheDocument()
    })

    it('renders nothing when there is no problem', () => {
        const { container } = render(<SourceProblems problems={[]} />)
        expect(container).toBeEmptyDOMElement()
    })
})
