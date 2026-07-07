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

type AuthMode = 'mock' | 'token'

export default function EndpointForm({ migrationId, role, onCreated }: Props) {
  const [host, setHost] = useState('')
  const [username, setUsername] = useState('')
  const [port, setPort] = useState(2083)
  const [authMode, setAuthMode] = useState<AuthMode>('mock')
  const [authRef, setAuthRef] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function handleSubmit(event: FormEvent) {
    event.preventDefault()
    setSubmitting(true)
    setError(null)
    try {
      const isToken = authMode === 'token'
      const endpoint = await createEndpoint(migrationId, {
        role,
        label: role === 'source' ? 'Sorgente' : 'Destinazione',
        host: host.trim(),
        port,
        username: username.trim(),
        auth_type: isToken ? 'token_ref' : 'mock',
        auth_ref: isToken ? authRef.trim() : null,
      })
      onCreated(endpoint)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Errore sconosciuto')
      setSubmitting(false)
    }
  }

  const canSubmit =
    host.trim() !== '' &&
    username.trim() !== '' &&
    port > 0 &&
    (authMode === 'mock' || authRef.trim() !== '') &&
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
          <option value="mock">Mock (test locale)</option>
          <option value="token">Token cPanel (env://)</option>
        </select>
      </label>
      {authMode === 'token' && (
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
        Nessun segreto viene salvato: solo un riferimento opaco (es.
        env://VAR). Il token viene letto dall’ambiente, mai memorizzato.
      </p>
    </form>
  )
}
