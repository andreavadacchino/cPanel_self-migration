import { useCallback, useEffect, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import {
  fetchCurrentJob,
  fetchEndpoints,
  fetchEvents,
  fetchInventory,
  fetchComparison,
  fetchLatestExecution,
  fetchMigration,
  fetchMigrationPlan,
  fetchWriterReadiness,
  startPreflight,
  type ComparisonReport,
  type Endpoint,
  type ExecutionRun,
  type InventoryOverview,
  type Job,
  type JobEvent,
  type Migration,
  type MigrationPlan,
  type WriterReadinessReport,
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
import WriterReadinessPanel from './WriterReadinessPanel'
import { buildOperatorFlow, type MigrationStage, type OperatorFlow } from './operatorFlow'

const STAGES: { id: MigrationStage; label: string; short: string }[] = [
  { id: 'connections', label: 'Connessioni', short: 'Endpoint e preflight' },
  { id: 'inventory', label: 'Inventario', short: 'Copertura letture' },
  { id: 'comparison', label: 'Comparazione', short: 'Differenze rilevate' },
  { id: 'plan', label: 'Piano', short: 'Classificazione azioni' },
  { id: 'readiness', label: 'Readiness', short: 'Evidenze e gap' },
  { id: 'execution', label: 'Esecuzione', short: 'Solo dry-run' },
]

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
  flow,
  onRecommended,
}: {
  ready: boolean
  configured: boolean
  source: Endpoint | undefined
  destination: Endpoint | undefined
  job: Job | null
  events: JobEvent[]
  inventory: InventoryOverview | null
  flow: OperatorFlow
  onRecommended: () => void
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
        <span className="director-panel__eyebrow">Azione consigliata</span>
        <h2>{flow.recommended.title}</h2>
        <p>{flow.recommended.description}</p>
        <div className="director-panel__action-row">
          <button className={`btn btn--operator btn--operator-${flow.recommended.tone}`} onClick={onRecommended} type="button">
            {flow.recommended.label} <span aria-hidden="true">→</span>
          </button>
          <small>{latestEvent(events)}</small>
        </div>
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

function MigrationRail({
  active,
  onChange,
  flow,
}: {
  active: MigrationStage
  onChange: (stage: MigrationStage) => void
  flow: OperatorFlow
}) {
  return (
    <nav className="migration-rail" aria-label="Fasi della migrazione">
      <div className="migration-rail__line" aria-hidden="true" />
      {STAGES.map((stage, index) => {
        const phase = flow.stages[stage.id]
        const complete = phase.state === 'complete'
        const available = phase.state !== 'blocked'
        return (
          <button
            aria-current={active === stage.id ? 'step' : undefined}
            className={`migration-step migration-step--${phase.state} ${active === stage.id ? 'migration-step--active' : ''} ${complete ? 'migration-step--complete' : ''}`}
            disabled={!available}
            key={stage.id}
            onClick={() => onChange(stage.id)}
            type="button"
          >
            <span className="migration-step__index">{complete ? '✓' : String(index + 1).padStart(2, '0')}</span>
            <span className="migration-step__copy">
              <strong>{stage.label}</strong>
              <small>{phase.label}</small>
            </span>
          </button>
        )
      })}
    </nav>
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
  const [activeStage, setActiveStage] = useState<MigrationStage>('connections')
  const [comparison, setComparison] = useState<ComparisonReport | null>(null)
  const [plan, setPlan] = useState<MigrationPlan | null>(null)
  const [readiness, setReadiness] = useState<WriterReadinessReport | null>(null)
  const [execution, setExecution] = useState<ExecutionRun | null>(null)

  const source = endpoints.find((e) => e.role === 'source')
  const destination = endpoints.find((e) => e.role === 'destination')
  const configured = Boolean(source) && Boolean(destination)
  const ready = Boolean(
    source?.connection_status === 'connected' &&
      destination?.connection_status === 'connected',
  )
  const comparisonReady = hasCompleteInventory(inventory)
  const flow = buildOperatorFlow({ endpoints, job, inventory, comparison, plan, readiness, execution })

  const refreshEvidence = useCallback(async () => {
    const [nextComparison, nextPlan, nextReadiness, nextExecution] = await Promise.all([
      fetchComparison(migrationId), fetchMigrationPlan(migrationId),
      fetchWriterReadiness(migrationId), fetchLatestExecution(migrationId),
    ])
    setComparison(nextComparison); setPlan(nextPlan); setReadiness(nextReadiness); setExecution(nextExecution)
  }, [migrationId])

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
      fetchComparison(migrationId),
      fetchMigrationPlan(migrationId),
      fetchWriterReadiness(migrationId),
      fetchLatestExecution(migrationId),
    ])
      .then(([mig, eps, currentJob, currentEvents, currentInventory, currentComparison, currentPlan, currentReadiness, currentExecution]) => {
        if (!active) return
        setMigration(mig)
        setEndpoints(eps)
        setJob(currentJob)
        setEvents(currentEvents)
        setInventory(currentInventory)
        setComparison(currentComparison); setPlan(currentPlan); setReadiness(currentReadiness); setExecution(currentExecution)
      })
      .catch((err: unknown) => {
        if (active)
          setLoadError(err instanceof Error ? err.message : 'Errore sconosciuto')
      })
    return () => {
      active = false
    }
  }, [migrationId])

  useEffect(() => { void refreshEvidence() }, [planRevision, refreshEvidence])

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
        flow={flow}
        onRecommended={() => setActiveStage(flow.recommended.stage)}
      />

      <MigrationRail active={activeStage} onChange={setActiveStage} flow={flow} />

      <section className="stage-workspace" aria-live="polite">
        <header className="stage-workspace__head">
          <div>
            <span className="stage-workspace__eyebrow">Fase {String(STAGES.findIndex((stage) => stage.id === activeStage) + 1).padStart(2, '0')}</span>
            <h2>{STAGES.find((stage) => stage.id === activeStage)?.label}</h2>
          </div>
          <span className="safety-chip"><span /> Read-only · writer disabilitati</span>
        </header>

        {activeStage === 'connections' && <>
          <div className="setup-grid">
            <EndpointCard migrationId={migrationId} role="source" endpoint={source} onChanged={upsertEndpoint} onRemoved={removeEndpoint} />
            <EndpointCard migrationId={migrationId} role="destination" endpoint={destination} onChanged={upsertEndpoint} onRemoved={removeEndpoint} />
          </div>
          <PreflightPanel configured={configured} ready={ready} running={starting} hasRun={Boolean(job)} rerunState={flow.rerunPreflight} onStart={handleStartPreflight} error={preflightError} />
          <JobStatusPanel job={job} events={events} />
        </>}

        {activeStage === 'inventory' && <>
          <InventorySummaryPanel overview={inventory} />
          <InventoryCoveragePanel overview={inventory} />
        </>}

        {activeStage === 'comparison' && <>
          <ComparisonPanel migrationId={migrationId} canGenerate={comparisonReady} blockedReason="Completa un preflight con inventario disponibile su sorgente e destinazione prima di generare la comparativa." onReportChanged={setComparison} />
          <ManualTasksPanel migrationId={migrationId} />
        </>}

        {activeStage === 'plan' && <MigrationPlanPanel migrationId={migrationId} onPlanChanged={(nextPlan) => { setPlan(nextPlan); setPlanRevision((value) => value + 1) }} />}
        {activeStage === 'readiness' && <WriterReadinessPanel migrationId={migrationId} planRevision={planRevision} onReportChanged={setReadiness} />}
        {activeStage === 'execution' && <ExecutionDryRunPanel migrationId={migrationId} planRevision={planRevision} onRunChanged={setExecution} />}
      </section>
    </>
  )
}
