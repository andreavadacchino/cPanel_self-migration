import { useState, type FormEvent } from 'react'
import {
  createEndpoint,
  type Endpoint,
  type EndpointRole,
} from '../../lib/api'

interface Props {
  migrationId: number
  role: EndpointRole
  onCreated: (endpoint: Endpoint) => void
}

const ROLE_LABEL: Record<EndpointRole, string> = {
  source: 'sorgente',
  destination: 'destinazione',
}

type AuthMode = 'direct' | 'env' | 'mock'

export default function EndpointForm({ migrationId, role, onCreated }: Props) {
  const [host, setHost] = useState('')
  const [username, setUsername] = useState('')
  const [port, setPort] = useState(2083)
  const [authMode, setAuthMode] = useState<AuthMode>('direct')
  const [token, setToken] = useState('')
  const [authRef, setAuthRef] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function handleSubmit(event: FormEvent) {
    event.preventDefault()
    setSubmitting(true)
    setError(null)
    try {
      const base = {
        role,
        label: role === 'source' ? 'Sorgente' : 'Destinazione',
        host: host.trim(),
        port,
        username: username.trim(),
      }
      const payload =
        authMode === 'direct'
          ? { ...base, auth_type: 'token' as const, token: token.trim() }
          : authMode === 'env'
            ? {
                ...base,
                auth_type: 'token_ref' as const,
                auth_ref: authRef.trim(),
              }
            : { ...base, auth_type: 'mock' as const }
      const endpoint = await createEndpoint(migrationId, payload)
      onCreated(endpoint)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Errore sconosciuto')
      setSubmitting(false)
    }
  }

  const credentialFilled =
    authMode === 'mock' ||
    (authMode === 'direct' && token.trim() !== '') ||
    (authMode === 'env' && authRef.trim() !== '')
  const canSubmit =
    host.trim() !== '' &&
    username.trim() !== '' &&
    port > 0 &&
    credentialFilled &&
    !submitting

  return (
    <form className="endpoint-form" onSubmit={handleSubmit}>
      <label className="field">
        <span className="field__label">Host {ROLE_LABEL[role]}</span>
        <input
          className="input"
          value={host}
          onChange={(e) => setHost(e.target.value)}
          placeholder={`${role}.example.com`}
        />
      </label>
      <div className="field-row">
        <label className="field field--grow">
          <span className="field__label">Username</span>
          <input
            className="input"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            placeholder="cpaneluser"
          />
        </label>
        <label className="field field--port">
          <span className="field__label">Porta</span>
          <input
            className="input"
            type="number"
            value={port}
            onChange={(e) => setPort(Number(e.target.value))}
          />
        </label>
      </div>
      <label className="field">
        <span className="field__label">Autenticazione</span>
        <select
          className="input"
          value={authMode}
          onChange={(e) => setAuthMode(e.target.value as AuthMode)}
        >
          <option value="direct">Token cPanel</option>
          <option value="env">Riferimento env://</option>
          <option value="mock">Mock (test locale)</option>
        </select>
      </label>
      {authMode === 'direct' && (
        <label className="field">
          <span className="field__label">Token API cPanel</span>
          <input
            className="input"
            type="password"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            autoComplete="off"
            placeholder="incolla qui il token cPanel"
          />
        </label>
      )}
      {authMode === 'env' && (
        <label className="field">
          <span className="field__label">Riferimento token</span>
          <input
            className="input"
            value={authRef}
            onChange={(e) => setAuthRef(e.target.value)}
            placeholder="env://SOURCE_CPANEL_TOKEN"
          />
        </label>
      )}
      {error && <div className="state-msg state-msg--error">{error}</div>}
      <button type="submit" className="btn btn--primary" disabled={!canSubmit}>
        {submitting ? 'Salvataggio…' : `Salva ${ROLE_LABEL[role]}`}
      </button>
      <p className="hint">
        {authMode === 'direct'
          ? 'Il token è cifrato prima di essere salvato e non viene mai mostrato di nuovo. Usa un token a scadenza.'
          : authMode === 'env'
            ? 'Salva solo un riferimento opaco (es. env://VAR): il token è letto dall’ambiente, mai memorizzato.'
            : 'Modalità offline per il test locale: nessuna credenziale richiesta.'}
      </p>
    </form>
  )
}
