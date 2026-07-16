import { useCallback, useEffect, useState } from 'react'
import { Activity, Bot, LogOut, Menu, Monitor, ScrollText, Settings2, X } from 'lucide-react'
import { api, APIError } from './api'
import type { Page } from './types'
import { Overview } from './pages/Overview'
import { Configuration } from './pages/Configuration'
import { BrowserSession } from './pages/BrowserSession'
import { Logs } from './pages/Logs'

const navigation: Array<{ id: Page; label: string; icon: typeof Activity }> = [
  { id: 'overview', label: '总览', icon: Activity },
  { id: 'config', label: '配置', icon: Settings2 },
  { id: 'browser', label: '浏览器', icon: Monitor },
  { id: 'logs', label: '日志', icon: ScrollText },
]

function Login({ onLogin }: { onLogin: () => void }) {
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  async function submit(event: React.FormEvent) {
    event.preventDefault()
    setLoading(true)
    setError('')
    try {
      await api('auth/login', { method: 'POST', body: JSON.stringify({ password }) })
      onLogin()
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '登录失败')
    } finally {
      setLoading(false)
    }
  }
  return (
    <main className="login-shell">
      <section className="login-panel page-enter">
        <div className="brand-mark"><Bot size={18} /></div>
        <p className="eyebrow">PRIVATE OPERATIONS</p>
        <h1>Pocket48<br />Console</h1>
        <p className="login-copy">登录以管理实时转发、浏览器认证与服务状态。</p>
        <form onSubmit={submit}>
          <label htmlFor="password">访问密码</label>
          <input id="password" type="password" autoComplete="current-password" value={password} onChange={(event) => setPassword(event.target.value)} autoFocus />
          {error ? <p className="form-error">{error}</p> : null}
          <button className="primary-button wide" type="submit" disabled={loading || password.length < 1}>{loading ? '验证中…' : '进入控制台'}</button>
        </form>
      </section>
    </main>
  )
}

export function App() {
  const [authenticated, setAuthenticated] = useState<boolean | null>(null)
  const [page, setPage] = useState<Page>('overview')
  const [drawer, setDrawer] = useState(false)

  const verify = useCallback(async () => {
    try {
      await api('session')
      setAuthenticated(true)
    } catch {
      setAuthenticated(false)
    }
  }, [])

  useEffect(() => { void verify() }, [verify])

  async function logout() {
    try { await api('auth/logout', { method: 'POST' }) } finally { setAuthenticated(false) }
  }

  function navigate(next: Page) {
    setPage(next)
    setDrawer(false)
  }

  if (authenticated === null) return <div className="boot-screen"><span /></div>
  if (!authenticated) return <Login onLogin={() => setAuthenticated(true)} />

  const content = page === 'overview' ? <Overview onNavigate={navigate} />
    : page === 'config' ? <Configuration />
      : page === 'browser' ? <BrowserSession /> : <Logs />

  return (
    <div className="app-shell">
      <aside className={`sidebar ${drawer ? 'open' : ''}`}>
        <div className="sidebar-brand"><div className="brand-mark small"><Bot size={16} /></div><span>Pocket48</span></div>
        <button className="icon-button close-drawer" onClick={() => setDrawer(false)} aria-label="关闭菜单"><X size={19} /></button>
        <nav>
          <p className="nav-label">工作区</p>
          {navigation.map(({ id, label, icon: Icon }) => (
            <button key={id} className={page === id ? 'active' : ''} onClick={() => navigate(id)}><Icon size={18} /><span>{label}</span></button>
          ))}
        </nav>
        <div className="sidebar-foot">
          <div><span className="status-dot healthy" /><span>服务已连接</span></div>
          <button onClick={logout}><LogOut size={17} /><span>退出登录</span></button>
        </div>
      </aside>
      {drawer ? <button className="drawer-scrim" onClick={() => setDrawer(false)} aria-label="关闭菜单" /> : null}
      <div className="workspace">
        <header className="topbar">
          <button className="icon-button menu-button" onClick={() => setDrawer(true)} aria-label="打开菜单"><Menu size={20} /></button>
          <div><span>Pocket48 Console</span><small>/ {navigation.find((item) => item.id === page)?.label}</small></div>
          <span className="top-status"><i />在线</span>
        </header>
        <div key={page} className="page-enter">{content}</div>
      </div>
      <nav className="mobile-nav" aria-label="主导航">
        {navigation.map(({ id, label, icon: Icon }) => <button key={id} className={page === id ? 'active' : ''} onClick={() => navigate(id)}><Icon size={19} /><span>{label}</span></button>)}
      </nav>
    </div>
  )
}

export function ErrorState({ error, retry }: { error: unknown; retry: () => void }) {
  const message = error instanceof APIError ? error.message : '暂时无法读取数据'
  return <div className="empty-state"><p>{message}</p><button className="secondary-button" onClick={retry}>重新加载</button></div>
}
