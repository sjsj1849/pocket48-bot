import type { GatewayConfig, GatewayState } from './types'

let apiKey = localStorage.getItem('gateway-api-key') || ''
export function setAPIKey(value: string) { apiKey = value; localStorage.setItem('gateway-api-key', value) }
export function getAPIKey() { return apiKey }

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`/api/${path}`, {
    ...init,
    headers: { 'Content-Type': 'application/json', ...(apiKey ? { Authorization: `Bearer ${apiKey}` } : {}), ...init?.headers },
  })
  const payload = await response.json().catch(() => ({}))
  if (!response.ok) throw new Error(payload.error || `请求失败 (${response.status})`)
  return payload as T
}

export const api = {
  state: () => request<GatewayState>('state'),
  save: (config: GatewayConfig) => request<{ ok: boolean; restartRequired: boolean }>('config', { method: 'PUT', body: JSON.stringify(config) }),
  test: (targetId: string) => request<{ ok: boolean }>('test', { method: 'POST', body: JSON.stringify({ targetId }) }),
}
