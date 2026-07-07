import { useEffect, useState } from 'react'
import { fetchMigrations, type Migration } from '../../lib/api'
import EmptyDashboard from '../../components/EmptyDashboard'

type LoadState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'ready'; migrations: Migration[] }

export default function MigrationDashboard() {
  const [state, setState] = useState<LoadState>({ kind: 'loading' })

  useEffect(() => {
    let active = true
    fetchMigrations()
      .then((migrations) => {
        if (active) setState({ kind: 'ready', migrations })
      })
      .catch((error: unknown) => {
        if (active) {
          const message =
            error instanceof Error ? error.message : 'Errore sconosciuto'
          setState({ kind: 'error', message })
        }
      })
    return () => {
      active = false
    }
  }, [])

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Migrazioni</h1>
          <p>Gestisci e monitora le migrazioni degli account cPanel.</p>
        </div>
        <button className="btn btn--primary">Nuova migrazione</button>
      </div>

      {state.kind === 'loading' && (
        <div className="state-msg">Caricamento…</div>
      )}

      {state.kind === 'error' && (
        <div className="state-msg state-msg--error">
          Impossibile contattare l'API: {state.message}
        </div>
      )}

      {state.kind === 'ready' && state.migrations.length === 0 && (
        <EmptyDashboard />
      )}

      {state.kind === 'ready' && state.migrations.length > 0 && (
        <div className="card-list">
          {state.migrations.map((migration) => (
            <div className="migration-card" key={migration.id}>
              <div>
                <div className="migration-card__name">{migration.name}</div>
                <div className="migration-card__domain">{migration.domain}</div>
              </div>
              <span className="badge">{migration.status}</span>
            </div>
          ))}
        </div>
      )}
    </>
  )
}
