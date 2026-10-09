export type Platform = 'qq' | 'feishu' | 'telegram'
export type Page = 'overview' | 'platforms' | 'targets' | 'routes' | 'deliveries' | 'settings'

export interface Target {
  id: string
  name: string
  platform: Platform
  kind: 'group' | 'private'
  address: string
  favorite: boolean
  description?: string
}

export interface Route {
  id: string
  name: string
  project: string
  event: string
  legacyPlatform?: string
  legacyKind?: string
  legacyAddress?: string
  targetIds: string[]
  enabled: boolean
}

export interface GatewayConfig {
  address: string
  apiKey: string
  qq: { enabled: boolean; wsUrl: string; accessToken?: string }
  feishu: { enabled: boolean; appId: string; appSecret?: string; uploadConcurrency: number; smallVideoBytes: number; videoWaitSeconds: number }
  telegram: { enabled: boolean; botToken?: string }
  targets: Target[]
  routes: Route[]
}

export interface DeliveryRecord {
  time: string
  project: string
  event: string
  summary: string
  target: string
  platform: Platform
  status: 'queued' | 'sent' | 'failed' | 'retrying'
  latencyMs: number
  error?: string
}

export interface GatewayState {
  config: GatewayConfig
  records: DeliveryRecord[]
  uptime: string
  queueDepth: number
}
