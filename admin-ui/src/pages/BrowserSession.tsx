import { useCallback, useEffect, useRef, useState } from 'react'
import { Expand, Monitor, MousePointer2, RefreshCw } from 'lucide-react'
import { api, apiEndpoint } from '../api'
import { ErrorState } from '../App'

type BrowserStatus = { available: boolean; running: boolean; display: string; message: string }
type QualityMode = 'smooth' | 'sharp'
type RFBSession = { disconnect: () => void; scaleViewport: boolean; resizeSession: boolean; viewOnly: boolean; qualityLevel: number; compressionLevel: number }

function applyQuality(session: RFBSession, mode: QualityMode) {
  session.qualityLevel = mode === 'smooth' ? 3 : 7
  session.compressionLevel = mode === 'smooth' ? 5 : 3
}

export function BrowserSession() {
  const screen = useRef<HTMLDivElement>(null)
  const rfb = useRef<RFBSession | null>(null)
  const [status, setStatus] = useState<BrowserStatus | null>(null)
  const [error, setError] = useState<unknown>(null)
  const [connecting, setConnecting] = useState(false)
  const [xBusy, setXBusy] = useState(false)
  const [xMessage, setXMessage] = useState('')
  const [xConfigured, setXConfigured] = useState(false)
  const [xLoginMessage, setXLoginMessage] = useState('')
  const [quality, setQuality] = useState<QualityMode>('smooth')
  const load = useCallback(async () => { try { setStatus(await api<BrowserStatus>('browser/status')); setError(null) } catch (reason) { setError(reason) } }, [])
  useEffect(() => { void load(); return () => rfb.current?.disconnect() }, [load])
  useEffect(() => {
    let mounted = true
    async function refreshX() {
      try {
        const value = await api<{sessionConfigured: boolean; loginStatus?: {message: string}}>('browser/x')
        if (mounted) { setXConfigured(value.sessionConfigured); setXLoginMessage(value.loginStatus?.message || '') }
      } catch {}
    }
    void refreshX()
    const timer = window.setInterval(() => void refreshX(), 10000)
    return () => { mounted = false; window.clearInterval(timer) }
  }, [])
  async function connect() {
    if (!screen.current) return
    setConnecting(true); setError(null)
    try {
      await api('browser/session', { method: 'POST' })
      const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:'
      const websocketURL = apiEndpoint('browser/ws').replace(/^https?:/, protocol)
      const { default: RFB } = await import('@novnc/novnc')
      rfb.current?.disconnect()
      screen.current.replaceChildren()
      const session = new RFB(screen.current, websocketURL)
      session.scaleViewport = true
      session.resizeSession = false
      session.viewOnly = false
      applyQuality(session, quality)
      rfb.current = session
      session.addEventListener('connect', () => { setConnecting(false); void load() })
      session.addEventListener('disconnect', (event: Event & { detail?: { clean?: boolean } }) => { setConnecting(false); if (!event.detail?.clean) setError(new Error('浏览器连接已中断，请重新连接')) })
    } catch (reason) { setError(reason); setConnecting(false) }
  }
  function changeQuality(mode: QualityMode) {
    setQuality(mode)
    if (rfb.current) applyQuality(rfb.current, mode)
  }
  function fullscreen() { void screen.current?.requestFullscreen?.() }
  async function xAction(action: 'open' | 'sync') {
    setXBusy(true); setError(null); setXMessage('')
    try {
      if (action === 'open' && !rfb.current) await connect()
      await api('browser/x', { method: 'POST', body: JSON.stringify({ action }) })
      if (action === 'sync') setXConfigured(true)
      setXMessage(action === 'open' ? '请在下方 X 网页输入邮箱，完成邮箱验证码或密码验证；登录后点击「同步 X 登录态」。' : 'X 登录态已保存。接下来验证时间线采集；保存会话不代表监控已运行。')
    } catch (reason) { setError(reason) }
    finally { setXBusy(false) }
  }
  if (error && !status) return <main className="page-content"><ErrorState error={error} retry={() => void load()} /></main>
  return (
    <main className="page-content browser-page">
      <section className="page-heading"><div><p className="eyebrow">INTERACTIVE AUTHENTICATION</p><h1>浏览器</h1><p>在隔离会话中完成二维码、滑块与登录确认。</p></div><div className="heading-actions"><span className={`status-pill ${status?.available ? 'healthy' : 'attention'}`}>{status?.available ? '桌面可用' : '等待侧卡'}</span><button className="icon-button" onClick={() => void load()} title="刷新状态"><RefreshCw size={18} /></button></div></section>
      <section className="browser-toolbar"><div><Monitor size={17} /><span>{status?.message || '正在检查浏览器桌面'}</span>{status?.display ? <code>{status.display}</code> : null}</div><div><div className="quality-control" aria-label="画面质量"><button className={quality === 'smooth' ? 'active' : ''} onClick={() => changeQuality('smooth')}>流畅</button><button className={quality === 'sharp' ? 'active' : ''} onClick={() => changeQuality('sharp')}>清晰</button></div><button className="secondary-button" onClick={() => void connect()} disabled={!status?.available || connecting}><MousePointer2 size={16} />{connecting ? '连接中…' : rfb.current ? '重新连接' : '打开会话'}</button><button className="icon-button" onClick={fullscreen} title="全屏" aria-label="全屏"><Expand size={18} /></button></div></section>
      <section className="browser-toolbar x-browser-auth"><div><button className="primary-button" disabled={!status?.available || xBusy || connecting} onClick={() => void xAction('open')}>X 登录</button><button className="secondary-button" disabled={!status?.available || xBusy} onClick={() => void xAction('sync')}>同步 X 登录态</button><span className={`status-pill ${xConfigured ? 'healthy' : 'attention'}`}>{xConfigured ? '会话已保存' : '等待登录'}</span></div><p>在下方网页完成邮箱验证码验证，登录后保存会话供监控使用。</p></section>
      {xMessage ? <p role="status">{xMessage}</p> : null}
      {xLoginMessage ? <p role="status">{xLoginMessage}</p> : null}
      {error ? <div className="inline-error">{error instanceof Error ? error.message : '浏览器连接失败'}</div> : null}
      <section className="browser-canvas" ref={screen}><div className="browser-placeholder"><div className="browser-symbol"><Monitor size={28} /></div><strong>{status?.available ? '浏览器桌面已就绪' : '等待浏览器侧卡启动'}</strong><p>{status?.available ? '打开会话后可直接扫码或拖动验证滑块' : 'Bot 启动统一浏览器侧卡后，这里会自动检测到会话'}</p>{status?.available ? <button className="primary-button" onClick={() => void connect()}><MousePointer2 size={16} />打开交互会话</button> : null}</div></section>
    </main>
  )
}
