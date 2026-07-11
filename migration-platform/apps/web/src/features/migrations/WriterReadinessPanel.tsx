import { useEffect, useState } from 'react'
import {
  fetchMigrationPlan,
  fetchWriterReadiness,
  generateWriterReadiness,
  type MigrationPlan,
  type WriterReadinessReport,
  type WriterReadinessStatus,
} from '../../lib/api'

const LABELS: Record<WriterReadinessStatus, string> = {
  not_ready: 'Non pronto',
  needs_inventory: 'Inventario richiesto',
  needs_contract_test: 'Contract test richiesto',
  needs_operator_input: 'Input operatore',
  eligible_for_real_design: 'Elegibile per design reale',
}

export default function WriterReadinessPanel({ migrationId, planRevision }: { migrationId: number; planRevision: number }) {
  const [plan, setPlan] = useState<MigrationPlan | null>(null)
  const [report, setReport] = useState<WriterReadinessReport | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    void Promise.all([fetchMigrationPlan(migrationId), fetchWriterReadiness(migrationId)])
      .then(([currentPlan, currentReport]) => { setPlan(currentPlan); setReport(currentReport) })
  }, [migrationId, planRevision])

  async function generate() {
    if (!plan) return
    setLoading(true); setError(null)
    try { setReport(await generateWriterReadiness(migrationId, plan.id)) }
    catch (err) { setError(err instanceof Error ? err.message : 'Errore readiness') }
    finally { setLoading(false) }
  }

  return <section className="panel">
    <div className="panel__head">
      <div>
        <div className="panel__title">Readiness writer reali</div>
        <p className="hint">Report read-only dei gap. Non abilita writer e non accoda esecuzioni.</p>
      </div>
      <button className="btn btn--primary" disabled={!plan || loading} onClick={() => void generate()}>
        {loading ? 'Analisi…' : 'Genera readiness'}
      </button>
    </div>
    {error && <div className="state-msg state-msg--error">{error}</div>}
    {!plan && <div className="state-msg">Genera prima un piano di migrazione.</div>}
    {report && <>
      <div className="state-msg state-msg--error">
        <strong>{LABELS[report.status]}.</strong> {report.global_blockers.map((item) => item.message).join(' ')}
      </div>
      <p className="hint">Evidenze: piano {report.plan_id}, comparazione {report.comparison_report_id}, snapshot {report.source_snapshot_id}/{report.destination_snapshot_id}.</p>
      <div className="cmp-table-wrap"><table className="cmp-table"><thead><tr><th>Categoria</th><th>Stato</th><th>Coverage S/D</th><th>Passi</th><th>Gap verificabili</th></tr></thead><tbody>
        {report.categories.map((item) => <tr key={item.category}>
          <td>{item.category}</td>
          <td><span className="badge">{LABELS[item.status]}</span></td>
          <td>{item.source_coverage} / {item.destination_coverage}</td>
          <td>{item.step_count}</td>
          <td><ul className="compact-list">{item.gaps.map((gap) => <li key={gap.code}>{gap.message}</li>)}</ul></td>
        </tr>)}
      </tbody></table></div>
      {report.steps.length > 0 && <details className="readiness-steps"><summary>Dettaglio dei {report.steps.length} passi del piano</summary>
        <div className="cmp-table-wrap"><table className="cmp-table"><thead><tr><th>Passo</th><th>Modalità</th><th>Stato</th><th>Motivi</th></tr></thead><tbody>
          {report.steps.map((step) => <tr key={step.step_id}><td className="cmp-table__key">{step.step_id}</td><td>{step.mode}</td><td>{LABELS[step.status]}</td><td><ul className="compact-list">{step.gaps.map((gap) => <li key={gap.code}>{gap.message}</li>)}</ul></td></tr>)}
        </tbody></table></div>
      </details>}
    </>}
  </section>
}
