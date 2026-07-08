import type {
  CoverageEntry,
  CoverageMatrix,
  CoverageStatus,
  InventoryOverview,
  InventorySnapshot,
} from '../../lib/api'

interface Props {
  overview: InventoryOverview | null
}

const ROLE_TITLE: Record<string, string> = {
  source: 'Sorgente',
  destination: 'Destinazione',
}

// Fixed display order: implemented categories first, unverified (P2) last.
const CATEGORY_ORDER: [string, string][] = [
  ['domains', 'Domini'],
  ['account', 'Account'],
  ['email_accounts', 'Email'],
  ['databases', 'Database'],
  ['dns_records', 'DNS'],
  ['cron_jobs', 'Cron'],
  ['ssl', 'SSL'],
  ['email_forwarders', 'Inoltri email'],
  ['email_autoresponders', 'Risponditori'],
  ['ftp_accounts', 'FTP'],
  ['redirects', 'Redirect'],
  ['email_filters', 'Filtri email'],
  ['mailing_lists', 'Mailing list'],
  ['php_settings', 'PHP'],
  ['postgres_databases', 'PostgreSQL'],
  ['subaccounts', 'Subaccount'],
]

// Deliberately avoids "tutto letto / completo": every non-ok status is explicit.
const STATUS_META: Record<CoverageStatus, { label: string; cls: string }> = {
  succeeded: { label: 'Letto', cls: 'cov--ok' },
  empty: { label: 'Vuoto', cls: 'cov--neutral' },
  partial: { label: 'Parziale', cls: 'cov--warn' },
  unsupported: { label: 'Non supportato', cls: 'cov--warn' },
  unavailable: { label: 'Non disponibile', cls: 'cov--warn' },
  failed: { label: 'Fallito', cls: 'cov--err' },
  unverified: { label: 'Non verificato', cls: 'cov--muted' },
}

function getCoverage(snapshot: InventorySnapshot | null): CoverageMatrix | null {
  const data = snapshot?.data as Record<string, unknown> | null | undefined
  const coverage = data?.coverage
  return coverage && typeof coverage === 'object'
    ? (coverage as CoverageMatrix)
    : null
}

function CoverageRow({ label, entry }: { label: string; entry: CoverageEntry }) {
  const meta = STATUS_META[entry.status] ?? {
    label: entry.status,
    cls: 'cov--muted',
  }
  return (
    <tr>
      <td>{label}</td>
      <td>
        <span className={`cov-badge ${meta.cls}`}>{meta.label}</span>
      </td>
      <td className="cov-method">{entry.method ?? '—'}</td>
      <td className="cov-count">
        {entry.items_count === null ? '—' : entry.items_count}
      </td>
      <td className="cov-msg">{entry.message ?? ''}</td>
    </tr>
  )
}

function CoverageColumn({ snapshot }: { snapshot: InventorySnapshot | null }) {
  if (!snapshot) {
    return <p className="hint">Nessun inventario ancora rilevato.</p>
  }
  const coverage = getCoverage(snapshot)
  if (!coverage) {
    return (
      <p className="hint">
        Copertura non disponibile per questo snapshot (rileggi l'inventario).
      </p>
    )
  }
  const rows = CATEGORY_ORDER.filter(([key]) => coverage[key]).map(
    ([key, label]) => (
      <CoverageRow key={key} label={label} entry={coverage[key]} />
    ),
  )
  return (
    <div className="cov-table-wrap">
      <table className="cov-table">
        <thead>
          <tr>
            <th>Categoria</th>
            <th>Stato</th>
            <th>Metodo</th>
            <th>Count</th>
            <th>Note</th>
          </tr>
        </thead>
        <tbody>{rows}</tbody>
      </table>
    </div>
  )
}

export default function InventoryCoveragePanel({ overview }: Props) {
  const hasAny = Boolean(overview && (overview.source || overview.destination))
  if (!hasAny) return null
  return (
    <section className="panel">
      <div className="panel__title">Copertura inventory</div>
      <p className="hint">
        Cosa è stato letto in sola lettura, cosa è vuoto, non supportato, non
        verificato o fallito. Le categorie non leggibili non generano falsi
        blocchi nel confronto.
      </p>
      <div className="setup-grid">
        {(['source', 'destination'] as const).map((role) => (
          <div className="panel" key={role}>
            <div className="panel__title">{ROLE_TITLE[role]}</div>
            <CoverageColumn snapshot={overview ? overview[role] : null} />
          </div>
        ))}
      </div>
    </section>
  )
}
