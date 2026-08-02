import { BrowserRouter, Route, Routes } from 'react-router-dom'
import RequireSetup from './components/RequireSetup'
import HostSetup from './pages/HostSetup'
import Landing from './pages/Landing'
import Lobby from './pages/Lobby'
import Setup from './pages/Setup'

function App() {
    return (
        <BrowserRouter>
            <Routes>
                {/* /setup sits outside the gate: it is where the gate sends an
                    unconfigured deployment, and stays reachable as a reference
                    once configuration is done. */}
                <Route path="/setup" element={<Setup />} />
                <Route
                    path="/"
                    element={
                        <RequireSetup>
                            <Landing />
                        </RequireSetup>
                    }
                />
                <Route
                    path="/host"
                    element={
                        <RequireSetup>
                            <HostSetup />
                        </RequireSetup>
                    }
                />
                <Route
                    path="/join/:code"
                    element={
                        <RequireSetup>
                            <Lobby />
                        </RequireSetup>
                    }
                />
            </Routes>
        </BrowserRouter>
    )
}

export default App
