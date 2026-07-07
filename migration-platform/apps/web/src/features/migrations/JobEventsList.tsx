import type { JobEvent } from '../../lib/api'

export default function JobEventsList({ events }: { events: JobEvent[] }) {
  if (events.length === 0) {
    return <p className="hint">Nessun evento ancora.</p>
  }
  return (
    <ul className="events">
      {events.map((event) => (
        <li key={event.id} className={`events__item events__item--${event.level}`}>
          <span className="events__phase">{event.phase ?? '—'}</span>
          <span className="events__msg">{event.message}</span>
          {event.progress != null && (
            <span className="events__progress">{event.progress}%</span>
          )}
        </li>
      ))}
    </ul>
  )
}
