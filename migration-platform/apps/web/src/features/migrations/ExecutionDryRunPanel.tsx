import { useEffect, useMemo, useState } from 'react'
import {
  cancelExecution, confirmExecution, createExecutionPreview, fetchLatestExecution,
  fetchMigrationPlan, runExecution, type ExecutionRun, type MigrationPlan,
} from '../../lib/api'

const STATUS: Record<string, string> = {
  previewed: 'Anteprima', awaiting_confirmation: 'In attesa di conferma', queued: 'Confermato, in coda',
  running: 'Simulazione in corso', succeeded: 'Simulazione completata', failed: 'Fallito', cancelled: 'Annullato',
}

export default function ExecutionDryRunPanel({ migrationId, planRevision, onRunChanged }: { migrationId: number; planRevision?: number; onRunChanged?: (run: ExecutionRun) => void }) {
  const [plan, setPlan] = useState<MigrationPlan | null>(null)
  const [run, setRun] = useState<ExecutionRun | null>(null)
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [passwords, setPasswords] = useState<Record<string, string>>({})
  const [phrase, setPhrase] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    void Promise.all([fetchMigrationPlan(migrationId), fetchLatestExecution(migrationId)]).then(([nextPlan, latest]) => {
      setPlan(nextPlan); setRun(latest)
    })
  }, [migrationId, planRevision])

  const selectable = useMemo(() => plan?.steps.filter((step) => !['manual', 'excluded'].includes(step.mode)) ?? [], [plan])
  function toggle(id: string) { setSelected((current) => { const next = new Set(current); next.has(id) ? next.delete(id) : next.add(id); return next }) }
  async function action(work: () => Promise<ExecutionRun>) {
    setBusy(true); setError(null)
    try { const nextRun = await work(); setRun(nextRun); onRunChanged?.(nextRun) } catch (err) { setError(err instanceof Error ? err.message : 'Errore dry-run') }
    finally { setBusy(false) }
  }

  if (!plan) return <section className="panel"><div className="panel__title">Esecutore sicuro</div><p className="hint">Genera prima un piano di migrazione.</p></section>

  return <section className="panel">
    <div className="panel__head"><div><div className="panel__title">Esecutore sicuro · solo dry-run</div><p className="hint">Nessun writer è abilitato. Piano {plan.id}, comparazione {plan.comparison_report_id}.</p></div>{run && <span className="badge">{STATUS[run.status] ?? run.status}</span>}</div>
    <div className="state-msg"><strong>Garanzia corrente:</strong> tutte le chiamate sono simulate e puntano soltanto alla destinazione. Nessuna scrittura verrà inviata.</div>
    {error && <div className="state-msg state-msg--error">{error}</div>}

    {(!run || ['succeeded','failed','cancelled'].includes(run.status)) && <>
      <div className="cmp-table-wrap"><table className="cmp-table"><thead><tr><th>Seleziona</th><th>Modalità</th><th>Passo</th><th>Credenziale</th></tr></thead><tbody>
        {selectable.map((step) => <tr key={step.id}><td><input type="checkbox" checked={selected.has(step.id)} onChange={() => toggle(step.id)} aria-label={`Seleziona ${step.title}`} /></td><td>{step.mode}</td><td><div>{step.title}</div><small className="hint">{step.id}</small></td><td>{step.mode === 'secret_required' ? <input type="password" autoComplete="new-password" value={passwords[step.id] ?? ''} onChange={(e) => setPasswords((old) => ({ ...old, [step.id]: e.target.value }))} placeholder="Nuova password" aria-label={`Nuova password per ${step.title}`} /> : 'Non richiesta'}</td></tr>)}
      </tbody></table></div>
      <button className="btn btn--primary" disabled={busy || selected.size === 0} onClick={() => void action(() => createExecutionPreview(migrationId, { plan_id: plan.id, selected_step_ids: [...selected], passwords }))}>Crea anteprima dry-run</button>
    </>}

    {run && !['succeeded','failed','cancelled'].includes(run.status) && <>
      <h3>Chiamate previste ({run.preview.length})</h3>
      <div className="cmp-table-wrap"><table className="cmp-table"><thead><tr><th>Passo</th><th>Target</th><th>Chiamata simulata</th><th>Scrive?</th></tr></thead><tbody>
        {run.preview.map((item) => <tr key={item.step_id}><td>{item.step_id}</td><td>{item.target}</td><td><code>{item.call.module}::{item.call.function}</code></td><td>No</td></tr>)}
      </tbody></table></div>
      {run.status === 'awaiting_confirmation' && <div className="execution-confirm"><label><strong>Conferma forte</strong><span className="hint"> Digita esattamente: <code>{run.confirmation_phrase}</code></span><input value={phrase} onChange={(e) => setPhrase(e.target.value)} placeholder="Frase esatta" /></label><button className="btn btn--primary" disabled={busy || phrase !== run.confirmation_phrase} onClick={() => void action(() => confirmExecution(run.id, run.plan_id, phrase))}>Conferma piano e valida destinazione</button></div>}
      {run.status === 'queued' && <button className="btn btn--primary" disabled={busy} onClick={() => void action(() => runExecution(run.id))}>Esegui simulazione senza scritture</button>}
      {['awaiting_confirmation','queued'].includes(run.status) && <button className="btn" disabled={busy} onClick={() => void action(() => cancelExecution(run.id))}>Annulla questo dry-run</button>}
    </>}

    {run?.status === 'succeeded' && <div className="state-msg"><strong>Dry-run completato.</strong> {run.events.filter((event) => event.phase === 'step').length} chiamate simulate, zero scritture.</div>}
  </section>
}
