const BASE_URL = import.meta.env.VITE_API_BASE_URL ?? 'http://localhost:8000'

export type EndpointRole = 'source' | 'destination'
export type AuthType = 'none' | 'token' | 'token_ref' | 'password_ref' | 'mock'
export type ConnectionStatus = 'unknown' | 'testing' | 'connected' | 'failed'
export type JobStatus = 'pending' | 'queued' | 'running' | 'succeeded' | 'failed'

export interface Migration {
  id: number
  name: string
  domain: string
  status: string
  created_at: string
  updated_at: string
}

export interface Capabilities {
  source: string
  can_connect: boolean
  can_authenticate: boolean
  can_read_account_info: boolean
  can_read_domains: boolean
  can_read_email: boolean
  can_read_databases: boolean
  can_read_cron: boolean
  can_read_dns: boolean
  can_read_ssl: boolean
  can_read_forwarders: boolean
  can_read_autoresponders: boolean
  can_read_ftp: boolean
  limitations: string[]
}

// Coverage matrix (Sprint 3.5): one entry per inventory category, carried in
// InventorySnapshot.data.coverage. Finer-grained than the boolean can_read_*.
export type CoverageStatus =
  | 'succeeded'
  | 'empty'
  | 'partial'
  | 'unsupported'
  | 'unavailable'
  | 'failed'
  | 'unverified'

export interface CoverageEntry {
  status: CoverageStatus
  method: string | null
  read_only_verified: boolean
  items_count: number | null
  message: string | null
}

export type CoverageMatrix = Record<string, CoverageEntry>

export interface Endpoint {
  id: number
  migration_id: number
  role: EndpointRole
  label: string | null
  host: string
  port: number
  username: string
  auth_type: AuthType
  // The opaque auth_ref and the encrypted token are never returned — only flags.
  has_auth_ref: boolean
  has_auth_secret: boolean
  verify_tls: boolean
  connection_status: ConnectionStatus
  last_checked_at: string | null
  last_error: string | null
  capabilities: Capabilities | null
  created_at: string
  updated_at: string
}

export interface EndpointCreate {
  role: EndpointRole
  label?: string | null
  host: string
  port: number
  username: string
  auth_type: AuthType
  auth_ref?: string | null
  // Plaintext token for auth_type 'token' — sent once, never returned.
  token?: string | null
  // False skips TLS certificate verification (self-signed / mismatched certs).
  verify_tls?: boolean
}

// Edit an existing endpoint (role is immutable). token is optional: omit it to
// keep the stored one when auth_type stays 'token'.
export interface EndpointUpdate {
  label?: string | null
  host: string
  port: number
  username: string
  auth_type: AuthType
  auth_ref?: string | null
  token?: string | null
  verify_tls?: boolean
}

export interface Job {
  id: number
  migration_id: number | null
  type: string
  status: JobStatus
  current_phase: string | null
  progress_percent: number
  created_at: string
  started_at: string | null
  finished_at: string | null
  error: string | null
}

export interface JobEvent {
  id: number
  job_id: number
  level: string
  phase: string | null
  message: string
  progress: number | null
  created_at: string
}

export interface InventorySummary {
  domains_count: number | null
  email_accounts_count: number | null
  databases_count: number | null
  cron_jobs_count: number | null
  dns_records_count: number | null
  ssl_items_count: number | null
  warnings_count: number
}

export interface InventorySnapshot {
  id: number
  migration_id: number
  endpoint_id: number
  endpoint_role: EndpointRole
  status: string
  captured_at: string | null
  summary: InventorySummary | null
  data: Record<string, unknown> | null
  error: string | null
  created_at: string
  updated_at: string
}

export interface InventoryOverview {
  source: InventorySnapshot | null
  destination: InventorySnapshot | null
}

