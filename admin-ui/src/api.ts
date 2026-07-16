let csrf = ''

const endpoint = (path: string) => new URL(`api/${path.replace(/^\//, '')}`, document.baseURI).toString()

export class APIError extends Error {
  constructor(message: string, public status: number) {
    super(message)
  }
}

export async function api<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers)
  if (init.body) headers.set('Content-Type', 'application/json')
  if (csrf && init.method && !['GET', 'HEAD'].includes(init.method)) headers.set('X-CSRF-Token', csrf)
  const response = await fetch(endpoint(path), { ...init, headers, credentials: 'same-origin' })
  const data = await response.json().catch(() => ({}))
  if (!response.ok) throw new APIError(data.error || `请求失败 (${response.status})`, response.status)
  if (data.csrf) csrf = data.csrf
  return data as T
}

export const apiEndpoint = endpoint
