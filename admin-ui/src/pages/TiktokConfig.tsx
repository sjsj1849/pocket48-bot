import { useEffect, useState } from 'react'
import { Pencil, Trash2 } from 'lucide-react'
import { api } from '../api'
import { PlatformField } from '../components/PlatformField'
import { markDraftClean, useDraftedSettings } from '../drafts'
import { TargetSelector, useTargets } from '../components/TargetSelector'

type Subscription = {
  id: string
  username: string
  displayName?: string
  avatar?: string
  targetIds?: string[]
  enabled: boolean
  atAll: boolean
}
type Settings = { enabled: boolean; pollSeconds: number; subscriptions: Subscription[] }
type AccountStatus = {
  username: string
  displayName?: string
  ready: boolean
  lastScan?: string
  lastSuccess?: string
  lastError?: string
  failCount: number
  seenCount: number
}
type Status = {
  lastCheck?: string
  lastSuccess?: string
  error?: string
  targets?: Record<string, string>
}
type Result = {
  settings: Settings
  status: Status
  accounts: AccountStatus[]
  limits: { min: number; max: number; reset: number }
}
type PreviewVideo = {
  id: string
  desc: string
  cover: string
  url: string
  time: number
  seconds: number
  author: string
  play: number
}

const defaults = { atAll: false }

/**
 * TikTok（洋抖）作品监控配置页（2026-10-04 新增）。
 *
 * 与其它平台页的差异：TikTok 没有「按关键词搜账号」这回事 ——
 * 采集走 Playwright 渲染 + 拦截 XHR，没有可匿名调用的用户查询接口，
 * 而 api/post/item_list 连请求 5-6 次就会限流约 5 分钟。
 * 所以这里不提供输入即搜的搜索框，改成：
 *   - 手填用户名（或直接粘贴主页/作品链接，服务端会解析）
 *   - 「只读测试」按钮真实拉一次作品列表来验证账号有效
 */
