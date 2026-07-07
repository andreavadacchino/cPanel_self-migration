import AppShell from './components/AppShell'
import MigrationDashboard from './features/migrations/MigrationDashboard'

export default function App() {
  return (
    <AppShell>
      <MigrationDashboard />
    </AppShell>
  )
}
