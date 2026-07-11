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
import ManualTasksPanel from './ManualTasksPanel'
import MigrationPlanPanel from './MigrationPlanPanel'
import ExecutionDryRunPanel from './ExecutionDryRunPanel'

function isTerminal(job: Job | null): boolean {
  return job != null && (job.status === 'succeeded' || job.status === 'failed')
}

function jobLabel(job: Job | null): string {
  if (!job) return 'Non avviato'
  if (job.status === 'succeeded') return 'Completato'
  if (job.status === 'failed') return 'Richiede attenzione'
  if (job.status === 'running') return 'In esecuzione'
  if (job.status === 'queued') return 'In coda'
  return 'In attesa'
}

function inventoryLabel(overview: InventoryOverview | null): string {
  if (!overview || (!overview.source && !overview.destination)) return 'Da leggere'
  if (overview.source?.status === 'failed' || overview.destination?.status === 'failed') {
    return 'Errore lettura'
  }
  if (overview.source && overview.destination) return 'Disponibile'
  return 'Parziale'
}

function hasCompleteInventory(overview: InventoryOverview | null): boolean {
  return Boolean(
    overview?.source?.summary &&
      overview.destination?.summary &&
      overview.source.status !== 'failed' &&
      overview.destination.status !== 'failed',
  )
}

function nextActionLabel(ready: boolean, job: Job | null, inventory: InventoryOverview | null): string {
  if (!ready) return 'Configura entrambi gli endpoint'
  if (!job) return 'Avvia il preflight read-only'
  if (job.status === 'failed') return 'Leggi errore job e correggi endpoint'
  if (!isTerminal(job)) return 'Attendi completamento preflight'
  if (inventoryLabel(inventory) !== 'Disponibile') return 'Verifica copertura inventory'
  return 'Genera o rivedi la comparativa'
}

function latestEvent(events: JobEvent[]): string {
  const event = events[events.length - 1]
  if (!event) return 'Nessun evento registrato'
  return event.message
}

function MigrationDirector({
  ready,
  configured,
  source,
  destination,
  job,
  events,
  inventory,
}: {
  ready: boolean
  configured: boolean
  source: Endpoint | undefined
  destination: Endpoint | undefined
  job: Job | null
  events: JobEvent[]
  inventory: InventoryOverview | null
}) {
  const connected = [source, destination].filter(
    (endpoint) => endpoint?.connection_status === 'connected',
  ).length
  const inventoryState = inventoryLabel(inventory)
  const jobState = jobLabel(job)
  const progress = job?.progress_percent ?? 0

  return (
    <section className="director-panel" aria-label="Stato operativo migrazione">
      <div className="director-panel__main">
        <span className="director-panel__eyebrow">Prossima azione</span>
        <h2>{nextActionLabel(ready, job, inventory)}</h2>
        <p>{latestEvent(events)}</p>
        <div className="director-rail" aria-label="Avanzamento preflight">
          <div
            className="director-rail__bar"
            style={{ width: `${Math.max(6, progress)}%` }}
          />
        </div>
      </div>
      <div className="director-panel__stats">
        <div className="director-stat">
          <span>Endpoint</span>
          <strong>{connected}/2</strong>
          <small>{ready ? 'Connessi' : configured ? 'Da testare' : 'Setup incompleto'}</small>
        </div>
        <div className="director-stat">
          <span>Preflight</span>
          <strong>{jobState}</strong>
          <small>{job?.current_phase ?? 'Nessuna fase attiva'}</small>
        </div>
        <div className="director-stat">
          <span>Inventory</span>
          <strong>{inventoryState}</strong>
          <small>Sorgente + destinazione</small>
        </div>
      </div>
    </section>
  )
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
  const [planRevision, setPlanRevision] = useState(0)

  const source = endpoints.find((e) => e.role === 'source')
  const destination = endpoints.find((e) => e.role === 'destination')
  const configured = Boolean(source) && Boolean(destination)
  const ready = Boolean(
    source?.connection_status === 'connected' &&
      destination?.connection_status === 'connected',
  )
  const comparisonReady = hasCompleteInventory(inventory)

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

      <MigrationDirector
        ready={ready}
        configured={configured}
        source={source}
        destination={destination}
        job={job}
        events={events}
        inventory={inventory}
      />

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
        configured={configured}
        ready={ready}
        running={starting}
        hasRun={Boolean(job)}
        onStart={handleStartPreflight}
        error={preflightError}
      />

      <JobStatusPanel job={job} events={events} />

      <InventorySummaryPanel overview={inventory} />

      <InventoryCoveragePanel overview={inventory} />

      <ComparisonPanel
        migrationId={migrationId}
        canGenerate={comparisonReady}
        blockedReason="Completa un preflight con inventario disponibile su sorgente e destinazione prima di generare la comparativa."
      />
      <ManualTasksPanel migrationId={migrationId} />
      <MigrationPlanPanel migrationId={migrationId} onPlanChanged={() => setPlanRevision((value) => value + 1)} />
      <ExecutionDryRunPanel migrationId={migrationId} planRevision={planRevision} />
    </>
  )
}
