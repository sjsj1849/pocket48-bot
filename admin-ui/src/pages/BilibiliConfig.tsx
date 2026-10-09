import { useEffect, useState } from 'react'
import { Pencil, Search, Trash2 } from 'lucide-react'
import { api } from '../api'
import { PlatformField } from '../components/PlatformField'
import { markDraftClean, useDraftedSettings } from '../drafts'
import { TargetSelector, useTargets } from '../components/TargetSelector'

type Up = { uid: string; name: string; avatar: string }
type Subscription = { id: string; uid: string; name: string; avatar: string; groupId: number; targetIds?: string[]; enabled: boolean; video: boolean; dynamics: boolean; live: boolean; atAll: boolean; minVideoSeconds?: number }
type Settings = { enabled: boolean; pollSeconds: number; videoPollSeconds: number; livePollSeconds: number; cookie?: string; subscriptions: Subscription[] }
type Dynamic = { id: string; kind: string; author: string; title: string; text: string; cover: string; url: string; time: number; length?: string; seconds?: number; view?: number }
type Result = { settings: Settings; cookieConfigured?: boolean; status: { lastCheck?: string; lastSuccess?: string; error?: string; targets?: Record<string, string> } }

const choices = [['dynamics', '动态'], ['video', '视频投稿'], ['live', '直播'], ['atAll', '@全体成员']] as const
const defaults = { dynamics: true, video: true, live: false, atAll: false, minVideoSeconds: 0 }

const kindLabels: Record<string, string> = { video: '新视频', draw: '图文动态', article: '新专栏', text: '文字动态', forward: '转发' }

