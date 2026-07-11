import { useEffect, useMemo, useState } from 'react'
import {
  cancelExecution, confirmExecution, createExecutionPreview, fetchLatestExecution,
  fetchMigrationPlan, runExecution, type ExecutionRun, type MigrationPlan,
} from '../../lib/api'

const STATUS: Record<string, string> = {
  previewed: 'Anteprima pronta', awaiting_confirmation: 'Da confermare', queued: 'Pronta da simulare',
  running: 'Simulazione in corso', succeeded: 'Completata', failed: 'Fallita', cancelled: 'Annullata',
}
const MODE: Record<string, string> = { automatic: 'Automatica', approval: 'Con approvazione', secret_required: 'Password richiesta' }
const TERMINAL = ['succeeded', 'failed', 'cancelled']

export default function ExecutionDryRunPanel({ migrationId, planRevision, onRunChanged, onBackToReadiness }: { migrationId: number; planRevision?: number; onRunChanged?: (run: ExecutionRun) => void; onBackToReadiness?: () => void }) {
  const [plan, setPlan] = useState<MigrationPlan | null>(null)
  const [run, setRun] = useState<ExecutionRun | null>(null)
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [passwords, setPasswords] = useState<Record<string, string>>({})
  const [phrase, setPhrase] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [showNewRun, setShowNewRun] = useState(false)

  useEffect(() => {
    void Promise.all([fetchMigrationPlan(migrationId), fetchLatestExecution(migrationId)]).then(([nextPlan, latest]) => {
      setPlan(nextPlan); setRun(latest)
    })
  }, [migrationId, planRevision])

  const selectable = useMemo(() => plan?.steps.filter((step) => !['manual', 'excluded'].includes(step.mode)) ?? [], [plan])
  const secretSteps = useMemo(() => selectable.filter((step) => step.mode === 'secret_required'), [selectable])
  const missingPasswords = secretSteps.filter((step) => selected.has(step.id) && !passwords[step.id]).length
  const terminal = Boolean(run && TERMINAL.includes(run.status))
  const runCurrent = Boolean(run && plan && run.plan_id === plan.id)
  const selecting = !runCurrent || (terminal && showNewRun)
  const simulatedEvents = run?.events.filter((event) => event.phase === 'step') ?? []

  function toggle(id: string) { setSelected((current) => { const next = new Set(current); next.has(id) ? next.delete(id) : next.add(id); return next }) }
  function selectAll() { setSelected(new Set(selectable.map((step) => step.id))) }
  function clearSelection() { setSelected(new Set()); setPasswords({}) }
  async function action(work: () => Promise<ExecutionRun>) {
    setBusy(true); setError(null)
    try { const nextRun = await work(); setRun(nextRun); setShowNewRun(false); onRunChanged?.(nextRun) }
    catch (err) { setError(err instanceof Error ? err.message : 'Errore dry-run') }
    finally { setBusy(false) }
  }

  if (!plan) return <section className="panel execution-console"><div className="panel__title">Simulazione del piano</div><p className="hint">Genera prima un piano corrente per preparare il dry-run.</p>{onBackToReadiness && <button className="btn" onClick={onBackToReadiness}>Torna alla readiness</button>}</section>

  const currentStep = selecting ? 1 : run?.status === 'awaiting_confirmation' ? 2 : run?.status === 'queued' ? 3 : 4

  return <section className="panel execution-console">
    <header className="execution-hero">
      <div>
        <span className="execution-hero__eyebrow">Simulazione controllata · piano {plan.id}</span>
        <h3>Verifica il percorso prima di qualsiasi implementazione reale</h3>
        <p>Usa snapshot e piano correnti, valida dipendenze, password e destinazione, quindi registra le chiamate che verrebbero inviate.</p>
      </div>
      <div className="execution-hero__seal"><strong>0</strong><span>scritture cPanel</span></div>
    </header>

    <div className="simulation-truth">
      <div className="simulation-truth__yes"><strong>Cosa verifica</strong><span>Coerenza di piano e snapshot</span><span>Dipendenze e input richiesti</span><span>Raggiungibilità della destinazione</span><span>Sequenza delle chiamate previste</span></div>
      <div className="simulation-truth__no"><strong>Cosa non verifica</strong><span>Risposta reale dei writer cPanel</span><span>Creazione effettiva delle risorse</span><span>Verifica post-write o rollback</span><span>Tempi e limiti dell’esecuzione reale</span></div>
    </div>

    <ol className="execution-steps" aria-label="Fasi del dry-run">
      {['Seleziona', 'Controlla', 'Conferma', 'Risultato'].map((label, index) => <li className={currentStep === index + 1 ? 'is-current' : currentStep > index + 1 ? 'is-done' : ''} key={label}><span>{currentStep > index + 1 ? '✓' : index + 1}</span><strong>{label}</strong></li>)}
    </ol>

    {run && !selecting && <div className="execution-status-line"><div><span>Run #{run.id}</span><strong>{STATUS[run.status] ?? run.status}</strong></div><div><span>Evidenze</span><strong>snapshot {run.source_snapshot_id}/{run.destination_snapshot_id}</strong></div><div><span>Target</span><strong>solo destinazione</strong></div></div>}
    {error && <div className="state-msg state-msg--error">{error}</div>}
    {run && !runCurrent && <div className="execution-archive-note"><strong>Il dry-run #{run.id} è storico.</strong><span>Deriva dal piano {run.plan_id}; il piano corrente è {plan.id}. Consulta l’audit precedente solo come riferimento e prepara una nuova simulazione.</span></div>}

    {selecting && <div className="execution-selection">
      <div className="execution-section-head"><div><span>01 · Configura la simulazione</span><h3>Quali passi vuoi verificare?</h3><p>Seleziona soltanto gli elementi che vuoi includere nell’anteprima. Manuali ed esclusi non sono candidabili.</p></div><div className="execution-selection__count"><strong>{selected.size}</strong><span>di {selectable.length} selezionati</span></div></div>
      <div className="execution-toolbar"><button className="btn" onClick={selectAll} type="button">Seleziona tutti gli idonei</button><button className="btn btn--quiet" disabled={selected.size === 0} onClick={clearSelection} type="button">Azzera selezione</button><span>{missingPasswords > 0 ? `${missingPasswords} password mancanti` : selected.size > 0 ? 'Input completi' : 'Nessuna operazione selezionata'}</span></div>
      <div className="execution-step-list">{selectable.map((step) => {
        const checked = selected.has(step.id)
        return <article className={`execution-step-card ${checked ? 'is-selected' : ''}`} key={step.id}>
          <label><input type="checkbox" checked={checked} onChange={() => toggle(step.id)} /><span className="execution-step-card__check" /><span className="execution-step-card__body"><span className="execution-step-card__top"><strong>{step.title}</strong><em>{MODE[step.mode] ?? step.mode}</em></span><small>{step.category} · {step.key}</small>{step.depends_on_categories.length > 0 && <span className="execution-dependency">Dipende da: {step.depends_on_categories.join(', ')}</span>}</span></label>
          {checked && step.mode === 'secret_required' && <div className="execution-secret"><label>Nuova password<input type="password" autoComplete="new-password" value={passwords[step.id] ?? ''} onChange={(event) => setPasswords((old) => ({ ...old, [step.id]: event.target.value }))} placeholder="Richiesta per questa simulazione" /></label><small>Viene cifrata e non compare nell’audit.</small></div>}
        </article>
      })}</div>
      <footer className="execution-action-bar"><div><strong>Pronto a creare l’anteprima?</strong><span>La prossima schermata mostrerà le chiamate simulate. Non verrà eseguita alcuna scrittura.</span></div><button className="btn btn--primary" disabled={busy || selected.size === 0 || missingPasswords > 0} onClick={() => void action(() => createExecutionPreview(migrationId, { plan_id: plan.id, selected_step_ids: [...selected], passwords }))}>{busy ? 'Preparazione…' : `Crea anteprima di ${selected.size} passi`}</button></footer>
    </div>}

    {run && runCurrent && !terminal && <div className="execution-preview">
      <div className="execution-section-head"><div><span>02 · Anteprima verificabile</span><h3>{run.preview.length} chiamate pianificate, nessuna scrittura</h3><p>Controlla target e funzione prevista prima di confermare il dry-run.</p></div></div>
      <div className="execution-call-list">{run.preview.map((item, index) => <article key={item.step_id}><span>{String(index + 1).padStart(2, '0')}</span><div><strong>{item.step_id}</strong><small>{item.call.api} · {item.call.module}::{item.call.function}</small></div><em>NO WRITE</em></article>)}</div>
      {run.status === 'awaiting_confirmation' && <div className="execution-confirm-card"><div><span>03 · Conferma forte</span><h3>Rivalida piano e destinazione</h3><p>La conferma effettua un nuovo test read-only della destinazione e rifiuta evidenze diventate obsolete.</p><code>{run.confirmation_phrase}</code></div><label>Digita la frase esatta<input value={phrase} onChange={(event) => setPhrase(event.target.value)} placeholder="Conferma richiesta" /><button className="btn btn--primary" disabled={busy || phrase !== run.confirmation_phrase} onClick={() => void action(() => confirmExecution(run.id, run.plan_id, phrase))}>{busy ? 'Validazione…' : 'Conferma e valida destinazione'}</button></label></div>}
      {run.status === 'queued' && <div className="execution-action-bar execution-action-bar--ready"><div><strong>Destinazione validata. Simulazione pronta.</strong><span>Verranno creati soltanto eventi di audit con `write_performed=false`.</span></div><button className="btn btn--primary" disabled={busy} onClick={() => void action(() => runExecution(run.id))}>{busy ? 'Simulazione…' : 'Esegui simulazione del piano'}</button></div>}
      {['awaiting_confirmation','queued'].includes(run.status) && <button className="btn btn--quiet" disabled={busy} onClick={() => void action(() => cancelExecution(run.id))}>Annulla questa simulazione</button>}
    </div>}

    {run && runCurrent && terminal && !showNewRun && <div className={`execution-result execution-result--${run.status}`}>
      <div className="execution-result__mark">{run.status === 'succeeded' ? '✓' : run.status === 'cancelled' ? '—' : '!'}</div>
      <div className="execution-result__lead"><span>04 · Risultato</span><h3>{STATUS[run.status]}</h3><p>{run.status === 'succeeded' ? `Il piano ha prodotto ${simulatedEvents.length} eventi simulati. Nessuna risorsa è stata creata o modificata.` : run.error ?? 'La simulazione non è stata completata.'}</p></div>
      <div className="execution-result__facts"><div><strong>{run.preview.length}</strong><span>chiamate pianificate</span></div><div><strong>{simulatedEvents.length}</strong><span>passi simulati</span></div><div><strong>0</strong><span>scritture eseguite</span></div></div>
      <div className="execution-next"><span>Che cosa fare ora</span><h4>{run.status === 'succeeded' ? 'Rivedi l’audit e i limiti prima di procedere' : 'Correggi il problema e prepara una nuova simulazione'}</h4><p>Questo risultato conferma la coerenza dell’orchestrazione, non il successo di una migrazione reale. I writer reali restano non implementati e disabilitati.</p><div><button className="btn btn--primary" onClick={onBackToReadiness}>Torna ai passi non pronti</button><button className="btn" onClick={() => { setShowNewRun(true); clearSelection(); setPhrase('') }}>Prepara una nuova simulazione</button></div></div>
      <details className="execution-audit"><summary>Apri audit della simulazione ({run.events.length} eventi)</summary><ol>{run.events.map((event) => <li key={event.id}><span>{event.phase}</span><p>{event.message}</p></li>)}</ol></details>
    </div>}
  </section>
}
