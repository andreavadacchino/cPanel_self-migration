import { useState, type FormEvent } from 'react'
import { createMigration, type Migration } from '../../lib/api'

interface Props {
  onCreated: (migration: Migration) => void
  onCancel: () => void
}

export default function CreateMigrationForm({ onCreated, onCancel }: Props) {
  const [name, setName] = useState('')
  const [domain, setDomain] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function handleSubmit(event: FormEvent) {
    event.preventDefault()
    setSubmitting(true)
    setError(null)
    try {
      const migration = await createMigration(name.trim(), domain.trim())
      onCreated(migration)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Errore sconosciuto')
      setSubmitting(false)
    }
  }

  const canSubmit = name.trim() !== '' && domain.trim() !== '' && !submitting

  return (
    <form className="panel form form--floating" onSubmit={handleSubmit}>
      <div className="form__title">Nuova migrazione</div>
      <label className="field">
        <span className="field__label">Nome</span>
        <input
          className="input"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="Sito Acme"
          autoFocus
        />
      </label>
      <label className="field">
        <span className="field__label">Dominio</span>
        <input
          className="input"
          value={domain}
          onChange={(e) => setDomain(e.target.value)}
          placeholder="acme.example"
        />
      </label>
      {error && <div className="state-msg state-msg--error">{error}</div>}
      <div className="form__actions">
        <button type="button" className="btn btn--ghost" onClick={onCancel}>
          Annulla
        </button>
        <button type="submit" className="btn btn--primary" disabled={!canSubmit}>
          {submitting ? 'Creazione…' : 'Crea migrazione'}
        </button>
      </div>
    </form>
  )
}
