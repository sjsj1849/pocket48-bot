import { useCallback, useEffect, useState } from 'react'
import { ArrowRight, ChevronDown, RefreshCw, RotateCw } from 'lucide-react'
import { api } from '../api'
import type { OverviewData, Page, ServiceState } from '../types'
import { ErrorState } from '../App'

function ServiceRow({ service }: { service: ServiceState }) {
  const [open, setOpen] = useState(false)
  return (
    <div className={`service-row ${open ? 'expanded' : ''}`}>
      <button className="service-main" onClick={() => setOpen((value) => !value)}>
        <span className={`status-dot ${service.status}`} />
        <span className="service-name"><strong>{service.name}</strong><small>{service.subtitle}</small></span>
        <span className={`status-pill ${service.status}`}>{service.statusText}</span>
        <span className="service-uptime">{service.uptime}</span>
        <ChevronDown size={17} className="chevron" />
      </button>
      <div className="service-detail"><span>{service.detail}</span><span>{service.lastEvent}</span><time>{service.lastTime || '暂无记录'}</time></div>
    </div>
  )
}

function formatPercent(value: number) {
  if (!Number.isFinite(value)) return '0.0'
  return value.toFixed(1)
}

function Meter({ label, value }: { label: string; value: number }) {
  const safe = Number.isFinite(value) ? Math.min(100, Math.max(0, value)) : 0
  return (
    <div className="meter">
      <div><span>{label}</span><strong>{formatPercent(safe)}%</strong></div>
      <div className="meter-track"><i style={{ width: `${safe}%` }} /></div>
    </div>
  )
}

export function Overview({ onNavigate }: { onNavigate: (page: Page) => void }) {
  const [data, setData] = useState<OverviewData | null>(null)
  const [error, setError] = useState<unknown>(null)
  const [refreshing, setRefreshing] = useState(false)
  const load = useCallback(async (quiet = false) => {
    if (!quiet) setRefreshing(true)
    try { setData(await api<OverviewData>('overview')); setError(null) } catch (reason) { setError(reason) } finally { setRefreshing(false) }
  }, [])
  useEffect(() => {
    void load()
    // 5s keeps CPU/memory meters responsive without hammering the admin API.
    const timer = window.setInterval(() => void load(true), 5_000)
    return () => window.clearInterval(timer)
  }, [load])

  async function restart() {
    setRefreshing(true)
    try { await api('service/restart', { method: 'POST' }); window.setTimeout(() => void load(), 1800) } catch (reason) { setError(reason); setRefreshing(false) }
  }

  if (error && !data) return <main className="page-content"><ErrorState error={error} retry={() => void load()} /></main>
  if (!data) return <main className="page-content"><div className="skeleton hero-skeleton" /></main>
  const healthy = data.services.filter((service) => service.status === 'healthy').length
  return (
    <main className="page-content">
      <section className="page-heading">
        <div><p className="eyebrow">OPERATIONS OVERVIEW</p><h1>运行总览</h1><p>实时转发、认证会话与系统资源。</p></div>
        <div className="heading-actions"><span className="updated">更新于 {data.updatedAt}</span><button className="icon-button" onClick={() => void load()} title="刷新" aria-label="刷新"><RefreshCw size={18} className={refreshing ? 'spin' : ''} /></button><button className="secondary-button" onClick={() => void restart()}><RotateCw size={16} />重启 Bot</button></div>
      </section>
      {data.attention.length ? <section className="attention-band"><div><span>需要处理</span><strong>{data.attention[0].title}</strong><p>{data.attention[0].description}</p></div><button onClick={() => onNavigate(data.attention[0].target)}>{data.attention[0].action}<ArrowRight size={16} /></button></section> : null}
      <section className="section-block">
        <div className="section-title"><div><h2>服务健康</h2><p>{healthy}/{data.services.length} 个服务运行正常</p></div><span className="quiet-badge">自动刷新</span></div>
        <div className="service-table">{data.services.map((service) => <ServiceRow key={service.id} service={service} />)}</div>
      </section>
      <div className="overview-grid">
        <section className="section-block activity-block"><div className="section-title"><div><h2>最近活动</h2><p>关键事件与消息流转</p></div><button className="text-button" onClick={() => onNavigate('logs')}>查看日志 <ArrowRight size={15} /></button></div><div className="activity-list">{data.activity.length ? data.activity.map((item, index) => <div className="activity-item" key={`${item.time}-${index}`}><time>{item.time}</time><span className={`activity-mark ${item.level}`} /><div><strong>{item.source}</strong><p>{item.message}</p></div></div>) : <p className="muted">暂无关键事件</p>}</div></section>
        <section className="section-block resources-block">
          <div className="section-title">
            <div>
              <h2>系统资源</h2>
              <p>{data.resources.os} · CPU 为瞬时占用 · 每 5 秒刷新</p>
            </div>
          </div>
          <div className="resource-meters">
            <Meter label="CPU" value={data.resources.cpuPercent} />
            <Meter label="内存" value={data.resources.memoryPercent} />
            <Meter label="磁盘" value={data.resources.diskPercent} />
          </div>
          <div className="uptime-stat"><span>系统运行时间</span><strong>{data.resources.uptime}</strong></div>
        </section>
      </div>
    </main>
  )
}
