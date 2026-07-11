import type { Job, JobEvent } from '../../lib/api'
import JobStatusBadge from '../../components/JobStatusBadge'
import JobEventsList from './JobEventsList'

interface Props {
  job: Job | null
  events: JobEvent[]
}

export default function JobStatusPanel({ job, events }: Props) {
  return (
    <section className="panel">
      <header className="panel__head">
        <div className="panel__title">Stato preflight</div>
        {job && <JobStatusBadge status={job.status} />}
      </header>

      {!job ? (
        <p className="hint">Nessun job avviato per questa migrazione.</p>
      ) : (
        <>
          <div className="job-meta">
            <span>
              Fase: <strong>{job.current_phase ?? '—'}</strong>
            </span>
            <span>Job #{job.id}</span>
          </div>
          <div className="progress">
            <div
              className="progress__bar"
              style={{ width: `${job.progress_percent}%` }}
            />
          </div>
          {job.error && (
            <div className="state-msg state-msg--error">{job.error}</div>
          )}
        </>
      )}

      <div className="events-block">
        <div className="events-block__title">Eventi</div>
        <JobEventsList events={events} />
      </div>
    </section>
  )
}
