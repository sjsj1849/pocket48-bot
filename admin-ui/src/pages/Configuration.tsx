import { useCallback, useEffect, useMemo, useState } from 'react'
import { Check, Eye, EyeOff, Plus, Save, Trash2 } from 'lucide-react'
import { api } from '../api'
import type { ConfigField } from '../types'
import { ErrorState } from '../App'

type ConfigResponse = { groups: Record<string, ConfigField[]>; groupOrder: string[] }
type XiaohongshuSubscription = { groupId: number; userId: string; profileUrl?: string; name?: string; atAll: boolean; lastNoteId?: string; liveActive?: boolean }
type XiaohongshuResponse = { subscriptions: XiaohongshuSubscription[] }

function Field({ field, value, onChange }: { field: ConfigField; value: unknown; onChange: (value: unknown) => void }) {
  const [reveal, setReveal] = useState(false)
  if (field.kind === 'boolean') return (
    <label className="config-row toggle-row"><span><strong>{field.label}</strong><small>{field.description}</small></span><input type="checkbox" checked={Boolean(value)} onChange={(event) => onChange(event.target.checked)} /><i /></label>
  )
  if (field.kind === 'stringList') return (
    <label className="config-row"><span><strong>{field.label}</strong><small>{field.description}</small></span><textarea rows={3} value={Array.isArray(value) ? value.join('\n') : ''} onChange={(event) => onChange(event.target.value.split('\n'))} /></label>
  )
  return (
    <label className="config-row"><span><strong>{field.label}</strong><small>{field.description}</small></span><div className="input-wrap"><input type={field.kind === 'secret' && !reveal ? 'password' : field.kind === 'integer' ? 'number' : 'text'} min={field.kind === 'integer' ? 0 : undefined} placeholder={field.kind === 'secret' && field.configured ? '已配置，留空保持不变' : ''} value={typeof value === 'string' || typeof value === 'number' ? value : ''} onChange={(event) => onChange(field.kind === 'integer' ? Number(event.target.value) : event.target.value)} />{field.kind === 'secret' ? <button type="button" onClick={() => setReveal((shown) => !shown)} aria-label={reveal ? '隐藏' : '显示'}>{reveal ? <EyeOff size={17} /> : <Eye size={17} />}</button> : null}</div></label>
  )
}

