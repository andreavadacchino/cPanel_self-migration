import type { InventorySnapshot, InventoryOverview } from '../../lib/api'

interface Props {
  overview: InventoryOverview | null
}

const ROLE_TITLE: Record<string, string> = {
  source: 'Sorgente',
  destination: 'Destinazione',
}

function count(value: number | null): string {
  return value === null ? '—' : String(value)
}

function SnapshotColumn({ snapshot }: { snapshot: InventorySnapshot | null }) {
  if (!snapshot) {
    return <p className="hint">Nessun inventario ancora rilevato.</p>
  }
  const s = snapshot.summary
  if (snapshot.status === 'failed' || !s) {
    return (
      <div className="state-msg state-msg--error">
        {snapshot.error ?? 'Lettura inventario fallita.'}
      </div>
    )
  }
  return (
    <>
      <dl className="kv">
        <div>
          <dt>Domini</dt>
          <dd>{count(s.domains_count)}</dd>
        </div>
        <div>
          <dt>Email</dt>
          <dd>{count(s.email_accounts_count)}</dd>
        </div>
        <div>
          <dt>Database</dt>
          <dd>{count(s.databases_count)}</dd>
        </div>
        <div>
          <dt>Cron</dt>
          <dd>{count(s.cron_jobs_count)}</dd>
        </div>
        <div>
          <dt>SSL</dt>
          <dd>{count(s.ssl_items_count)}</dd>
        </div>
        <div>
          <dt>DNS</dt>
          <dd>{count(s.dns_records_count)}</dd>
        </div>
      </dl>
      {s.warnings_count > 0 && (
        <div className="hint">
          {s.warnings_count} avviso/i (alcune letture non disponibili).
        </div>
      )}
    </>
  )
}

export default function InventorySummaryPanel({ overview }: Props) {
  const hasAny = Boolean(overview && (overview.source || overview.destination))
  return (
    <section className="panel">
      <div className="panel__title">Inventario (sola lettura)</div>
      {!hasAny && (
        <p className="hint">
          Il preflight legge l'inventario di sorgente e destinazione. Avvialo per
          popolare questa sezione.
        </p>
      )}
      {hasAny && (
        <div className="setup-grid">
          {(['source', 'destination'] as const).map((role) => (
            <div className="panel" key={role}>
              <div className="panel__title">{ROLE_TITLE[role]}</div>
              <SnapshotColumn
                snapshot={overview ? overview[role] : null}
              />
            </div>
          ))}
        </div>
      )}
    </section>
  )
}
