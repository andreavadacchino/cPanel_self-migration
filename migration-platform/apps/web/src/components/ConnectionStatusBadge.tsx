import type { ConnectionStatus } from '../lib/api'

const LABELS: Record<ConnectionStatus, string> = {
  unknown: 'Non testato',
  testing: 'Test in corso…',
  connected: 'Connesso',
  failed: 'Fallito',
}

export default function ConnectionStatusBadge({
  status,
}: {
  status: ConnectionStatus
}) {
  return (
    <span className={`status status--${status}`}>
      <span className="status__dot" />
      {LABELS[status]}
    </span>
  )
}
