import { useCallback, useDeferredValue, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { ArrowDownToLine, CirclePause, CirclePlay, RefreshCw, Search } from 'lucide-react'
import { api } from '../api'
import { ErrorState } from '../App'

type LogResponse = { lines: string[]; updatedAt: string }
type Level = 'error' | 'warn' | 'success' | 'info' | 'debug' | 'plain'
type Density = 'comfortable' | 'compact'
type FilterLevel = 'all' | 'error' | 'warn' | 'success' | 'info'

type ParsedLine = {
  raw: string
  time: string
  source: string
  message: string
  level: Level
}

const LINE_RE =
  /^(?:(\d{4}\/\d{2}\/\d{2}\s+\d{2}:\d{2}:\d{2})\s+)?(?:\[([^\]]+)\])?\s*(.*)$/

function classifyLine(line: string): Level {
  if (/error|失败|异常|panic|fatal|FATAL|ERROR|\[err\]|traceback/i.test(line)) return 'error'
  if (/warn|warning|超时|timeout|degraded|skip |告警|⚠️/i.test(line)) return 'warn'
  if (/connected|healthy|success|notes_ok|已启动|已恢复|forward |✅|OK\]/i.test(line)) return 'success'
  if (/\[INFO\]|status=|Starting |心跳|ready|scan via|resolved |Checking /i.test(line)) return 'info'
  if (/\[DEBUG\]|debug/i.test(line)) return 'debug'
  return 'plain'
}

function parseLine(raw: string): ParsedLine {
  const m = raw.match(LINE_RE)
  const time = m?.[1]?.trim() || ''
  let source = (m?.[2] || '').trim()
  let message = (m?.[3] ?? raw).trim()
  // Nested sidecar tags: [Weibo-auth:stdout] [weibo-auth] foo
  const nested = message.match(/^\[([^\]]+)\]\s*(.*)$/)
  if (nested) {
    if (!source) source = nested[1]
    else if (!source.toLowerCase().includes(nested[1].toLowerCase().split(/[:\s]/)[0])) {
      // keep outer as primary source
    }
    message = nested[2]
  }
  if (!source) {
    if (/^\[?Weibo\]?/i.test(raw) || message.startsWith('[Weibo]')) source = 'Weibo'
    else if (/NAPCAT|NapCat/i.test(raw)) source = 'NapCat'
    else source = 'bot'
  }
  // short source for badge
  source = source.replace(/:stdout$/i, '').replace(/^weibo-auth$/i, 'Weibo-auth')
  const level = classifyLine(raw)
  return { raw, time, source, message: message || raw, level }
}

const HIGHLIGHT =
  /(error|失败|异常|panic|fatal|warn(?:ing)?|超时|timeout|degraded|connected|healthy|success|notes_ok|status=\w+|skip\s|forward\s|resolved\s|⚠️|✅)/gi

function renderHighlighted(line: string): ReactNode {
  const nodes: ReactNode[] = []
  let last = 0
  let m: RegExpExecArray | null
  const re = new RegExp(HIGHLIGHT.source, HIGHLIGHT.flags)
  while ((m = re.exec(line)) !== null) {
    if (m.index > last) nodes.push(line.slice(last, m.index))
    const token = m[0]
    const cls = classifyLine(token)
    nodes.push(
      <mark key={`${m.index}-${token}`} className={`log-hl log-hl-${cls === 'plain' || cls === 'debug' ? 'info' : cls}`}>
        {token}
      </mark>,
    )
    last = m.index + token.length
  }
  if (last < line.length) nodes.push(line.slice(last))
  return nodes.length ? nodes : line
}

function sourceTone(source: string): string {
  const s = source.toLowerCase()
  if (s.includes('nim')) return 'src-nim'
  if (s.includes('weibo')) return 'src-weibo'
  if (s.includes('douyin')) return 'src-douyin'
  if (s.includes('xiaohongshu') || s.includes('小红书')) return 'src-xhs'
  if (s.includes('napcat') || s.includes('onebot')) return 'src-qq'
  if (s.includes('pocket') || s === 'bot') return 'src-bot'
  return 'src-other'
}

