import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { fetchMigrations, type Migration } from '../../lib/api'
import EmptyDashboard from '../../components/EmptyDashboard'
import CreateMigrationForm from './CreateMigrationForm'

type LoadState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'ready'; migrations: Migration[] }

function statusLabel(status: string): string {
  switch (status) {
    case 'draft':
      return 'Bozze'
    case 'running':
      return 'In corso'
    case 'succeeded':
      return 'Completate'
    case 'failed':
      return 'Critiche'
    default:
      return 'Monitorate'
  }
}

function buildStats(migrations: Migration[]) {
  const groups = new Map<string, number>()
  for (const migration of migrations) {
    groups.set(migration.status, (groups.get(migration.status) ?? 0) + 1)
  }

  return [
    {
      label: 'Sessioni',
      value: migrations.length,
      tone: 'neutral',
    },
    ...Array.from(groups.entries()).map(([status, value]) => ({
      label: statusLabel(status),
      value,
      tone: status,
    })),
  ]
}

function formatDate(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return 'Data non disponibile'
  return new Intl.DateTimeFormat('it-IT', {
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    month: 'short',
  }).format(date)
}

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

  const stats = state.kind === 'ready' ? buildStats(state.migrations) : []

  return (
    <>
      <div className="page-head page-head--dashboard">
        <div className="page-head__copy">
          <span className="eyebrow">Migrazioni cPanel</span>
          <h1>Console operativa</h1>
          <p>
            Sessioni, endpoint, preflight e comparativa inventario in un unico
            pannello di controllo.
          </p>
          {stats.length > 0 && (
            <div className="stats-strip">
              {stats.map((stat) => (
                <div
                  className={`stat-card stat-card--${stat.tone}`}
                  key={`${stat.label}-${stat.value}`}
                >
                  <div className="stat-card__value">{stat.value}</div>
                  <div className="stat-card__label">{stat.label}</div>
                </div>
              ))}
            </div>
          )}
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
        <div className="modal-backdrop" role="presentation">
          <CreateMigrationForm
            onCancel={() => setCreating(false)}
            onCreated={(migration) => {
              setCreating(false)
              navigate(`/migrations/${migration.id}`)
            }}
          />
        </div>
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
                <div className="migration-card__meta">
                  Aggiornata {formatDate(migration.updated_at)}
                </div>
              </div>
              <div className="migration-card__side">
                <span className="badge">{migration.status}</span>
                <span className="migration-card__open">Apri setup</span>
              </div>
            </Link>
          ))}
        </div>
      )}
    </>
  )
}
