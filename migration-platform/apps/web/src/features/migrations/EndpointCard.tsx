import { useState } from 'react'
import {
  testConnection,
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
}

const TITLE: Record<EndpointRole, string> = {
  source: 'Server sorgente',
  destination: 'Server destinazione',
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
      <div className="hint">
        Modalità: {capabilities.source === 'mock' ? 'mock' : 'cPanel reale'}
      </div>
      <div className="cap-badges">
        {CAP_LABELS.map(([key, label]) => (
          <span
            key={key}
            className={`badge ${Boolean(capabilities[key]) ? '' : 'badge--muted'}`}
          >
            {label}
          </span>
        ))}
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
}: Props) {
  const [testing, setTesting] = useState(false)
  const [error, setError] = useState<string | null>(null)

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

  return (
    <section className="panel endpoint-card">
      <header className="endpoint-card__head">
        <div className="endpoint-card__title">{TITLE[role]}</div>
        {endpoint ? (
          <ConnectionStatusBadge
            status={testing ? 'testing' : endpoint.connection_status}
          />
        ) : (
          <span className="badge badge--muted">Non configurato</span>
        )}
      </header>

      {endpoint ? (
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
          <button
            className="btn btn--ghost"
            onClick={handleTest}
            disabled={testing}
          >
            {testing ? 'Test in corso…' : 'Testa connessione'}
          </button>
        </>
      ) : (
        <EndpointForm
          migrationId={migrationId}
          role={role}
          onCreated={onChanged}
        />
      )}
    </section>
  )
}
