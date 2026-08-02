import { useEffect, useState, type ReactNode } from 'react'
import { Navigate } from 'react-router-dom'
import { getSetupStatus } from '../api'

// RequireSetup keeps the app out of reach until this deployment has at least
// one movie source. Without one there is no deck to deal, so every route would
// fail at its first query; sending the host to the setup guide explains that
// instead of showing an empty picker.
//
// A failed status request is treated as "configured": the check exists to guide
// a fresh install, not to gate a working one behind a request that might be
// blocked by a proxy.
export default function RequireSetup({ children }: { children: ReactNode }) {
    const [configured, setConfigured] = useState<boolean | null>(null)

    useEffect(() => {
        getSetupStatus()
            .then((s) => setConfigured(s.configured))
            .catch((err) => {
                console.error('Failed to load setup status:', err)
                setConfigured(true)
            })
    }, [])

    // Render nothing until the answer arrives, so a configured deployment never
    // flashes the setup guide on the way to the landing page.
    if (configured === null) return null
    if (!configured) return <Navigate to="/setup" replace />
    return <>{children}</>
}
