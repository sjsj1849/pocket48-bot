import { useEffect, useRef, useState } from 'react'
import { api } from '../api'
import { WeverseAIConfig } from './WeverseAIConfig'

type Subscription = { id: string; groupId: number; communityId: number; communityName: string; slug: string; memberIds: string[]; memberNames: string[]; posts: boolean; comments: boolean; live: boolean; translate: boolean; atAll: boolean; atAllMemberIds?: string[]; atAllMemberNames?: string[]; enabled: boolean }
type Settings = { enabled: boolean; pollSeconds: number; proxyUrl?: string; subscriptions: Subscription[] }
type Community = { id: number; name: string; slug: string; members: string[] }
type Member = { id: string; name: string }
type Result = { settings: Settings; sessionConfigured: boolean; status: { lastCheck?: string; lastSuccess?: string; error?: string } }
const events = [['posts', '发帖'], ['comments', '成员回复 / 评论'], ['live', '开播 / 结束 / 回放'], ['translate', '附中文翻译'], ['atAll', '@全体成员']] as const
const draftDefaults = { posts: true, comments: true, live: true, translate: true, atAll: false, atAllMemberIds: [] as string[], atAllMemberNames: [] as string[] }

export function WeverseConfig({ defaultGroup }: { defaultGroup: string }) {
  const [data, setData] = useState<Result>()
  const [settings, setSettings] = useState<Settings>()
  const [query, setQuery] = useState('Hearts2Hearts')
  const [communities, setCommunities] = useState<Community[]>([])
  const [community, setCommunity] = useState<Community>()
  const [members, setMembers] = useState<Member[]>([])
  const [selected, setSelected] = useState<string[]>([])
  const [allMembers, setAllMembers] = useState(true)
  const [group, setGroup] = useState(defaultGroup)
  const [draft, setDraft] = useState(draftDefaults)
  const [editing, setEditing] = useState('')
 const [mentionRestricted, setMentionRestricted] = useState(false)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  const [preview, setPreview] = useState<Array<{ kind: string; author: string; body: string; parentBody?: string; parentTranslation?: string; translation?: string; translationError?: string; url: string }>>([])
  const searchGeneration = useRef(0)
  const memberGeneration = useRef(0)

  useEffect(() => {
    const controller = new AbortController()
    api<Result>('weverse/settings', { signal: controller.signal }).then(r => { setData(r); setSettings(r.settings) }).catch(e => { if (!controller.signal.aborted) setError(e.message) })
    return () => controller.abort()
  }, [])

  async function action(key: string, work: () => Promise<void>) {
    setBusy(key); setError(''); setMessage('')
    try { await work() } catch (e) { setError(e instanceof Error ? e.message : '操作失败') } finally { setBusy('') }
  }
  async function save(next: Settings) {
    await api('weverse/settings', { method: 'PUT', body: JSON.stringify(next) })
    setSettings(next); setMessage('已保存，下一次检查生效，无需重启')
  }
  async function search() {
    const generation = ++searchGeneration.current
    await action('search', async () => {
      const r = await api<{ communities: Community[] }>(`weverse/search?q=${encodeURIComponent(query)}`)
      if (generation === searchGeneration.current) { setCommunities(r.communities); if (!r.communities.length) setMessage('没有匹配的团体或成员，请尝试官方英文名或韩文名') }
    })
  }
  async function selectCommunity(c: Community, edit?: Subscription) {
    const generation = ++memberGeneration.current
    setCommunity(c); setMembers([]); setSelected(edit?.memberIds || []); setAllMembers(!edit?.memberIds.length)
    setEditing(edit?.id || ''); setGroup(edit ? String(edit.groupId) : defaultGroup); setDraft({ ...draftDefaults, ...edit, atAllMemberIds: edit?.atAllMemberIds || [], atAllMemberNames: edit?.atAllMemberNames || [] }); setMentionRestricted(!!edit?.atAllMemberIds?.length)
    await action('members', async () => {
      const r = await api<{ members: Member[] }>(`weverse/members?communityId=${c.id}`)
      if (generation === memberGeneration.current) setMembers(r.members)
    })
  }
  async function browser(actionName: 'open' | 'sync') {
    await action(actionName, async () => {
      await api('weverse/browser', { method: 'POST', body: JSON.stringify({ action: actionName }) })
      if (actionName === 'open') {
        window.dispatchEvent(new CustomEvent('pocket48-navigate', { detail: 'browser' }))
      } else {
        const r = await api<Result>('weverse/settings'); setData(r); setMessage('登录态已同步，请选择成员或进行只读测试')
      }
    })
  }
  if (!settings) return <div className="weverse-config">{error ? <p className="inline-error">{error}</p> : <p>正在加载 Weverse 配置…</p>}</div>
  return <div className="weverse-config">
    <p className="muted">监控指定团体或成员的发帖、回复与开播。首次启用只建立基线，不补发历史消息。</p>
    {error && <div role="alert" className="inline-error">{error}</div>}
    {message && <p role="status" className="wv-notice">{message}</p>}
    <section className="wv-card">
      <h3>登录与运行</h3>
      <p>{data?.sessionConfigured ? '已保存登录态（有效性请点击只读测试确认）' : '尚未登录 Weverse'}</p>
      <p className="muted">打开登录页后，在「浏览器」页面完成登录并加入 Hearts2Hearts 社区，再回到这里同步登录态。程序会自动续期；无法续期时显示重新登录提示。请在 Weverse 设置中将翻译语言设为中文。</p>
      <div className="wv-actions">
        <button className="secondary-button" disabled={!!busy} onClick={() => void browser('open')}>打开 Weverse 登录</button>
        <button className="secondary-button" disabled={!!busy} onClick={() => void browser('sync')}>同步登录态</button>
        <button className="secondary-button" disabled={!!busy || !data?.sessionConfigured} onClick={() => void action('preview', async () => { const r = await api<{ events: typeof preview; message: string }>('weverse/preview', { method: 'POST' }); setPreview(r.events); setMessage(r.message) })}>{busy === 'preview' ? '测试中…' : '只读测试（不推送）'}</button>
      </div>
      <label className="wv-check"><input type="checkbox" checked={settings.enabled} onChange={e => setSettings({ ...settings, enabled: e.target.checked })} />启用 Weverse 监控</label>
      <div className="wv-fields">
        <label>检查间隔（秒）<input type="number" min="30" max="3600" value={settings.pollSeconds} onChange={e => setSettings({ ...settings, pollSeconds: Number(e.target.value) })} /></label>
        <label>网络代理（可选）<input value={settings.proxyUrl || ''} placeholder="留空使用服务器网络" onChange={e => setSettings({ ...settings, proxyUrl: e.target.value })} /></label>
      </div>
      <button className="primary-button" disabled={!!busy} onClick={() => void action('save', () => save(settings))}>保存运行设置</button>
      {data?.status.lastCheck && <p className="muted">最近检查：{new Date(data.status.lastCheck).toLocaleString()}　{data.status.error || '正常'}</p>}
    </section>
    <WeverseAIConfig />
    <section className="wv-card">
      <h3>搜索团体与成员</h3>
      <p className="muted">支持团体英文名、韩文名、成员官方名，以及 Weverse 社区链接。可输入 Hearts2Hearts、H2H 或 CARMEN。</p>
      <form className="wv-actions" onSubmit={e => { e.preventDefault(); void search() }}>
        <input aria-label="搜索团体或成员" value={query} onChange={e => setQuery(e.target.value)} />
        <button className="secondary-button" type="submit" disabled={!!busy}>{busy === 'search' ? '搜索中…' : '搜索'}</button>
      </form>
      <div className="wv-results">{communities.map(c => <button key={c.id} className={`wv-result ${community?.id === c.id ? 'selected' : ''}`} disabled={!!busy} onClick={() => void selectCommunity(c)}><strong>{c.name}</strong><small>{c.members.join(' · ') || '查看成员'}</small></button>)}</div>
      {community && <div className="wv-editor">
        <h4>{editing ? '编辑订阅' : '添加订阅'} · {community.name}</h4>
        {!members.length ? <p>登录并加入社区后，点击下方按钮加载成员。</p> : null}
        <button className="secondary-button" disabled={!!busy} onClick={() => void selectCommunity(community, editing ? settings.subscriptions.find(s => s.id === editing) : undefined)}>重新加载成员</button>
        <label className="wv-check"><input type="checkbox" checked={allMembers} onChange={e => setAllMembers(e.target.checked)} />整团（包括以后加入的成员）</label>
        {!allMembers && <div className="wv-members">{members.map(m => <label key={m.id} className="wv-check"><input type="checkbox" checked={selected.includes(m.id)} onChange={e => setSelected(v => e.target.checked ? [...v, m.id] : v.filter(id => id !== m.id))} />{m.name}</label>)}</div>}
        <label>推送 QQ 群号<input aria-label="推送 QQ 群号" inputMode="numeric" value={group} onChange={e => setGroup(e.target.value)} /></label>
        <div className="wv-members">{events.map(([k, label]) => <label key={k} className="wv-check"><input type="checkbox" checked={draft[k]} onChange={e => setDraft({ ...draft, [k]: e.target.checked })} />{label}</label>)}</div>
        {draft.atAll && <div className="wv-editor">
          <label className="wv-check"><input type="checkbox" checked={mentionRestricted} onChange={e => setMentionRestricted(e.target.checked)} />仅指定成员 @全体</label>
          <p className="muted">只影响 @全体成员，其他成员的内容仍正常转发。</p>
          {mentionRestricted && <div className="wv-members">{members.map(m => <label key={m.id} className="wv-check"><input type="checkbox" checked={draft.atAllMemberIds.includes(m.id)} onChange={e => setDraft(v => ({ ...v, atAllMemberIds: e.target.checked ? [...v.atAllMemberIds, m.id] : v.atAllMemberIds.filter(id => id !== m.id) }))} />@全体：{m.name}</label>)}</div>}
        </div>}
        <button className="primary-button" disabled={!!busy || !members.length || (draft.atAll && mentionRestricted && !draft.atAllMemberIds.length) || !/^\d+$/.test(group) || Number(group) <= 0 || (!allMembers && !selected.length) || (!draft.posts && !draft.comments && !draft.live)} onClick={() => void action('subscribe', async () => {
          const chosen = members.filter(m => selected.includes(m.id))
 const mentions=members.filter(m=>draft.atAllMemberIds.includes(m.id))
          const item: Subscription = { ...draft, atAllMemberIds: mentionRestricted ? mentions.map(m=>m.id) : [], atAllMemberNames: mentionRestricted ? mentions.map(m=>m.name) : [], id: editing || crypto.randomUUID(), groupId: Number(group), communityId: community.id, communityName: community.name, slug: community.slug, memberIds: allMembers ? [] : chosen.map(m => m.id), memberNames: allMembers ? [] : chosen.map(m => m.name), enabled: true }
          await save({ ...settings, subscriptions: [...settings.subscriptions.filter(s => s.id !== item.id), item] }); setCommunity(undefined); setEditing('')
        })}>{editing ? '保存订阅' : '添加订阅'}</button>
      </div>}
    </section>
    <section className="wv-card"><h3>已订阅</h3>
      {!settings.subscriptions.length && <p className="muted">还没有订阅。搜索 Hearts2Hearts 后选择成员和推送群。</p>}
      {settings.subscriptions.map(s => <div className="wv-subscription" key={s.id}><div><strong>{s.communityName} · {s.memberNames.length ? s.memberNames.join('、') : '整团'}</strong><p>QQ群 {s.groupId} · {events.filter(([k]) => s[k]).map(([, label]) => label).join(' / ')}</p>{s.atAll && <p>@全体：{s.atAllMemberIds?.length ? (s.atAllMemberNames || []).join('、') : '全部成员'}</p>}</div><div className="wv-actions">
        <button className="secondary-button" disabled={!!busy} onClick={() => void action('toggle', () => save({ ...settings, subscriptions: settings.subscriptions.map(v => v.id === s.id ? { ...v, enabled: !v.enabled } : v) }))}>{s.enabled ? '暂停' : '启用'}</button>
        <button className="secondary-button" disabled={!!busy} onClick={() => void selectCommunity({ id: s.communityId, name: s.communityName, slug: s.slug, members: s.memberNames }, s)}>编辑</button>
        <button className="secondary-button" disabled={!!busy} onClick={() => { if (window.confirm(`删除 ${s.communityName} 的这条订阅？`)) void action('delete', () => save({ ...settings, subscriptions: settings.subscriptions.filter(v => v.id !== s.id) })) }}>删除</button>
      </div></div>)}
    </section>
    {preview.length > 0 && <section className="wv-card"><h3>只读预览</h3>{preview.slice(0,10).map((e,i) => <article className="wv-preview" key={i}><strong>【{e.author}|Weverse{e.kind === 'comment' ? '回复' : e.kind === 'live' ? '直播' : '动态'}】</strong>{e.parentBody && <><p>原文上下文：{e.parentBody}</p>{e.parentTranslation && <p>中文（机器翻译）：{e.parentTranslation}</p>}</>}<p>{e.body}</p>{e.translation && <p>中文（机器翻译）<br />{e.translation}</p>}{e.translationError && <p className="muted">翻译暂不可用：{e.translationError}</p>}<a href={e.url} target="_blank" rel="noreferrer">查看原文</a></article>)}</section>}
  </div>
}
