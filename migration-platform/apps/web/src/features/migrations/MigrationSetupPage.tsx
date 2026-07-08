import { useCallback, useEffect, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import {
  fetchCurrentJob,
  fetchEndpoints,
  fetchEvents,
  fetchInventory,
  fetchMigration,
  startPreflight,
  type Endpoint,
  type InventoryOverview,
  type Job,
  type JobEvent,
  type Migration,
} from '../../lib/api'
import EndpointCard from './EndpointCard'
import PreflightPanel from './PreflightPanel'
import JobStatusPanel from './JobStatusPanel'
import InventorySummaryPanel from './InventorySummaryPanel'
import InventoryCoveragePanel from './InventoryCoveragePanel'
import ComparisonPanel from './ComparisonPanel'

function isTerminal(job: Job | null): boolean {
  return job != null && (job.status === 'succeeded' || job.status === 'failed')
}

export default function MigrationSetupPage() {
  const params = useParams<{ id: string }>()
  const migrationId = Number(params.id)

  const [migration, setMigration] = useState<Migration | null>(null)
  const [endpoints, setEndpoints] = useState<Endpoint[]>([])
  const [job, setJob] = useState<Job | null>(null)
  const [events, setEvents] = useState<JobEvent[]>([])
  const [inventory, setInventory] = useState<InventoryOverview | null>(null)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [preflightError, setPreflightError] = useState<string | null>(null)
  const [starting, setStarting] = useState(false)

  const source = endpoints.find((e) => e.role === 'source')
  const destination = endpoints.find((e) => e.role === 'destination')
  const ready = Boolean(source) && Boolean(destination)

  const refreshJob = useCallback(async () => {
    const [currentJob, currentEvents, currentInventory] = await Promise.all([
      fetchCurrentJob(migrationId),
      fetchEvents(migrationId),
      fetchInventory(migrationId),
    ])
    setJob(currentJob)
    setEvents(currentEvents)
    setInventory(currentInventory)
  }, [migrationId])

  useEffect(() => {
    let active = true
    if (Number.isNaN(migrationId)) {
      setLoadError('Migrazione non valida')
      return
    }
    Promise.all([
      fetchMigration(migrationId),
      fetchEndpoints(migrationId),
      fetchCurrentJob(migrationId),
      fetchEvents(migrationId),
      fetchInventory(migrationId),
    ])
      .then(([mig, eps, currentJob, currentEvents, currentInventory]) => {
        if (!active) return
        setMigration(mig)
        setEndpoints(eps)
        setJob(currentJob)
        setEvents(currentEvents)
        setInventory(currentInventory)
      })
      .catch((err: unknown) => {
        if (active)
          setLoadError(err instanceof Error ? err.message : 'Errore sconosciuto')
      })
    return () => {
      active = false
    }
  }, [migrationId])

  // Poll while a job is in flight.
  useEffect(() => {
    if (job == null || isTerminal(job)) return
    const timer = setTimeout(() => {
      void refreshJob()
    }, 1000)
    return () => clearTimeout(timer)
  }, [job, refreshJob])

  function upsertEndpoint(endpoint: Endpoint) {
    setEndpoints((prev) => {
      const rest = prev.filter((e) => e.id !== endpoint.id)
      return [...rest, endpoint]
    })
  }

  function removeEndpoint(endpointId: number) {
    setEndpoints((prev) => prev.filter((e) => e.id !== endpointId))
  }

  async function handleStartPreflight() {
    setStarting(true)
    setPreflightError(null)
    try {
      const created = await startPreflight(migrationId)
      setJob(created)
      await refreshJob()
    } catch (err) {
      setPreflightError(
        err instanceof Error ? err.message : 'Errore sconosciuto',
      )
    } finally {
      setStarting(false)
    }
  }

  if (loadError) {
    return (
      <>
        <Link className="back-link" to="/">
          ← Migrazioni
        </Link>
        <div className="state-msg state-msg--error">{loadError}</div>
      </>
    )
  }

  return (
    <>
      <Link className="back-link" to="/">
        ← Migrazioni
      </Link>

      <div className="page-head">
        <div>
          <h1>{migration ? migration.name : 'Setup migrazione'}</h1>
          <p>{migration ? migration.domain : 'Caricamento…'}</p>
        </div>
        {migration && <span className="badge">{migration.status}</span>}
      </div>

      <div className="setup-grid">
        <EndpointCard
          migrationId={migrationId}
          role="source"
          endpoint={source}
          onChanged={upsertEndpoint}
          onRemoved={removeEndpoint}
        />
        <EndpointCard
          migrationId={migrationId}
          role="destination"
          endpoint={destination}
          onChanged={upsertEndpoint}
          onRemoved={removeEndpoint}
        />
      </div>

      <PreflightPanel
        ready={ready}
        running={starting}
        onStart={handleStartPreflight}
        error={preflightError}
      />

      <JobStatusPanel job={job} events={events} />

      <InventorySummaryPanel overview={inventory} />

      <InventoryCoveragePanel overview={inventory} />

      <ComparisonPanel migrationId={migrationId} />
    </>
  )
}
