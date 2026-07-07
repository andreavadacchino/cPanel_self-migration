export interface Migration {
  id: number
  name: string
  domain: string
  status: string
  created_at: string
  updated_at: string
}

const BASE_URL = import.meta.env.VITE_API_BASE_URL ?? 'http://localhost:8000'

export async function fetchMigrations(): Promise<Migration[]> {
  const response = await fetch(`${BASE_URL}/api/migrations`)
  if (!response.ok) {
    throw new Error(`Errore API (${response.status})`)
  }
  return (await response.json()) as Migration[]
}
