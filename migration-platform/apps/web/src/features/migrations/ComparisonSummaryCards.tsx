import type { ComparisonSummary } from '../../lib/api'

const CATEGORY_LABEL: Record<string, string> = {
  domains: 'Domini',
  email_accounts: 'Email',
  databases: 'Database',
  cron_jobs: 'Cron',
  ssl: 'SSL',
  capabilities: 'Capability',
}

export default function ComparisonSummaryCards({
  summary,
}: {
  summary: ComparisonSummary
}) {
  const cards = [
    { key: 'blocker', label: 'Blocchi critici', value: summary.blockers_count },
    { key: 'warning', label: 'Avvisi', value: summary.warnings_count },
    { key: 'info', label: 'Informazioni', value: summary.infos_count },
  ]
  return (
    <>
      <div className="summary-cards">
        {cards.map((c) => (
          <div className={`summary-card summary-card--${c.key}`} key={c.key}>
            <div className="summary-card__value">{c.value}</div>
            <div className="summary-card__label">{c.label}</div>
          </div>
        ))}
      </div>
      {summary.categories.length > 0 && (
        <p className="hint">
          Categorie confrontate:{' '}
          {summary.categories
            .map((c) => CATEGORY_LABEL[c] ?? c)
            .join(', ')}
        </p>
      )}
    </>
  )
}