const LEVEL_CHIPS: Array<{ id: FilterLevel; label: string }> = [
  { id: 'all', label: '全部' },
  { id: 'error', label: '错误' },
  { id: 'warn', label: '警告' },
  { id: 'success', label: '成功' },
  { id: 'info', label: '信息' },
]

export function Logs() {
  const [data, setData] = useState<LogResponse | null>(null)
  const [query, setQuery] = useState('')
  const deferredQuery = useDeferredValue(query.trim().toLowerCase())
  const [paused, setPaused] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [stickBottom, setStickBottom] = useState(true)
  const [filterLevel, setFilterLevel] = useState<FilterLevel>('all')
  const [filterSource, setFilterSource] = useState<string>('all')
  const [density, setDensity] = useState<Density>('comfortable')
  const consoleRef = useRef<HTMLElement | null>(null)

  const load = useCallback(async () => {
    try {
      setData(await api<LogResponse>('logs?limit=800'))
      setError(null)
    } catch (reason) {
      setError(reason)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  useEffect(() => {
    if (paused) return
    const timer = window.setInterval(() => void load(), 5000)
    return () => window.clearInterval(timer)
  }, [load, paused])

  const parsed = useMemo(() => (data?.lines || []).map(parseLine), [data])

  const counts = useMemo(() => {
    const c = { all: parsed.length, error: 0, warn: 0, success: 0, info: 0, debug: 0, plain: 0 }
    for (const row of parsed) {
      if (row.level in c) c[row.level as keyof typeof c] += 1
      if (row.level === 'info' || row.level === 'plain' || row.level === 'debug') {
        // info chip includes plain/debug as "信息流"
      }
    }
    c.info = parsed.filter((r) => r.level === 'info' || r.level === 'plain' || r.level === 'debug').length
    return c
  }, [parsed])

  const sourceOptions = useMemo(() => {
    const map = new Map<string, number>()
    for (const row of parsed) {
      const key = row.source || 'bot'
      map.set(key, (map.get(key) || 0) + 1)
    }
    return Array.from(map.entries())
      .sort((a, b) => b[1] - a[1])
      .slice(0, 8)
  }, [parsed])

  const lines = useMemo(() => {
    return parsed.filter((row) => {
      if (filterLevel === 'error' && row.level !== 'error') return false
      if (filterLevel === 'warn' && row.level !== 'warn') return false
      if (filterLevel === 'success' && row.level !== 'success') return false
      if (filterLevel === 'info' && !(row.level === 'info' || row.level === 'plain' || row.level === 'debug')) return false
      if (filterSource !== 'all' && row.source !== filterSource) return false
      if (deferredQuery && !row.raw.toLowerCase().includes(deferredQuery)) return false
      return true
    })
  }, [parsed, filterLevel, filterSource, deferredQuery])

  useEffect(() => {
    const el = consoleRef.current
    if (!el || !stickBottom) return
    requestAnimationFrame(() => {
      el.scrollTop = el.scrollHeight
    })
  }, [lines, stickBottom, data?.updatedAt, density])

  function onScroll() {
    const el = consoleRef.current
    if (!el) return
    const distance = el.scrollHeight - el.scrollTop - el.clientHeight
    setStickBottom(distance < 48)
  }

  function jumpLatest() {
    const el = consoleRef.current
    if (!el) return
    setStickBottom(true)
    el.scrollTop = el.scrollHeight
  }

  if (error && !data) {
    return (
      <main className="page-content">
        <ErrorState error={error} retry={() => void load()} />
      </main>
    )
  }

  return (
    <main className="page-content logs-page">
      <section className="page-heading">
        <div>
          <p className="eyebrow">LIVE SERVICE OUTPUT</p>
          <h1>日志</h1>
          <p>按级别/来源筛选，默认跟随最新。错误与关键状态会高亮。</p>
        </div>
        <div className="heading-actions">
          <span className="updated">{paused ? '已暂停' : `更新于 ${data?.updatedAt || '—'}`}</span>
          <div className="log-density" role="group" aria-label="行距">
            <button
              type="button"
              className={density === 'comfortable' ? 'active' : ''}
              onClick={() => setDensity('comfortable')}
            >
              舒适
            </button>
            <button type="button" className={density === 'compact' ? 'active' : ''} onClick={() => setDensity('compact')}>
              紧凑
            </button>
          </div>
          <button className="secondary-button" onClick={() => setPaused((value) => !value)}>
            {paused ? <CirclePlay size={16} /> : <CirclePause size={16} />}
            {paused ? '继续' : '暂停'}
          </button>
          <button className="secondary-button" onClick={jumpLatest} title="跳到最新">
            <ArrowDownToLine size={16} />
            最新
          </button>
          <button className="icon-button" onClick={() => void load()} title="刷新">
            <RefreshCw size={18} />
          </button>
        </div>
      </section>

      <section className="log-toolbar">
        <div className="log-toolbar-top">
          <label className="log-search">
            <Search size={17} />
            <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索日志内容、UID、房间号…" />
          </label>
          <div className="log-chips" role="tablist" aria-label="按级别筛选">
            {LEVEL_CHIPS.map((chip) => {
              const n =
                chip.id === 'all'
                  ? counts.all
                  : chip.id === 'info'
                    ? counts.info
                    : counts[chip.id]
              return (
                <button
                  key={chip.id}
                  type="button"
                  role="tab"
                  aria-selected={filterLevel === chip.id}
                  className={`log-chip level-${chip.id} ${filterLevel === chip.id ? 'active' : ''}`}
                  onClick={() => setFilterLevel(chip.id)}
                >
                  <i />
                  {chip.label}
                  <em>{n}</em>
                </button>
              )
            })}
          </div>
        </div>
        <div className="log-toolbar-bottom">
          <div className="log-source-scroll">
            <button
              type="button"
              className={`log-source-chip ${filterSource === 'all' ? 'active' : ''}`}
              onClick={() => setFilterSource('all')}
            >
              全部来源
            </button>
            {sourceOptions.map(([name, n]) => (
              <button
                key={name}
                type="button"
                className={`log-source-chip ${sourceTone(name)} ${filterSource === name ? 'active' : ''}`}
                onClick={() => setFilterSource(name === filterSource ? 'all' : name)}
                title={`${name} · ${n}`}
              >
                {name}
                <em>{n}</em>
              </button>
            ))}
          </div>
          <span className="log-count">
            显示 {lines.length}/{counts.all}
            {!stickBottom ? ' · 已上滚' : ' · 跟随最新'}
          </span>
        </div>
      </section>

      {error ? <div className="inline-error">{error instanceof Error ? error.message : '日志刷新失败'}</div> : null}

      <section
        className={`log-console density-${density}`}
        ref={consoleRef as never}
        onScroll={onScroll}
        aria-live="polite"
      >
        <div className="log-console-head" aria-hidden>
          <span>时间</span>
          <span>来源</span>
          <span>内容</span>
        </div>
        {lines.length === 0 ? (
          <div className="log-empty">当前筛选下没有日志</div>
        ) : (
          lines.map((row, index) => (
            <div key={`${index}-${row.time}-${row.message.slice(0, 40)}`} className={`log-row level-${row.level}`}>
              <span className="log-time">{row.time ? row.time.slice(5) : '—'}</span>
              <span className={`log-src ${sourceTone(row.source)}`}>{row.source}</span>
              <code className="log-msg">{renderHighlighted(row.message)}</code>
            </div>
          ))
        )}
      </section>

      {!stickBottom ? (
        <button type="button" className="log-jump-fab" onClick={jumpLatest}>
          <ArrowDownToLine size={15} />
          回到最新
        </button>
      ) : null}
    </main>
  )
}
