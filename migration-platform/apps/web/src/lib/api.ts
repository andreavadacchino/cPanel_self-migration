const BASE_URL = import.meta.env.VITE_API_BASE_URL ?? 'http://localhost:8000'

export type EndpointRole = 'source' | 'destination'
export type AuthType = 'none' | 'token_ref' | 'password_ref' | 'mock'
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
  limitations: string[]
}

export interface Endpoint {
  id: number
  migration_id: number
  role: EndpointRole
  label: string | null
  host: string
  port: number
  username: string
  auth_type: AuthType
  // Sprint 2: the opaque auth_ref is never returned — only this flag.
  has_auth_ref: boolean
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

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${BASE_URL}${path}`, {
    headers: { 'Content-Type': 'application/json' },
    ...init,
  })
  if (!response.ok) {
    let detail = `Errore API (${response.status})`
    try {
      const body = (await response.json()) as { detail?: string }
      if (body?.detail) detail = body.detail
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
