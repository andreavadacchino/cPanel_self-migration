import { useEffect, useMemo, useState } from 'react'
import {
  fetchComparison,
  generateComparison,
  type ComparisonReport,
  type Severity,
} from '../../lib/api'
import ComparisonSummaryCards from './ComparisonSummaryCards'
import ComparisonEntriesTable from './ComparisonEntriesTable'

const CATEGORY_LABEL: Record<string, string> = {
  domains: 'Domini',
  email_accounts: 'Email',
  databases: 'Database',
  mysql_users: 'Utente MySQL',
  cron_jobs: 'Cron',
  ssl: 'SSL',
  capabilities: 'Capability',
}

type SeverityFilter = Severity | 'all'

export default function ComparisonPanel({
  migrationId,
  canGenerate,
  blockedReason,
  onReportChanged,
}: {
  migrationId: number
  canGenerate: boolean
  blockedReason: string
  onReportChanged?: (report: ComparisonReport) => void
}) {
  const [report, setReport] = useState<ComparisonReport | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [severity, setSeverity] = useState<SeverityFilter>('all')
  const [category, setCategory] = useState<string>('all')

  useEffect(() => {
    let active = true
    fetchComparison(migrationId).then((r) => {
      if (active) setReport(r)
    })
    return () => {
      active = false
    }
  }, [migrationId])

  async function handleGenerate() {
    setLoading(true)
    setError(null)
    try {
      const generated = await generateComparison(migrationId)
      setReport(generated)
      onReportChanged?.(generated)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Errore sconosciuto')
    } finally {
      setLoading(false)
    }
  }

  const categories = report?.summary?.categories ?? []
  const filtered = useMemo(() => {
    const entries = report?.entries ?? []
    return entries.filter(
      (e) =>
        (severity === 'all' || e.severity === severity) &&
        (category === 'all' || e.category === category),
    )
  }, [report, severity, category])

  return (
    <section className="panel">
      <div className="panel__head">
        <div className="panel__title">Comparativa (sola lettura)</div>
        <button
          className="btn btn--primary"
          onClick={handleGenerate}
          disabled={loading || !canGenerate}
        >
          {loading ? 'Generazione…' : report ? 'Aggiorna comparazione' : 'Genera comparazione'}
        </button>
      </div>

      {error && <div className="state-msg state-msg--error">{error}</div>}

      {!report && !error && (
        <p className="hint">
          {canGenerate
            ? "Confronta l'inventario di sorgente e destinazione per vedere cosa manca, cosa è diverso e cosa può bloccare una migrazione futura."
            : blockedReason}
        </p>
      )}

      {report && report.summary && (
        <>
          <ComparisonSummaryCards summary={report.summary} />

          <div className="cmp-filters">
            <label className="field__label">Severità</label>
            <select
              className="input"
              value={severity}
              onChange={(e) => setSeverity(e.target.value as SeverityFilter)}
            >
              <option value="all">Tutte</option>
              <option value="blocker">Blocchi critici</option>
              <option value="warning">Avvisi</option>
              <option value="info">Informazioni</option>
            </select>

            <label className="field__label">Categoria</label>
            <select
              className="input"
              value={category}
              onChange={(e) => setCategory(e.target.value)}
            >
              <option value="all">Tutte</option>
              {categories.map((c) => (
                <option value={c} key={c}>
                  {CATEGORY_LABEL[c] ?? c}
                </option>
              ))}
            </select>
          </div>

          <ComparisonEntriesTable entries={filtered} />
        </>
      )}
    </section>
  )
}
