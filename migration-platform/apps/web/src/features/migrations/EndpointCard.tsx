import { useState } from 'react'
import {
  deleteEndpoint,
  testConnection,
  updateEndpointCredentials,
  type Capabilities,
  type Endpoint,
  type EndpointRole,
} from '../../lib/api'
import ConnectionStatusBadge from '../../components/ConnectionStatusBadge'
import EndpointForm from './EndpointForm'

interface Props {
  migrationId: number
  role: EndpointRole
  endpoint: Endpoint | undefined
  onChanged: (endpoint: Endpoint) => void
  onRemoved: (endpointId: number) => void
}

const TITLE: Record<EndpointRole, string> = {
  source: 'Server sorgente',
  destination: 'Server destinazione',
}

const ROLE_META: Record<EndpointRole, { code: string; description: string }> = {
  source: {
    code: 'SRC',
    description: 'Account cPanel da leggere in sola lettura',
  },
  destination: {
    code: 'DST',
    description: 'Ambiente target per verifica e preparazione',
  },
}

const CAP_LABELS: ReadonlyArray<[keyof Capabilities, string]> = [
  ['can_read_account_info', 'Account'],
  ['can_read_domains', 'Domini'],
  ['can_read_email', 'Email'],
  ['can_read_databases', 'Database'],
  ['can_read_cron', 'Cron'],
  ['can_read_ssl', 'SSL'],
  ['can_read_dns', 'DNS'],
]

function CapabilitiesView({ capabilities }: { capabilities: Capabilities }) {
  return (
    <div className="capabilities">
      <div className="capabilities__head">
        <span className="capabilities__label">Copertura lettura</span>
        <span className="capabilities__mode">
          {capabilities.source === 'mock' ? 'Mock' : 'cPanel reale'}
        </span>
      </div>
      <div className="cap-badges">
        {CAP_LABELS.map(([key, label]) => {
          const enabled = Boolean(capabilities[key])
          return (
            <span
              key={key}
              className={`cap-badge ${enabled ? 'cap-badge--on' : 'cap-badge--off'}`}
            >
              <span className="cap-badge__dot" />
              {label}
            </span>
          )
        })}
      </div>
      {capabilities.limitations.length > 0 && (
        <div className="hint">
          Non disponibili: {capabilities.limitations.join(', ')}
        </div>
      )}
    </div>
  )
}

export default function EndpointCard({
  migrationId,
  role,
  endpoint,
  onChanged,
  onRemoved,
}: Props) {
  const [testing, setTesting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [refreshing, setRefreshing] = useState(false)
  const [newToken, setNewToken] = useState('')
  const [savingToken, setSavingToken] = useState(false)
  const [editing, setEditing] = useState(false)
  const [removing, setRemoving] = useState(false)

  async function handleTest() {
    if (!endpoint) return
    setTesting(true)
    setError(null)
    try {
      const updated = await testConnection(endpoint.id)
      onChanged(updated)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Errore sconosciuto')
    } finally {
      setTesting(false)
    }
  }

  async function handleRemove() {
    if (!endpoint) return
    setRemoving(true)
    setError(null)
    try {
      await deleteEndpoint(endpoint.id)
      onRemoved(endpoint.id)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Errore sconosciuto')
      setRemoving(false)
    }
  }

  async function handleSaveToken() {
    if (!endpoint || newToken.trim() === '') return
    setSavingToken(true)
    setError(null)
    try {
      const updated = await updateEndpointCredentials(endpoint.id, newToken.trim())
      onChanged(updated)
      setNewToken('')
      setRefreshing(false)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Errore sconosciuto')
    } finally {
      setSavingToken(false)
    }
  }

  return (
    <section className={`panel endpoint-card endpoint-card--${role}`}>
      <header className="endpoint-card__head">
        <div className="endpoint-card__identity">
          <div className="endpoint-card__mark" aria-hidden="true">
            {ROLE_META[role].code}
          </div>
          <div>
            <div className="endpoint-card__title">{TITLE[role]}</div>
            <div className="endpoint-card__subtitle">
              {ROLE_META[role].description}
            </div>
          </div>
        </div>
        {endpoint ? (
          <ConnectionStatusBadge
            status={testing ? 'testing' : endpoint.connection_status}
          />
        ) : (
          <span className="badge badge--muted">Non configurato</span>
        )}
      </header>

      {endpoint && editing ? (
        <EndpointForm
          migrationId={migrationId}
          role={role}
          endpoint={endpoint}
          onSaved={(saved) => {
            onChanged(saved)
            setEditing(false)
          }}
          onCancel={() => setEditing(false)}
        />
      ) : endpoint ? (
        <>
          <dl className="kv">
            <div>
              <dt>Host</dt>
              <dd>{endpoint.host}</dd>
            </div>
            <div>
              <dt>Utente</dt>
              <dd>{endpoint.username}</dd>
            </div>
            <div>
              <dt>Porta</dt>
              <dd>{endpoint.port}</dd>
            </div>
          </dl>
          {endpoint.last_error && (
            <div className="state-msg state-msg--error">
              {endpoint.last_error}
            </div>
          )}
          {endpoint.capabilities && (
            <CapabilitiesView capabilities={endpoint.capabilities} />
          )}
          {error && <div className="state-msg state-msg--error">{error}</div>}
          <div className="endpoint-card__actions">
            <button
              className="btn btn--ghost"
              onClick={handleTest}
              disabled={testing || removing}
            >
              {testing ? 'Test in corso…' : 'Testa connessione'}
            </button>
            <button
              className="btn btn--ghost"
              onClick={() => {
                setError(null)
                setEditing(true)
              }}
              disabled={removing}
            >
              Modifica
            </button>
            {endpoint.auth_type === 'token' && !refreshing && (
              <button
                className="btn btn--ghost"
                onClick={() => setRefreshing(true)}
                disabled={removing}
              >
                Aggiorna token
              </button>
            )}
            <button
              className="btn btn--ghost endpoint-card__danger"
              onClick={handleRemove}
              disabled={removing}
            >
              {removing ? 'Rimozione…' : 'Rimuovi'}
            </button>
          </div>
          {endpoint.auth_type === 'token' && refreshing && (
            <div className="field endpoint-card__token-field">
              <span className="field__label">Nuovo token API cPanel</span>
              <input
                className="input"
                type="password"
                value={newToken}
                autoComplete="off"
                placeholder="incolla il nuovo token"
                onChange={(e) => setNewToken(e.target.value)}
              />
              <div className="form__actions">
                <button
                  className="btn btn--ghost"
                  onClick={() => {
                    setRefreshing(false)
                    setNewToken('')
                  }}
                  disabled={savingToken}
                >
                  Annulla
                </button>
                <button
                  className="btn btn--primary"
                  onClick={handleSaveToken}
                  disabled={savingToken || newToken.trim() === ''}
                >
                  {savingToken ? 'Salvataggio…' : 'Salva token'}
                </button>
              </div>
            </div>
          )}
        </>
      ) : (
        <EndpointForm
          migrationId={migrationId}
          role={role}
          onSaved={onChanged}
        />
      )}
    </section>
  )
}
