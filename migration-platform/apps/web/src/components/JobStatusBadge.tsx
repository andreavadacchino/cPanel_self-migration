import type { JobStatus } from '../lib/api'

const LABELS: Record<JobStatus, string> = {
  pending: 'In attesa',
  queued: 'In coda',
  running: 'In esecuzione',
  succeeded: 'Completato',
  failed: 'Fallito',
}

export default function JobStatusBadge({ status }: { status: JobStatus }) {
  return (
    <span className={`status status--job-${status}`}>
      <span className="status__dot" />
      {LABELS[status]}
    </span>
  )
}
