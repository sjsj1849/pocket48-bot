// signSnapshot：签到监控的「内部快照页」，不出现在导航里。
// 供 Playwright 截图生成 PNG —— 与面板复用同一组件，视觉完全一致。
export type Page = 'overview' | 'config' | 'signMonitor' | 'docs' | 'browser' | 'logs' | 'signSnapshot'

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
