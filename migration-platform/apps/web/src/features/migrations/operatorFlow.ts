import type {
  ComparisonReport,
  Endpoint,
  ExecutionRun,
  InventoryOverview,
  Job,
  MigrationPlan,
  WriterReadinessReport,
} from '../../lib/api'

export type MigrationStage = 'connections' | 'inventory' | 'comparison' | 'plan' | 'readiness' | 'execution'
export type FlowState = 'idle' | 'action' | 'running' | 'complete' | 'stale' | 'blocked'

export interface StageState {
  id: MigrationStage
  state: FlowState
  label: string
  detail: string
}

export interface OperatorAction {
  stage: MigrationStage
  label: string
  title: string
  description: string
  tone: 'primary' | 'warning' | 'danger'
}

export interface OperatorFlow {
  stages: Record<MigrationStage, StageState>
  recommended: OperatorAction
  rerunPreflight: 'not_needed' | 'recommended' | 'required'
}

interface Input {
  endpoints: Endpoint[]
  job: Job | null
  inventory: InventoryOverview | null
  comparison: ComparisonReport | null
  plan: MigrationPlan | null
  readiness: WriterReadinessReport | null
  execution: ExecutionRun | null
}

const stage = (id: MigrationStage, state: FlowState, label: string, detail: string): StageState => ({ id, state, label, detail })

