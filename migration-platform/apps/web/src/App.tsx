import { Route, Routes } from 'react-router-dom'
import AppShell from './components/AppShell'
import MigrationDashboard from './features/migrations/MigrationDashboard'
import MigrationSetupPage from './features/migrations/MigrationSetupPage'

export default function App() {
  return (
    <AppShell>
      <Routes>
        <Route path="/" element={<MigrationDashboard />} />
        <Route path="/migrations/:id" element={<MigrationSetupPage />} />
      </Routes>
    </AppShell>
  )
}