// Comparison (Sprint 3)
export type Severity = 'blocker' | 'warning' | 'info'
export type ComparisonEntryState =
  | 'match'
  | 'missing_on_destination'
  | 'only_on_destination'
  | 'different'
  | 'unknown'

export interface ComparisonSide {
  exists: boolean
  fingerprint: string | null
}

export interface ComparisonEntry {
  category: string
  key: string
  state: ComparisonEntryState
  severity: Severity
  title: string
  message: string
  source: ComparisonSide
  destination: ComparisonSide
}

export interface ComparisonCategoryStats {
  source: number
  destination: number
  match: number
  blocker: number
  warning: number
  info: number
  // True when a read-capability gap made a per-item comparison unreliable.
  skipped?: boolean
}

export interface ComparisonSummary {
  blockers_count: number
  warnings_count: number
  infos_count: number
  categories: string[]
  by_category: Record<string, ComparisonCategoryStats>
}

export interface ComparisonReport {
  id: number
  migration_id: number
  source_snapshot_id: number | null
  destination_snapshot_id: number | null
  status: string
  summary: ComparisonSummary | null
  entries: ComparisonEntry[]
  blockers_count: number
  warnings_count: number
  infos_count: number
  error: string | null
  created_at: string
  updated_at: string
}

export type ManualTaskStatus = 'pending' | 'in_progress' | 'done' | 'skipped'
export type VerificationStatus = 'pending' | 'verified' | 'failed'

export interface ManualTask {
  id: number
  migration_id: number
  comparison_report_id: number
  category: string
  item_key: string
  title: string
  instructions: string
  status: ManualTaskStatus
  verification_status: VerificationStatus
  created_at: string
  updated_at: string
}

export interface MigrationPlanStep {
  id: string
  category: string
  key: string
  title: string
  mode: 'automatic' | 'approval' | 'secret_required' | 'manual' | 'excluded'
  reason: string
  state: string
  comparison_state?: ComparisonEntryState
  severity: Severity
  depends_on_categories: string[]
}

export interface MigrationPlan {
  id: number
  migration_id: number
  comparison_report_id: number
  status: string
  summary: Record<string, number>
  steps: MigrationPlanStep[]
  error: string | null
  created_at: string
  updated_at: string
}

export type WriterReadinessStatus = 'not_ready' | 'needs_inventory' | 'needs_contract_test' | 'needs_operator_input' | 'eligible_for_real_design'
export interface ReadinessGap { code: string; message: string }
export interface WriterReadinessCategory {
  category: string
  status: WriterReadinessStatus
  source_coverage: CoverageStatus
  destination_coverage: CoverageStatus
  step_count: number
  gaps: ReadinessGap[]
}
export interface WriterReadinessStep {
  step_id: string
  category: string
  mode: MigrationPlanStep['mode']
  status: WriterReadinessStatus
  depends_on_categories: string[]
  gaps: ReadinessGap[]
}
export interface WriterReadinessReport {
  id: number
  migration_id: number
  plan_id: number
  comparison_report_id: number
  source_snapshot_id: number
  destination_snapshot_id: number
  status: WriterReadinessStatus
  summary: Record<string, number>
  global_blockers: ReadinessGap[]
  categories: WriterReadinessCategory[]
  steps: WriterReadinessStep[]
  created_at: string
}

export type ExecutionStatus = 'previewed' | 'awaiting_confirmation' | 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled'

export interface ExecutionPreviewItem {
  step_id: string
  category: string
  target: 'destination'
  call: { api: string; module: string; function: string; arguments: Record<string, string> }
  mode: 'dry-run'
  will_write: false
}

export interface ExecutionEvent {
  id: number
  level: string
  phase: string
  step_id: string | null
  message: string
  planned_call: Record<string, unknown> | null
  result: Record<string, unknown> | null
  verification: Record<string, unknown> | null
  created_at: string
}

