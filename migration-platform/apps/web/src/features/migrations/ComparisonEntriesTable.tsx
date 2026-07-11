import type { ComparisonEntry } from '../../lib/api'
import SeverityBadge from './SeverityBadge'
import StateBadge from './StateBadge'

const CATEGORY_LABEL: Record<string, string> = {
  domains: 'Domini',
  email_accounts: 'Email',
  databases: 'Database',
  mysql_users: 'Utente MySQL',
  cron_jobs: 'Cron',
  ssl: 'SSL',
  capabilities: 'Capability',
}

export default function ComparisonEntriesTable({
  entries,
}: {
  entries: ComparisonEntry[]
}) {
  if (entries.length === 0) {
    return <p className="hint">Nessuna differenza per i filtri selezionati.</p>
  }
  return (
    <div className="cmp-table-wrap">
      <table className="cmp-table">
        <thead>
          <tr>
            <th>Severità</th>
            <th>Categoria</th>
            <th>Elemento</th>
            <th>Stato</th>
            <th>Dettaglio</th>
          </tr>
        </thead>
        <tbody>
          {entries.map((e) => (
            <tr key={`${e.category}:${e.key}`}>
              <td>
                <SeverityBadge severity={e.severity} />
              </td>
              <td>{CATEGORY_LABEL[e.category] ?? e.category}</td>
              <td className="cmp-table__key">{e.key}</td>
              <td>
                <StateBadge state={e.state} />
              </td>
              <td className="cmp-table__msg">{e.message}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