export function BilibiliConfig({ defaultGroup }: { defaultGroup: string }) {
  const [data, setData] = useState<Result>()
  const [settings, setSettings] = useState<Settings>()
  const [baseline, setBaseline] = useState<Settings>()
  const [query, setQuery] = useState('')
  const [ups, setUps] = useState<Up[]>([])
  const [selected, setSelected] = useState<Up>()
  const [targetIds, setTargetIds] = useState<string[]>([])
  const { formatTargetIds } = useTargets()
  const [draft, setDraft] = useState<{ dynamics: boolean; video: boolean; live: boolean; atAll: boolean; minVideoSeconds: number }>(defaults)
  const [editing, setEditing] = useState('')
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  const [preview, setPreview] = useState<Dynamic[]>([])

  useEffect(() => {
    const c = new AbortController()
    api<Result>('bilibili/settings', { signal: c.signal })
      .then(r => { setData(r); setSettings(r.settings); setBaseline(r.settings) })
      .catch(e => { if (!c.signal.aborted) setError(e.message) })
    return () => c.abort()
  }, [])

  // 运行设置（间隔、Cookie）跟着右上角「保存更改」一起提交
  useDraftedSettings('bilibili', 'B 站运行设置', settings, baseline, async (next: Settings) => {
    const r = await api<{ settings: Settings }>('bilibili/settings', { method: 'PUT', body: JSON.stringify(next) })
    setSettings(r.settings); setBaseline(r.settings); setData(await api<Result>('bilibili/settings'))
    setMessage('B 站运行设置已保存，下一次检查生效')
    return r.settings
  })

  async function action(name: string, work: () => Promise<void>) {
    setBusy(name); setError(''); setMessage('')
    try { await work() } catch (e) { setError(e instanceof Error ? e.message : '操作失败') } finally { setBusy('') }
  }
  // 订阅增删改是「立即生效」的操作，单独落盘并同步基线
  async function save(next: Settings) {
    const r = await api<{ settings: Settings }>('bilibili/settings', { method: 'PUT', body: JSON.stringify(next) })
    setSettings(r.settings); setBaseline(r.settings)
    markDraftClean('bilibili')
    setMessage('已保存，下一次检查生效，无需重启')
  }
  function edit(s: Subscription) {
    setSelected({ uid: s.uid, name: s.name, avatar: s.avatar })
    setTargetIds(s.targetIds || [])
    setDraft({ dynamics: s.dynamics, video: s.video, live: s.live, atAll: s.atAll, minVideoSeconds: s.minVideoSeconds || 0 })
    setEditing(s.id)
  }
  function reset() { setSelected(undefined); setEditing(''); setDraft({ ...defaults }); setTargetIds([]) }

  if (!settings) return <div>{error ? <p className="inline-error">{error}</p> : <p className="muted">正在加载 B 站配置…</p>}</div>
  return <div className="platform-config x-config">
    {error && <p className="inline-error" role="alert">{error}</p>}
    {message && <p className="platform-notice" role="status">{message}</p>}
    <section className="platform-section">
      <h3>运行设置</h3>
      <p className="muted">全部使用 B 站公开接口，无需登录即可运行；首次启用只建立基线，不补发历史内容。</p>
      <p className="muted">注意：B 站对投稿接口限流很紧，机房 IP 上被限流时会自动退避并在下方显示原因。填写登录 Cookie 可显著放宽配额。修改后点右上角「保存更改」。</p>
      <PlatformField label="动态检查间隔（秒）" description="60–3600 秒，建议 180 秒。监控图文动态与专栏，接口较宽松。">
        <input type="number" min="60" max="3600" value={settings.pollSeconds} onChange={e => setSettings({ ...settings, pollSeconds: Number(e.target.value) })} />
      </PlatformField>
      <PlatformField label="视频投稿检查间隔（秒）" description="300–3600 秒，建议 600 秒。投稿接口限流很紧，放慢更稳。">
        <input type="number" min="300" max="3600" value={settings.videoPollSeconds} onChange={e => setSettings({ ...settings, videoPollSeconds: Number(e.target.value) })} />
      </PlatformField>
      <PlatformField label="直播检查间隔（秒）" description="20–3600 秒，建议 60 秒。开播通知要更及时。">
        <input type="number" min="20" max="3600" value={settings.livePollSeconds} onChange={e => setSettings({ ...settings, livePollSeconds: Number(e.target.value) })} />
      </PlatformField>
      <PlatformField label="登录 Cookie（可选）" description={data?.cookieConfigured ? '已配置。留空表示保持现有 Cookie 不变。' : '通常在「浏览器」页面登录后会自动同步到这里；这里可手动粘贴 SESSDATA 兜底。'}>
        <input type="password" autoComplete="off" placeholder={data?.cookieConfigured ? '已配置，留空不修改' : 'SESSDATA=xxx; bili_jct=xxx'} value={settings.cookie || ''} onChange={e => setSettings({ ...settings, cookie: e.target.value })} />
      </PlatformField>
      <div className="inline-actions">
        <button className="secondary-button" disabled={!!busy} onClick={() => void action('refresh', async () => { setData(await api<Result>('bilibili/settings')) })}>刷新状态</button>
      </div>
      {data?.status.lastCheck && <p className="muted">最近检查：{new Date(data.status.lastCheck).toLocaleString()} · {data.status.error || '正常'}</p>}
    </section>
    <section className="platform-section">
      <h3>添加订阅</h3>
      <p className="muted">填写 UP 主的数字 UID，或粘贴空间主页链接（例如 https://space.bilibili.com/123456）。</p>
      <form className="platform-search" onSubmit={e => { e.preventDefault(); void action('search', async () => { const r = await api<{ ups: Up[] }>(`bilibili/search?q=${encodeURIComponent(query)}`); setUps(r.ups); if (!r.ups.length) setMessage('没有找到该 UP 主') }) }}>
        <input aria-label="搜索 B 站 UP 主" value={query} onChange={e => setQuery(e.target.value)} placeholder="UID 或空间主页链接" />
        <button className="secondary-button" disabled={!!busy}><Search size={16} />{busy === 'search' ? '查询中…' : '查询'}</button>
      </form>
      <div className="platform-results">
        {ups.map(u => <button className={`platform-result${selected?.uid === u.uid ? ' selected' : ''}`} key={u.uid} disabled={!!busy} onClick={() => { setSelected(u); setEditing(''); setDraft({ ...defaults }) }}>{u.avatar && <img src={u.avatar} alt="" loading="lazy" />}<span><strong>{u.name}</strong><small>UID {u.uid}</small></span></button>)}
      </div>
      {selected && <div className="platform-editor">
        <h4>{editing ? '编辑订阅' : '添加订阅'} · {selected.name}（UID {selected.uid}）</h4>
        <PlatformField label="投递目标" description="勾选消息投递目标（可多选）。"><TargetSelector value={targetIds} onChange={setTargetIds} /></PlatformField>
        <div className="platform-choices">
          {choices.map(([key, label]) => <label key={key}><input type="checkbox" checked={draft[key]} onChange={e => setDraft({ ...draft, [key]: e.target.checked })} />{label}</label>)}
        </div>
        {draft.video ? <p className="muted">短视频与抖音同名内容会按「先发布先推送」自动去重：抖音先发的 B 站跳过，B 站先发的照推，标题对不上一律都推。10 分钟以内的短视频会附带视频本体。</p> : null}
        <div className="inline-actions">
          <button className="primary-button" disabled={!!busy || (!draft.dynamics && !draft.video && !draft.live)} onClick={() => void action('subscribe', async () => {
            const existing = settings.subscriptions.find(v => v.id === editing)
            const sub: Subscription = { ...draft, minVideoSeconds: Number(draft.minVideoSeconds) || 0, id: editing || crypto.randomUUID(), uid: selected.uid, name: selected.name, avatar: selected.avatar, groupId: existing?.groupId ?? Number(defaultGroup), targetIds, enabled: true }
            await save({ ...settings, subscriptions: editing ? settings.subscriptions.map(v => v.id === sub.id ? sub : v) : [...settings.subscriptions, sub] })
            reset()
          })}>{editing ? '保存订阅' : '添加订阅'}</button>
          <button className="secondary-button" disabled={!!busy} onClick={reset}>取消</button>
          <button className="secondary-button" disabled={!!busy} onClick={() => void action('preview', async () => { const r = await api<{ dynamics: Dynamic[]; message: string }>('bilibili/preview', { method: 'POST', body: JSON.stringify({ uid: selected.uid, minVideoSeconds: editing ? (settings.subscriptions.find(v => v.id === editing)?.minVideoSeconds || 0) : (draft.minVideoSeconds || 0) }) }); setPreview(r.dynamics); setMessage(r.message) })}>{busy === 'preview' ? '测试中…' : '只读测试（不推送）'}</button>
        </div>
      </div>}
    </section>
    <section className="platform-section">
      <h3>已订阅 UP 主</h3>
      {!settings.subscriptions.length && <p className="muted">还没有订阅，请先查询 UID 并勾选投递目标。</p>}
      {settings.subscriptions.map(s => <article className="douyin-card" key={s.id}>
        <div className="douyin-card-main">
          <div className="douyin-card-heading"><strong>{s.name || s.uid}</strong><span className="quiet-badge">{s.enabled ? '已启用' : '已暂停'}</span></div>
          <p className="muted">UID {s.uid} · {formatTargetIds(s.targetIds) || '未设置投递目标'}</p>
          <p className="muted">{choices.filter(([key]) => s[key]).map(([, label]) => label).join(' / ')}{s.video ? ' · 短视频跨平台去重' : ''}</p>
          {data?.status.targets?.[s.id] && <p className="muted">{data.status.targets[s.id]}</p>}
        </div>
        <div className="sub-item-actions">
          <button className="secondary-button" disabled={!!busy} onClick={() => void action('toggle', () => save({ ...settings, subscriptions: settings.subscriptions.map(v => v.id === s.id ? { ...v, enabled: !v.enabled } : v) }))}>{s.enabled ? '暂停' : '启用'}</button>
          <button className="icon-button" aria-label={`编辑 B 站订阅 ${s.uid}`} disabled={!!busy} onClick={() => edit(s)}><Pencil size={15} /></button>
          <button className="icon-button danger" aria-label={`删除 B 站订阅 ${s.uid}`} disabled={!!busy} onClick={() => { if (window.confirm(`删除 ${s.name || s.uid} 的订阅？`)) void action('delete', () => save({ ...settings, subscriptions: settings.subscriptions.filter(v => v.id !== s.id) })) }}><Trash2 size={16} /></button>
        </div>
      </article>)}
    </section>
    {!!preview.length && <section className="platform-section">
      <h3>只读预览</h3>
      {preview.filter(d => {
        const threshold = editing ? (settings.subscriptions.find(v => v.id === editing)?.minVideoSeconds || 0) : (draft.minVideoSeconds || 0)
        if (d.kind !== 'video' || !threshold) return true
        return !d.seconds || d.seconds >= threshold
      }).map(d => <article className="platform-preview" key={d.id}>
        <strong>{kindLabels[d.kind] || d.kind}{d.time ? ` · ${new Date(d.time).toLocaleString()}` : ''}</strong>
        <p>{d.title || d.text || '(无正文)'}</p>
        {d.length || d.view ? <p className="muted">{d.length ? '时长 ' + d.length : ''}{d.length && d.view ? ' · ' : ''}{d.view ? '播放 ' + d.view.toLocaleString() : ''}</p> : null}
        <a href={d.url} target="_blank" rel="noreferrer">查看原文</a>
      </article>)}
    </section>}
  </div>
}
