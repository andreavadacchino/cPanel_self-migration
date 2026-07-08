import { useState, type FormEvent } from 'react'
import {
  createEndpoint,
  updateEndpoint,
  type AuthType,
  type Endpoint,
  type EndpointRole,
  type EndpointUpdate,
} from '../../lib/api'

interface Props {
  migrationId: number
  role: EndpointRole
  // When present, the form edits this endpoint instead of creating a new one.
  endpoint?: Endpoint
  onSaved: (endpoint: Endpoint) => void
  onCancel?: () => void
}

const ROLE_LABEL: Record<EndpointRole, string> = {
  source: 'sorgente',
  destination: 'destinazione',
}

type AuthMode = 'direct' | 'env' | 'mock'

function modeOf(endpoint?: Endpoint): AuthMode {
  if (!endpoint) return 'direct'
  if (endpoint.auth_type === 'token') return 'direct'
  if (endpoint.auth_type === 'token_ref') return 'env'
  return 'mock'
}

export default function EndpointForm({
  migrationId,
  role,
  endpoint,
  onSaved,
  onCancel,
}: Props) {
  const isEdit = endpoint != null
  const [host, setHost] = useState(endpoint?.host ?? '')
  const [username, setUsername] = useState(endpoint?.username ?? '')
  const [port, setPort] = useState(endpoint?.port ?? 2083)
  const [authMode, setAuthMode] = useState<AuthMode>(modeOf(endpoint))
  const [token, setToken] = useState('')
  const [authRef, setAuthRef] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Editing a 'token' endpoint may keep the stored token (leave the field blank).
  const keepsToken = isEdit && endpoint?.auth_type === 'token'

  async function handleSubmit(event: FormEvent) {
    event.preventDefault()
    setSubmitting(true)
    setError(null)
    try {
      const authType: AuthType =
        authMode === 'direct'
          ? 'token'
          : authMode === 'env'
            ? 'token_ref'
            : 'mock'
      const payload: EndpointUpdate = {
        label: role === 'source' ? 'Sorgente' : 'Destinazione',
        host: host.trim(),
        port,
        username: username.trim(),
        auth_type: authType,
        auth_ref: authMode === 'env' ? authRef.trim() : null,
        token: authMode === 'direct' && token.trim() !== '' ? token.trim() : null,
      }
      const saved =
        isEdit && endpoint
          ? await updateEndpoint(endpoint.id, payload)
          : await createEndpoint(migrationId, { role, ...payload })
      onSaved(saved)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Errore sconosciuto')
      setSubmitting(false)
    }
  }

  const credentialFilled =
    authMode === 'mock' ||
    (authMode === 'direct' && (token.trim() !== '' || keepsToken)) ||
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
            placeholder={
              keepsToken
                ? 'lascia vuoto per non cambiare il token'
                : 'incolla qui il token cPanel'
            }
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
      <div className="form__actions">
        {isEdit && onCancel && (
          <button
            type="button"
            className="btn btn--ghost"
            onClick={onCancel}
            disabled={submitting}
          >
            Annulla
          </button>
        )}
        <button type="submit" className="btn btn--primary" disabled={!canSubmit}>
          {submitting
            ? 'Salvataggio…'
            : isEdit
              ? 'Salva modifiche'
              : `Salva ${ROLE_LABEL[role]}`}
        </button>
      </div>
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