export interface ExecutionRun {
  id: number
  migration_id: number
  plan_id: number
  comparison_report_id: number
  source_snapshot_id: number
  destination_snapshot_id: number
  destination_endpoint_id: number
  status: ExecutionStatus
  dry_run: true
  selected_step_ids: string[]
  preview: ExecutionPreviewItem[]
  provided_secret_step_ids: string[]
  requested_by: string | null
  confirmation_phrase: string
  confirmed_at: string | null
  destination_validated_at: string | null
  started_at: string | null
  finished_at: string | null
  error: string | null
  created_at: string
  updated_at: string
  events: ExecutionEvent[]
}

// FastAPI reports errors in `detail`, which is a string for our domain errors
// (404/409/422 raised by services) but a *list* of {loc,msg,...} objects for
// request-validation errors. Coerce every shape to a readable string so the UI
// never shows "[object Object]".
function formatApiError(detail: unknown, status: number): string {
  if (typeof detail === 'string') return detail
  if (Array.isArray(detail)) {
    const msgs = detail
      .map((d) =>
        d && typeof d === 'object' && 'msg' in d
          ? String((d as { msg: unknown }).msg)
          : JSON.stringify(d),
      )
      .filter(Boolean)
    if (msgs.length > 0) return msgs.join('; ')
  }
  if (detail && typeof detail === 'object' && 'msg' in detail) {
    return String((detail as { msg: unknown }).msg)
  }
  return `Errore API (${status})`
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${BASE_URL}${path}`, {
    headers: { 'Content-Type': 'application/json' },
    ...init,
  })
  if (!response.ok) {
    let detail = `Errore API (${response.status})`
    try {
      const body = (await response.json()) as { detail?: unknown }
      if (body?.detail !== undefined && body.detail !== null) {
        detail = formatApiError(body.detail, response.status)
      }
    } catch {
      // response had no JSON body; keep the status-based message
    }
    throw new Error(detail)
  }
  if (response.status === 204) return undefined as T
  return (await response.json()) as T
}

// Migrations
export function fetchMigrations(): Promise<Migration[]> {
  return request<Migration[]>('/api/migrations')
}

export function fetchMigration(id: number): Promise<Migration> {
  return request<Migration>(`/api/migrations/${id}`)
}

export function createMigration(
  name: string,
  domain: string,
): Promise<Migration> {
  return request<Migration>('/api/migrations', {
    method: 'POST',
    body: JSON.stringify({ name, domain }),
  })
}

// Endpoints
export function fetchEndpoints(migrationId: number): Promise<Endpoint[]> {
  return request<Endpoint[]>(`/api/migrations/${migrationId}/endpoints`)
}

export function createEndpoint(
  migrationId: number,
  payload: EndpointCreate,
): Promise<Endpoint> {
  return request<Endpoint>(`/api/migrations/${migrationId}/endpoints`, {
    method: 'POST',
    body: JSON.stringify(payload),
  })
}

export function testConnection(endpointId: number): Promise<Endpoint> {
  return request<Endpoint>(`/api/endpoints/${endpointId}/test-connection`, {
    method: 'POST',
  })
}

export function updateEndpointCredentials(
  endpointId: number,
  token: string,
): Promise<Endpoint> {
  return request<Endpoint>(`/api/endpoints/${endpointId}/credentials`, {
    method: 'PATCH',
    body: JSON.stringify({ token }),
  })
}

export function updateEndpoint(
  endpointId: number,
  payload: EndpointUpdate,
): Promise<Endpoint> {
  return request<Endpoint>(`/api/endpoints/${endpointId}`, {
    method: 'PATCH',
    body: JSON.stringify(payload),
  })
}

export function deleteEndpoint(endpointId: number): Promise<void> {
  return request<void>(`/api/endpoints/${endpointId}`, { method: 'DELETE' })
}

// Preflight
export function startPreflight(migrationId: number): Promise<Job> {
  return request<Job>(`/api/migrations/${migrationId}/preflight`, {
    method: 'POST',
  })
}

export async function fetchCurrentJob(migrationId: number): Promise<Job | null> {
  try {
    return await request<Job>(`/api/migrations/${migrationId}/jobs/current`)
  } catch {
    // 404 → no job yet; treat as "no current job".
    return null
  }
}

export function fetchEvents(migrationId: number): Promise<JobEvent[]> {
  return request<JobEvent[]>(`/api/migrations/${migrationId}/events`)
}

// Inventory
export function fetchInventory(migrationId: number): Promise<InventoryOverview> {
  return request<InventoryOverview>(`/api/migrations/${migrationId}/inventory`)
}

// Comparison
export function generateComparison(
  migrationId: number,
): Promise<ComparisonReport> {
  return request<ComparisonReport>(
    `/api/migrations/${migrationId}/comparison`,
    { method: 'POST' },
  )
}

export async function fetchComparison(
  migrationId: number,
): Promise<ComparisonReport | null> {
  try {
    return await request<ComparisonReport>(
      `/api/migrations/${migrationId}/comparison`,
    )
  } catch {
    // 404 → no comparison generated yet.
    return null
  }
}

export function fetchManualTasks(migrationId: number): Promise<ManualTask[]> {
  return request<ManualTask[]>(`/api/migrations/${migrationId}/manual-tasks`)
}

export function updateManualTask(
  taskId: number,
  status: ManualTaskStatus,
): Promise<ManualTask> {
  return request<ManualTask>(`/api/manual-tasks/${taskId}`, {
    method: 'PATCH',
    body: JSON.stringify({ status }),
  })
}

export function verifyManualTask(taskId: number): Promise<ManualTask> {
  return request<ManualTask>(`/api/manual-tasks/${taskId}/verify`, {
    method: 'POST',
  })
}

export function generateMigrationPlan(migrationId: number): Promise<MigrationPlan> {
  return request<MigrationPlan>(`/api/migrations/${migrationId}/plan`, { method: 'POST' })
}

export async function fetchMigrationPlan(migrationId: number): Promise<MigrationPlan | null> {
  try {
    return await request<MigrationPlan>(`/api/migrations/${migrationId}/plan`)
  } catch {
    return null
  }
}

export function generateWriterReadiness(migrationId: number, planId: number): Promise<WriterReadinessReport> {
  return request<WriterReadinessReport>(`/api/migrations/${migrationId}/writer-readiness?plan_id=${planId}`, { method: 'POST' })
}

export async function fetchWriterReadiness(migrationId: number): Promise<WriterReadinessReport | null> {
  try { return await request<WriterReadinessReport>(`/api/migrations/${migrationId}/writer-readiness`) }
  catch { return null }
}

export function createExecutionPreview(migrationId: number, payload: { plan_id: number; selected_step_ids: string[]; passwords: Record<string, string>; requested_by?: string }): Promise<ExecutionRun> {
  return request<ExecutionRun>(`/api/migrations/${migrationId}/executions`, { method: 'POST', body: JSON.stringify(payload) })
}

export async function fetchLatestExecution(migrationId: number): Promise<ExecutionRun | null> {
  try { return await request<ExecutionRun>(`/api/migrations/${migrationId}/executions/latest`) }
  catch { return null }
}

export function confirmExecution(runId: number, planId: number, confirmationPhrase: string): Promise<ExecutionRun> {
  return request<ExecutionRun>(`/api/executions/${runId}/confirm`, { method: 'POST', body: JSON.stringify({ plan_id: planId, confirmation_phrase: confirmationPhrase }) })
}

export function runExecution(runId: number): Promise<ExecutionRun> {
  return request<ExecutionRun>(`/api/executions/${runId}/run`, { method: 'POST' })
}

export function cancelExecution(runId: number): Promise<ExecutionRun> {
  return request<ExecutionRun>(`/api/executions/${runId}/cancel`, { method: 'POST' })
}
