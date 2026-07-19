export type Page = 'overview' | 'config' | 'docs' | 'browser' | 'logs'

export interface ServiceState {
  id: string
  name: string
  subtitle: string
  status: 'healthy' | 'attention' | 'down'
  statusText: string
  uptime: string
  detail: string
  lastEvent: string
  lastTime: string
}

export interface OverviewData {
  updatedAt: string
  services: ServiceState[]
  activity: Array<{ time: string; level: string; source: string; message: string }>
  resources: { cpuPercent: number; memoryPercent: number; diskPercent: number; uptime: string; os: string }
  attention: Array<{ id: string; title: string; description: string; action: string; target: Page }>
}

export interface ConfigField {
  key: string
  group: string
  label: string
  description: string
  kind: 'string' | 'secret' | 'boolean' | 'integer' | 'stringList'
  value: unknown
  configured?: boolean
  restartRequired?: boolean
}
