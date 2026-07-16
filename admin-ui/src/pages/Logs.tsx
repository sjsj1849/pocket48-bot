import { useCallback, useDeferredValue, useEffect, useMemo, useState } from 'react'
import { CirclePause, CirclePlay, RefreshCw, Search } from 'lucide-react'
import { api } from '../api'
import { ErrorState } from '../App'

type LogResponse = { lines: string[]; updatedAt: string }

export function Logs() {
  const [data, setData] = useState<LogResponse | null>(null)
  const [query, setQuery] = useState('')
  const deferredQuery = useDeferredValue(query.trim().toLowerCase())
  const [paused, setPaused] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const load = useCallback(async () => { try { setData(await api<LogResponse>('logs?limit=800')); setError(null) } catch (reason) { setError(reason) } }, [])
  useEffect(() => { void load() }, [load])
  useEffect(() => { if (paused) return; const timer = window.setInterval(() => void load(), 5000); return () => window.clearInterval(timer) }, [load, paused])
  const lines = useMemo(() => deferredQuery ? (data?.lines || []).filter((line) => line.toLowerCase().includes(deferredQuery)) : data?.lines || [], [data, deferredQuery])
  if (error && !data) return <main className="page-content"><ErrorState error={error} retry={() => void load()} /></main>
  return (
    <main className="page-content logs-page">
      <section className="page-heading"><div><p className="eyebrow">LIVE SERVICE OUTPUT</p><h1>日志</h1><p>查看 Bot 与各连接侧卡的实时输出。</p></div><div className="heading-actions"><span className="updated">{paused ? '已暂停' : `更新于 ${data?.updatedAt || '—'}`}</span><button className="secondary-button" onClick={() => setPaused((value) => !value)}>{paused ? <CirclePlay size={16} /> : <CirclePause size={16} />}{paused ? '继续' : '暂停'}</button><button className="icon-button" onClick={() => void load()} title="刷新"><RefreshCw size={18} /></button></div></section>
      <section className="log-tools"><label><Search size={17} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="筛选日志内容" /></label><span>{lines.length} 行</span></section>
      {error ? <div className="inline-error">{error instanceof Error ? error.message : '日志刷新失败'}</div> : null}
      <section className="log-console" aria-live="polite">{lines.map((line, index) => <div key={`${index}-${line}`} className={/error|失败|异常/i.test(line) ? 'error' : /connected|healthy|success/i.test(line) ? 'success' : ''}><span>{String(index + 1).padStart(3, '0')}</span><code>{line}</code></div>)}</section>
    </main>
  )
}
