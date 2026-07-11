import { useEffect, useState } from 'react'
import {
  fetchManualTasks,
  updateManualTask,
  verifyManualTask,
  type ManualTask,
  type ManualTaskStatus,
} from '../../lib/api'

const STATUS_LABEL: Record<ManualTaskStatus, string> = {
  pending: 'Da fare',
  in_progress: 'In corso',
  done: 'Completata',
  skipped: 'Esclusa',
}

export default function ManualTasksPanel({ migrationId }: { migrationId: number }) {
  const [tasks, setTasks] = useState<ManualTask[]>([])
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState<number | null>(null)

  async function reload() {
    try {
      setTasks(await fetchManualTasks(migrationId))
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Errore checklist')
    }
  }

  useEffect(() => {
    void reload()
  }, [migrationId])

  async function change(task: ManualTask, status: ManualTaskStatus) {
    setBusy(task.id)
    try {
      const updated = await updateManualTask(task.id, status)
      setTasks((current) => current.map((item) => item.id === updated.id ? updated : item))
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Errore aggiornamento')
    } finally {
      setBusy(null)
    }
  }

  async function verify(task: ManualTask) {
    setBusy(task.id)
    try {
      const updated = await verifyManualTask(task.id)
      setTasks((current) => current.map((item) => item.id === updated.id ? updated : item))
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Verifica non riuscita')
    } finally {
      setBusy(null)
    }
  }

  return (
    <section className="panel">
      <div className="panel__head">
        <div>
          <div className="panel__title">Attività manuali</div>
          <p className="hint">Completa l’attività, riesegui preflight e comparativa, quindi verifica con nuove evidenze.</p>
        </div>
        <button className="btn" onClick={() => void reload()}>Aggiorna</button>
      </div>
      {error && <div className="state-msg state-msg--error">{error}</div>}
      {tasks.length === 0 ? <p className="hint">Nessuna attività manuale generata.</p> : (
        <div className="manual-tasks">
          {tasks.map((task) => (
            <article className="manual-task" key={task.id}>
              <div className="manual-task__body">
                <strong>{task.title}</strong>
                <p>{task.instructions}</p>
                <span className="badge">{STATUS_LABEL[task.status]}</span>{' '}
                <span className="badge">Verifica: {task.verification_status}</span>
              </div>
              <div className="manual-task__actions">
                <select
                  className="input"
                  value={task.status}
                  disabled={busy === task.id}
                  onChange={(event) => void change(task, event.target.value as ManualTaskStatus)}
                >
                  {Object.entries(STATUS_LABEL).map(([value, label]) => <option value={value} key={value}>{label}</option>)}
                </select>
                <button className="btn btn--primary" disabled={task.status !== 'done' || busy === task.id} onClick={() => void verify(task)}>
                  Verifica
                </button>
              </div>
            </article>
          ))}
        </div>
      )}
    </section>
  )
}