export function Configuration() {
  const [data, setData] = useState<ConfigResponse | null>(null)
  const [values, setValues] = useState<Record<string, unknown>>({})
  const [active, setActive] = useState('')
  const [error, setError] = useState<unknown>(null)
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)
  const [xiaohongshu, setXiaohongshu] = useState<XiaohongshuSubscription[]>([])
  const [xhsGroupID, setXhsGroupID] = useState('')
  const [xhsTarget, setXhsTarget] = useState('')
  const [xhsAtAll, setXhsAtAll] = useState(false)
  const [xhsSaving, setXhsSaving] = useState(false)
  const load = useCallback(async () => {
    try {
      const [response, xhsResponse] = await Promise.all([api<ConfigResponse>('config'), api<XiaohongshuResponse>('xiaohongshu/subscriptions')])
      const next: Record<string, unknown> = {}
      Object.values(response.groups).flat().forEach((field) => { next[field.key] = field.value ?? (field.kind === 'boolean' ? false : field.kind === 'stringList' ? [] : '') })
      setData(response); setValues(next); setXiaohongshu(xhsResponse.subscriptions); setActive((current) => current || response.groupOrder[0]); setError(null)
    } catch (reason) { setError(reason) }
  }, [])
  useEffect(() => { void load() }, [load])
  const changed = useMemo(() => {
    if (!data) return false
    return Object.values(data.groups).flat().some((field) => JSON.stringify(values[field.key]) !== JSON.stringify(field.value ?? (field.kind === 'boolean' ? false : field.kind === 'stringList' ? [] : '')))
  }, [data, values])
  async function save() {
    if (!data || !changed) return
    const patch: Record<string, unknown> = {}
    Object.values(data.groups).flat().forEach((field) => {
      const original = field.value ?? (field.kind === 'boolean' ? false : field.kind === 'stringList' ? [] : '')
      if (JSON.stringify(values[field.key]) !== JSON.stringify(original)) patch[field.key] = values[field.key]
    })
    setSaving(true); setError(null)
    try { await api('config', { method: 'PUT', body: JSON.stringify({ values: patch }) }); setSaved(true); window.setTimeout(() => setSaved(false), 2200); await load() } catch (reason) { setError(reason) } finally { setSaving(false) }
  }
  async function addXiaohongshu() {
    if (!xhsGroupID || !xhsTarget.trim()) return
    setXhsSaving(true); setError(null)
    const raw = xhsTarget.trim()
    try {
      // Backend resolves xhslink / profile URL / internal user_id. Do not strip short links client-side.
      await api('xiaohongshu/subscriptions', {
        method: 'POST',
        body: JSON.stringify({ groupId: Number(xhsGroupID), target: raw, userId: '', profileUrl: '', atAll: xhsAtAll }),
      })
      setXhsTarget(''); setXhsAtAll(false); await load()
    } catch (reason) { setError(reason) } finally { setXhsSaving(false) }
  }
  async function removeXiaohongshu(item: XiaohongshuSubscription) {
    setXhsSaving(true); setError(null)
    try { await api('xiaohongshu/subscriptions', { method: 'DELETE', body: JSON.stringify({ groupId: item.groupId, userId: item.userId }) }); await load() }
    catch (reason) { setError(reason) } finally { setXhsSaving(false) }
  }
  if (error && !data) return <main className="page-content"><ErrorState error={error} retry={() => void load()} /></main>
  if (!data) return <main className="page-content"><div className="skeleton hero-skeleton" /></main>
  return (
    <main className="page-content config-page">
      <section className="page-heading"><div><p className="eyebrow">SYSTEM CONFIGURATION</p><h1>配置</h1><p>修改后将安全重启 Bot 以应用新设置。</p></div><button className="primary-button" disabled={!changed || saving} onClick={() => void save()}>{saved ? <Check size={17} /> : <Save size={17} />}{saved ? '已保存' : saving ? '保存中…' : '保存更改'}</button></section>
      {error ? <div className="inline-error">{error instanceof Error ? error.message : '保存失败'}</div> : null}
      <div className="config-layout">
        <nav className="config-tabs">{data.groupOrder.map((group) => <button key={group} className={active === group ? 'active' : ''} onClick={() => setActive(group)}>{group}<span>{data.groups[group]?.length || 0}</span></button>)}</nav>
        <section className="section-block config-section"><div className="section-title"><div><h2>{active}</h2><p>{active === 'QChat' ? '实时消息链路与 REST 兜底策略' : active === '浏览器' ? '微博、抖音与小红书共享认证环境' : active === '小红书' ? '帖子实时轮询、登录态与目标群订阅' : '管理相关运行参数'}</p></div></div><div className="config-fields">{(data.groups[active] || []).map((field) => <Field key={field.key} field={field} value={values[field.key]} onChange={(value) => setValues((current) => ({ ...current, [field.key]: value }))} />)}{active === '小红书' ? <div className="xhs-manager"><div className="xhs-add"><input type="number" min="1" placeholder="目标 QQ 群号（转发目标）" value={xhsGroupID} onChange={(event) => setXhsGroupID(event.target.value)} /><input type="text" placeholder="xhslink 或个人主页链接（不要填小红书号）" value={xhsTarget} onChange={(event) => setXhsTarget(event.target.value)} /><label><input type="checkbox" checked={xhsAtAll} onChange={(event) => setXhsAtAll(event.target.checked)} /> @全体</label><button className="secondary-button" disabled={xhsSaving || !xhsGroupID || !xhsTarget.trim()} onClick={() => void addXiaohongshu()}><Plus size={16} />添加订阅</button></div><div className="xhs-list">{xiaohongshu.length ? xiaohongshu.map((item) => <div className="xhs-item" key={`${item.groupId}-${item.userId}`}><div><strong>{item.name || '待获取昵称'}</strong><small>群 {item.groupId} · {item.userId}{item.atAll ? ' · @全体' : ''}{item.liveActive ? ' · 直播中' : ''}</small></div><button className="icon-button" disabled={xhsSaving} aria-label="删除订阅" onClick={() => void removeXiaohongshu(item)}><Trash2 size={16} /></button></div>) : <p className="muted">暂无小红书订阅。添加后首次扫描只建立基线，不会转发历史帖子。小红书号（如 956753385）不能用，请粘贴 xhslink 分享链接。</p>}</div></div> : null}</div></section>
      </div>
    </main>
  )
}
