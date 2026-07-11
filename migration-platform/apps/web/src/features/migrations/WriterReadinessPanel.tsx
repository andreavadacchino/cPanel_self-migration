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

const CATEGORY_LABELS: Record<string, string> = {
  domains: 'Domini', databases: 'Database', mysql_users: 'Utenti MySQL',
  email_forwarders: 'Forwarder', cron_jobs: 'Cron', ftp_accounts: 'Account FTP',
  mailing_lists: 'Mailing list', dns_records: 'Record DNS',
  email_autoresponders: 'Autoresponder', email_accounts: 'Caselle email',
  php_settings: 'Configurazione PHP', ssl: 'Certificati SSL', subaccounts: 'Subaccount',
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

  const eligible = report?.categories.filter((item) => item.status === 'eligible_for_real_design') ?? []
  const attention = report?.categories.filter((item) => item.status !== 'eligible_for_real_design') ?? []
  const summaryItems: { label: string; value: number; tone: string }[] = report ? [
    { label: 'Elegibili', value: report.summary.eligible_for_real_design ?? 0, tone: 'ok' },
    { label: 'Inventario', value: report.summary.needs_inventory ?? 0, tone: 'warn' },
    { label: 'Contract test', value: report.summary.needs_contract_test ?? 0, tone: 'warn' },
    { label: 'Non pronte', value: report.summary.not_ready ?? 0, tone: 'danger' },
  ] : []

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
      <div className="readiness-overview">
        <div className="readiness-overview__score">
          <span>Pronte per il design</span>
          <strong>{eligible.length}<small>/{report.categories.length}</small></strong>
          <p>Valutazione tecnica, non autorizzazione alla scrittura.</p>
        </div>
        <div className="readiness-overview__metrics">
          {summaryItems.map((item) => <div className={`readiness-metric readiness-metric--${item.tone}`} key={item.label}><strong>{item.value}</strong><span>{item.label}</span></div>)}
        </div>
      </div>
      <div className="readiness-blocker">
        <span className="readiness-blocker__mark">!</span>
        <div><strong>Barriera globale attiva</strong><p>{report.global_blockers.map((item) => item.message).join(' ')}</p></div>
      </div>
      <p className="hint">Evidenze: piano {report.plan_id}, comparazione {report.comparison_report_id}, snapshot {report.source_snapshot_id}/{report.destination_snapshot_id}.</p>
      <div className="readiness-groups">
        <section className="readiness-group readiness-group--ok"><header><span /> <div><strong>Elegibili per il design reale</strong><small>{eligible.length} categorie con evidenze complete</small></div></header><div className="readiness-category-list">{eligible.map((item) => <div className="readiness-category" key={item.category}><div><strong>{CATEGORY_LABELS[item.category] ?? item.category}</strong><small>{item.source_coverage} → {item.destination_coverage}</small></div><span>{item.step_count} passi</span></div>)}</div></section>
        <section className="readiness-group readiness-group--attention"><header><span /> <div><strong>Richiedono attenzione</strong><small>{attention.length} categorie bloccate o manuali</small></div></header><div className="readiness-category-list">{attention.map((item) => <details className="readiness-category readiness-category--detail" key={item.category}><summary><div><strong>{CATEGORY_LABELS[item.category] ?? item.category}</strong><small>{LABELS[item.status]}</small></div><span>{item.step_count} passi</span></summary><ul className="compact-list">{item.gaps.map((gap) => <li key={gap.code}>{gap.message}</li>)}</ul></details>)}</div></section>
      </div>
      <details className="readiness-technical"><summary>Apri matrice tecnica completa</summary><div className="cmp-table-wrap"><table className="cmp-table"><thead><tr><th>Categoria</th><th>Stato</th><th>Coverage S/D</th><th>Passi</th></tr></thead><tbody>{report.categories.map((item) => <tr key={item.category}><td>{CATEGORY_LABELS[item.category] ?? item.category}</td><td>{LABELS[item.status]}</td><td>{item.source_coverage} / {item.destination_coverage}</td><td>{item.step_count}</td></tr>)}</tbody></table></div></details>
      {report.steps.length > 0 && <details className="readiness-steps"><summary>Dettaglio dei {report.steps.length} passi del piano</summary>
        <div className="cmp-table-wrap"><table className="cmp-table"><thead><tr><th>Passo</th><th>Modalità</th><th>Stato</th><th>Motivi</th></tr></thead><tbody>
          {report.steps.map((step) => <tr key={step.step_id}><td className="cmp-table__key">{step.step_id}</td><td>{step.mode}</td><td>{LABELS[step.status]}</td><td><ul className="compact-list">{step.gaps.map((gap) => <li key={gap.code}>{gap.message}</li>)}</ul></td></tr>)}
        </tbody></table></div>
      </details>}
    </>}
  </section>
}