export function TiktokConfig() {
  const [data, setData] = useState<Result>()
  const [settings, setSettings] = useState<Settings>()
  const [baseline, setBaseline] = useState<Settings>()
  const [input, setInput] = useState('')
  const [targetIds, setTargetIds] = useState<string[]>([])
  const [draft, setDraft] = useState(defaults)
  const [editing, setEditing] = useState('')
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  const [preview, setPreview] = useState<PreviewVideo[]>([])
  const { formatTargetIds } = useTargets()

  useEffect(() => {
    const c = new AbortController()
    api<Result>('tiktok/settings', { signal: c.signal })
      .then((r) => { setData(r); setSettings(r.settings); setBaseline(r.settings) })
      .catch((e) => { if (!c.signal.aborted) setError(e.message) })
    return () => c.abort()
  }, [])

  useDraftedSettings('tiktok', 'TikTok 运行设置', settings, baseline, async (next: Settings) => {
    const r = await api<{ settings: Settings }>('tiktok/settings', { method: 'PUT', body: JSON.stringify(next) })
    setSettings(r.settings); setBaseline(r.settings); setData(await api<Result>('tiktok/settings'))
    setMessage('TikTok 运行设置已保存，下一轮生效')
    return r.settings
  })

  async function action(name: string, work: () => Promise<void>) {
    setBusy(name); setError(''); setMessage('')
    try { await work() } catch (e) { setError(e instanceof Error ? e.message : '操作失败') } finally { setBusy('') }
  }

  async function save(next: Settings) {
    const r = await api<{ settings: Settings }>('tiktok/settings', { method: 'PUT', body: JSON.stringify(next) })
    setSettings(r.settings); setBaseline(r.settings)
    markDraftClean('tiktok')
    setData(await api<Result>('tiktok/settings'))
    setMessage('已保存。TikTok 轮询较慢，最长 15 分钟内生效，无需重启')
  }

  function reset() { setInput(''); setEditing(''); setDraft({ ...defaults }); setTargetIds([]); setPreview([]) }

  if (!settings) return <div>{error ? <p className="inline-error">{error}</p> : <p className="muted">正在加载 TikTok 配置…</p>}</div>

  const limits = data?.limits ?? { min: 300, max: 7200, reset: 900 }
  const accountOf = (username: string) => (data?.accounts || []).find((a) => a.username.toLowerCase() === username.toLowerCase())

  return <div className="platform-config x-config">
    {error && <p className="inline-error" role="alert">{error}</p>}
    {message && <p className="platform-notice" role="status">{message}</p>}

    <section className="platform-section">
      <h3>运行设置</h3>
      <p className="muted">采集通过浏览器渲染 TikTok 页面并拦截接口，<strong>不需要登录</strong>：全新访客会自动获得匿名 Cookie，下载照样成功。首次启用只建立基线，不补发历史作品。</p>
      <p className="muted">限流说明：作品列表接口连请求 5-6 次后会持续返回空响应约 5 分钟。间隔不建议低于 300 秒，心连心更新本身也不密。修改后点右上角「保存更改」。</p>
      <PlatformField label="作品检查间隔（秒）" description={`${limits.min}–${limits.max} 秒，建议 ${limits.reset} 秒。同一支片子在抖音、TikTok、B站都发时只推最早的那个。`}>
        <input type="number" min={limits.min} max={limits.max} value={settings.pollSeconds} onChange={(e) => setSettings({ ...settings, pollSeconds: Number(e.target.value) })} />
      </PlatformField>
      <div className="inline-actions">
        <button className="secondary-button" disabled={!!busy} onClick={() => void action('refresh', async () => { setData(await api<Result>('tiktok/settings')) })}>刷新状态</button>
        <button className="secondary-button" disabled={!!busy} onClick={() => void action('preview', async () => {
          const r = await api<{ videos: PreviewVideo[]; message: string }>('tiktok/preview', { method: 'POST', body: JSON.stringify({ username: input || editing }) })
          setPreview(r.videos); setMessage(r.message)
        })}>{busy === 'preview' ? '拉取中（约 5-10 秒）…' : '只读测试（不推送）'}</button>
      </div>
      {data?.status.lastCheck && <p className="muted">最近检查：{new Date(data.status.lastCheck).toLocaleString()} · {data.status.error || '正常'}</p>}
    </section>

    <section className="platform-section">
      <h3>添加账号</h3>
      <p className="muted">填写 TikTok 用户名（不含 @），也可以直接粘贴主页或作品链接，保存时会自动解析出用户名。</p>
      <div className="platform-editor">
        <PlatformField label="TikTok 用户名" description="例如 hearts2hearts，或粘贴 https://www.tiktok.com/@hearts2hearts">
          <input aria-label="TikTok 用户名" value={input} onChange={(e) => setInput(e.target.value)} placeholder="hearts2hearts 或主页链接" />
        </PlatformField>
        <PlatformField label="投递目标" description="勾选消息投递目标（可多选）。">
          <TargetSelector value={targetIds} onChange={setTargetIds} />
        </PlatformField>
        <label className="platform-choices"><label><input type="checkbox" checked={draft.atAll} onChange={(e) => setDraft({ ...draft, atAll: e.target.checked })} />@全体成员</label></label>
        <div className="inline-actions">
          <button className="primary-button" disabled={!!busy || !input.trim()} onClick={() => void action('subscribe', async () => {
            const existing = settings.subscriptions.find((v) => v.id === editing)
            const sub: Subscription = {
              ...draft,
              id: editing || `tt-${Date.now()}`,
              username: input.trim(),
              displayName: existing?.displayName || '',
              avatar: existing?.avatar || '',
              targetIds,
              enabled: true,
            }
            await save({ ...settings, subscriptions: editing ? settings.subscriptions.map((v) => v.id === sub.id ? sub : v) : [...settings.subscriptions, sub] })
            reset()
          })}>{editing ? '保存订阅' : '添加订阅'}</button>
          <button className="secondary-button" disabled={!!busy} onClick={reset}>取消</button>
        </div>
      </div>
    </section>

    <section className="platform-section">
      <h3>已订阅账号</h3>
      {!settings.subscriptions.length && <p className="muted">还没有订阅，请在上方填写用户名并勾选投递目标。</p>}
      {settings.subscriptions.map((s) => {
        const st = accountOf(s.username)
        return <article className="douyin-card" key={s.id}>
          <div className="douyin-card-main">
            <div className="douyin-card-heading">
              <strong>{s.displayName || s.username}</strong>
              <span className="quiet-badge">{s.enabled ? '已启用' : '已暂停'}</span>
              {st?.ready ? <span className="quiet-badge">基线已建立</span> : null}
            </div>
            <p className="muted">@{s.username} · {formatTargetIds(s.targetIds) || '未设置投递目标（将回落到抖音同名账号）'}</p>
            {st?.lastSuccess ? <p className="muted">最近成功：{new Date(st.lastSuccess).toLocaleString()} · 已记录 {st.seenCount} 条作品</p> : null}
            {st?.lastError ? <p className="inline-error">{st.lastError}{st.failCount > 1 ? `（连续失败 ${st.failCount} 次）` : ''}</p> : null}
            {data?.status.targets?.[s.id] && <p className="muted">{data.status.targets[s.id]}</p>}
          </div>
          <div className="sub-item-actions">
            <button className="secondary-button" disabled={!!busy} onClick={() => void action('toggle', () => save({ ...settings, subscriptions: settings.subscriptions.map((v) => v.id === s.id ? { ...v, enabled: !v.enabled } : v) }))}>{s.enabled ? '暂停' : '启用'}</button>
            <button className="icon-button" aria-label={`编辑 TikTok 订阅 ${s.username}`} disabled={!!busy} onClick={() => { setInput(s.username); setTargetIds(s.targetIds || []); setDraft({ atAll: s.atAll }); setEditing(s.id) }}><Pencil size={15} /></button>
            <button className="icon-button danger" aria-label={`删除 TikTok 订阅 ${s.username}`} disabled={!!busy} onClick={() => { if (window.confirm(`删除 @${s.username} 的订阅？`)) void action('delete', () => save({ ...settings, subscriptions: settings.subscriptions.filter((v) => v.id !== s.id) })) }}><Trash2 size={16} /></button>
          </div>
        </article>
      })}
    </section>

    {!!preview.length && <section className="platform-section">
      <h3>只读预览</h3>
      {preview.map((v) => <article className="platform-preview" key={v.id}>
        <strong>{v.author || '作品'} · {new Date(v.time).toLocaleString()}</strong>
        <p>{v.desc || '(无正文)'}</p>
        <p className="muted">{v.seconds ? `时长 ${v.seconds} 秒` : ''}{v.play ? ` · 播放 ${v.play.toLocaleString()}` : ''}</p>
        <a href={v.url} target="_blank" rel="noreferrer">查看原文</a>
      </article>)}
    </section>}
  </div>
}