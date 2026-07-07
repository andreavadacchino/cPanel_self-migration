import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { fetchMigrations, type Migration } from '../../lib/api'
import EmptyDashboard from '../../components/EmptyDashboard'
import CreateMigrationForm from './CreateMigrationForm'

type LoadState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'ready'; migrations: Migration[] }

export default function MigrationDashboard() {
  const [state, setState] = useState<LoadState>({ kind: 'loading' })
  const [creating, setCreating] = useState(false)
  const navigate = useNavigate()

  function load() {
    setState({ kind: 'loading' })
    fetchMigrations()
      .then((migrations) => setState({ kind: 'ready', migrations }))
      .catch((error: unknown) => {
        const message =
          error instanceof Error ? error.message : 'Errore sconosciuto'
        setState({ kind: 'error', message })
      })
  }

  useEffect(load, [])

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Migrazioni</h1>
          <p>Gestisci e monitora le migrazioni degli account cPanel.</p>
        </div>
        {!creating && (
          <button
            className="btn btn--primary"
            onClick={() => setCreating(true)}
          >
            Nuova migrazione
          </button>
        )}
      </div>

      {creating && (
        <CreateMigrationForm
          onCancel={() => setCreating(false)}
          onCreated={(migration) => {
            setCreating(false)
            navigate(`/migrations/${migration.id}`)
          }}
        />
      )}

      {state.kind === 'loading' && <div className="state-msg">Caricamento…</div>}

      {state.kind === 'error' && (
        <div className="state-msg state-msg--error">
          Impossibile contattare l'API: {state.message}
        </div>
      )}

      {state.kind === 'ready' && state.migrations.length === 0 && !creating && (
        <EmptyDashboard onCreate={() => setCreating(true)} />
      )}

      {state.kind === 'ready' && state.migrations.length > 0 && (
        <div className="card-list">
          {state.migrations.map((migration) => (
            <Link
              className="migration-card migration-card--link"
              key={migration.id}
              to={`/migrations/${migration.id}`}
            >
              <div>
                <div className="migration-card__name">{migration.name}</div>
                <div className="migration-card__domain">{migration.domain}</div>
              </div>
              <span className="badge">{migration.status}</span>
            </Link>
          ))}
        </div>
      )}
    </>
  )
}