export function buildOperatorFlow(input: Input): OperatorFlow {
  const source = input.endpoints.find((endpoint) => endpoint.role === 'source')
  const destination = input.endpoints.find((endpoint) => endpoint.role === 'destination')
  const connected = source?.connection_status === 'connected' && destination?.connection_status === 'connected'
  const inventoryReady = Boolean(input.inventory?.source?.summary && input.inventory.destination?.summary)
  const snapshotIds = [input.inventory?.source?.id, input.inventory?.destination?.id]
  const comparisonCurrent = Boolean(
    input.comparison && snapshotIds.includes(input.comparison.source_snapshot_id ?? undefined) && snapshotIds.includes(input.comparison.destination_snapshot_id ?? undefined),
  )
  const planCurrent = Boolean(input.plan && input.comparison && input.plan.comparison_report_id === input.comparison.id)
  const readinessCurrent = Boolean(
    planCurrent && input.readiness && input.plan && input.readiness.plan_id === input.plan.id &&
    input.readiness.source_snapshot_id === input.inventory?.source?.id &&
    input.readiness.destination_snapshot_id === input.inventory?.destination?.id,
  )
  const executionCurrent = Boolean(readinessCurrent && input.execution && input.plan && input.execution.plan_id === input.plan.id)
  const latestEndpointUpdate = Math.max(...[source?.updated_at, destination?.updated_at].filter(Boolean).map((value) => new Date(value as string).getTime()), 0)
  const oldestSnapshot = Math.min(...[input.inventory?.source?.captured_at, input.inventory?.destination?.captured_at].filter(Boolean).map((value) => new Date(value as string).getTime()), Number.POSITIVE_INFINITY)
  const endpointChanged = inventoryReady && latestEndpointUpdate > oldestSnapshot
  const rerunPreflight = endpointChanged ? 'required' : input.job?.status === 'failed' ? 'recommended' : inventoryReady ? 'not_needed' : 'required'

  const stages: OperatorFlow['stages'] = {
    connections: stage('connections', connected ? 'complete' : 'action', connected ? 'Connesse' : 'Azione richiesta', connected ? 'Entrambi gli endpoint sono raggiungibili' : 'Configura e testa sorgente e destinazione'),
    inventory: stage('inventory', endpointChanged ? 'stale' : input.job && !['succeeded', 'failed'].includes(input.job.status) ? 'running' : inventoryReady ? 'complete' : connected ? 'action' : 'blocked', endpointChanged ? 'Da aggiornare' : inventoryReady ? 'Corrente' : 'Da eseguire', endpointChanged ? 'Gli endpoint sono cambiati dopo l’ultima lettura' : inventoryReady ? `Snapshot ${snapshotIds.filter(Boolean).join(' / ')}` : 'Richiede endpoint connessi'),
    comparison: stage('comparison', !inventoryReady ? 'blocked' : !comparisonCurrent ? (input.comparison ? 'stale' : 'action') : 'complete', comparisonCurrent ? 'Corrente' : input.comparison ? 'Obsoleta' : 'Da generare', comparisonCurrent ? `Report ${input.comparison?.id}` : 'Richiede inventario corrente'),
    plan: stage('plan', !comparisonCurrent ? 'blocked' : !planCurrent ? (input.plan ? 'stale' : 'action') : 'complete', planCurrent ? 'Corrente' : input.plan ? 'Obsoleto' : 'Da generare', planCurrent ? `Piano ${input.plan?.id}` : 'Dipende dalla comparazione corrente'),
    readiness: stage('readiness', !planCurrent ? 'blocked' : !readinessCurrent ? (input.readiness ? 'stale' : 'action') : 'complete', !planCurrent ? 'Bloccata' : readinessCurrent ? 'Corrente' : input.readiness ? 'Obsoleta' : 'Da generare', readinessCurrent ? `Report ${input.readiness?.id}` : 'Dipende dal piano corrente'),
    execution: stage('execution', !readinessCurrent ? 'blocked' : input.execution && ['queued', 'running', 'awaiting_confirmation'].includes(input.execution.status) ? 'running' : executionCurrent ? 'complete' : 'action', !readinessCurrent ? 'Bloccata' : executionCurrent ? 'Disponibile' : 'Da preparare', executionCurrent ? `Dry-run ${input.execution?.id} · ${input.execution?.status}` : 'Solo simulazione, zero scritture'),
  }

  let recommended: OperatorAction
  if (!source || !destination) recommended = { stage: 'connections', label: 'Configura gli endpoint', title: 'Completa sorgente e destinazione', description: 'Servono entrambi gli endpoint prima di poter iniziare qualsiasi lettura.', tone: 'primary' }
  else if (!connected) recommended = { stage: 'connections', label: 'Testa le connessioni', title: 'Verifica gli endpoint', description: 'Entrambe le connessioni devono risultare valide prima del preflight.', tone: 'primary' }
  else if (endpointChanged || !inventoryReady || input.job?.status === 'failed') recommended = { stage: 'connections', label: endpointChanged ? 'Aggiorna il preflight' : 'Avvia il preflight', title: endpointChanged ? 'Le evidenze a valle sono obsolete' : 'Acquisisci l’inventario read-only', description: endpointChanged ? 'Un endpoint è cambiato: preflight, comparazione, piano e readiness devono essere rigenerati.' : 'Legge sorgente e destinazione senza effettuare scritture.', tone: endpointChanged ? 'warning' : 'primary' }
  else if (input.job && !['succeeded', 'failed'].includes(input.job.status)) recommended = { stage: 'connections', label: 'Segui il preflight', title: 'Preflight in esecuzione', description: 'Attendi il completamento: le fasi successive saranno sbloccate automaticamente.', tone: 'primary' }
  else if (!comparisonCurrent) recommended = { stage: 'comparison', label: input.comparison ? 'Aggiorna la comparazione' : 'Genera la comparazione', title: 'Confronta le evidenze correnti', description: 'Identifica differenze, blocker e risorse presenti soltanto su uno dei due endpoint.', tone: input.comparison ? 'warning' : 'primary' }
  else if (!planCurrent) recommended = { stage: 'plan', label: input.plan ? 'Aggiorna il piano' : 'Genera il piano', title: 'Trasforma le differenze in decisioni', description: 'Classifica ogni elemento come automatico, da approvare, manuale o escluso.', tone: input.plan ? 'warning' : 'primary' }
  else if (!readinessCurrent) recommended = { stage: 'readiness', label: input.readiness ? 'Aggiorna la readiness' : 'Genera la readiness', title: 'Verifica evidenze e gap', description: 'Controlla quali passi sono realmente candidabili e quali richiedono intervento.', tone: input.readiness ? 'warning' : 'primary' }
  else {
    const blockedSteps = input.readiness?.steps.filter((item) => item.status === 'not_ready').length ?? 0
    if (blockedSteps > 0) recommended = { stage: 'readiness', label: `Rivedi ${blockedSteps} passi non pronti`, title: 'Decisioni operative ancora necessarie', description: 'Esamina prima i passi bloccati o manuali; gli elementi idonei restano disponibili per il dry-run.', tone: 'warning' }
    else if (!executionCurrent) recommended = { stage: 'execution', label: 'Prepara il dry-run', title: 'Simula i passi idonei', description: 'Seleziona le operazioni e verifica l’anteprima. Nessun writer reale è abilitato.', tone: 'primary' }
    else if (input.execution?.status === 'awaiting_confirmation') recommended = { stage: 'execution', label: 'Completa la conferma', title: 'Anteprima pronta per la conferma', description: 'Controlla le chiamate simulate e inserisci la frase richiesta.', tone: 'warning' }
    else if (input.execution?.status === 'queued') recommended = { stage: 'execution', label: 'Esegui la simulazione', title: 'Dry-run confermato', description: 'Avvia la simulazione senza scritture sulla destinazione.', tone: 'primary' }
    else recommended = { stage: 'execution', label: 'Rivedi il dry-run', title: 'Percorso guidato completato', description: 'Consulta evidenze ed eventi prima di decidere il prossimo incremento operativo.', tone: 'primary' }
  }

  if (stages[recommended.stage].state === 'complete') stages[recommended.stage] = { ...stages[recommended.stage], state: 'action', label: 'Azione richiesta' }
  return { stages, recommended, rerunPreflight }
}
