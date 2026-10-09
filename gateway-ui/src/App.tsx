import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  Activity, Check, ChevronRight, CircleAlert, ContactRound, Copy,
  Database, Edit3, ExternalLink, History, Home, KeyRound, Link2, Menu, MessageSquare,
  Plus, RefreshCw, Route as RouteIcon, Save, Send, Settings2, Trash2, X,
} from 'lucide-react'
import { api, getAPIKey, setAPIKey } from './api'
import type { GatewayConfig, GatewayState, Page, Platform, Route, Target } from './types'

const nav: Array<{ id: Page; label: string; icon: typeof Home }> = [
  { id: 'overview', label: '总览', icon: Home },
  { id: 'platforms', label: '平台连接', icon: Link2 },
  { id: 'targets', label: '目标地址簿', icon: ContactRound },
  { id: 'routes', label: '路由规则', icon: RouteIcon },
  { id: 'deliveries', label: '投递记录', icon: History },
  { id: 'settings', label: '设置', icon: Settings2 },
]

const platformMeta: Record<Platform, { name: string; description: string; short: string }> = {
  qq: { name: 'QQ / OneBot', short: 'QQ', description: '通过 NapCat 或 LLOneBot 提供 QQ 消息出口' },
  feishu: { name: '飞书', short: '飞书', description: '飞书开放平台应用与原生消息卡片' },
  telegram: { name: 'Telegram', short: 'TG', description: '预留的 Telegram Bot API 出口' },
}

function id(prefix: string) { return `${prefix}_${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}` }
function when(value?: string) {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

function Button({ children, tone = 'secondary', ...props }: React.ButtonHTMLAttributes<HTMLButtonElement> & { tone?: 'primary' | 'secondary' | 'danger' | 'text' }) {
  return <button {...props} className={`button ${tone} ${props.className || ''}`}>{children}</button>
}

function Empty({ children }: { children: React.ReactNode }) { return <div className="empty"><Database size={22} /><p>{children}</p></div> }

function Modal({ title, subtitle, children, onClose }: { title: string; subtitle?: string; children: React.ReactNode; onClose: () => void }) {
  return <div className="modal-layer" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}>
    <section className="modal" role="dialog" aria-modal="true" aria-label={title}>
      <header><div><h2>{title}</h2>{subtitle && <p>{subtitle}</p>}</div><button className="icon-button" onClick={onClose}><X size={18} /></button></header>
      {children}
    </section>
  </div>
}

function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return <label className="field"><span>{label}{hint && <small>{hint}</small>}</span>{children}</label>
}

export function App() {
  const [page, setPage] = useState<Page>('overview')
  const [drawer, setDrawer] = useState(false)
  const [state, setState] = useState<GatewayState | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [saved, setSaved] = useState('')

  const refresh = useCallback(async () => {
    setLoading(true); setError('')
    try { setState(await api.state()) } catch (reason) { setError(reason instanceof Error ? reason.message : '无法连接消息出口服务') }
    finally { setLoading(false) }
  }, [])
  useEffect(() => { void refresh() }, [refresh])

  async function saveConfig(config: GatewayConfig, note = '配置已保存，重启网关后连接配置生效') {
    await api.save(config)
    setState((current) => current ? { ...current, config } : current)
    setSaved(note); window.setTimeout(() => setSaved(''), 3200)
  }

  const content = !state ? null : page === 'overview' ? <Overview state={state} navigate={setPage} refresh={refresh} />
    : page === 'platforms' ? <Platforms state={state} save={saveConfig} />
      : page === 'targets' ? <Targets state={state} save={saveConfig} />
        : page === 'routes' ? <Routes state={state} save={saveConfig} />
          : page === 'deliveries' ? <Deliveries state={state} />
            : <Settings state={state} save={saveConfig} />

  return <div className="app-shell">
    <aside className={`sidebar ${drawer ? 'open' : ''}`}>
      <div className="sidebar-brand"><div className="brand-mark"><Send size={15} /></div><span>Message Gateway</span></div>
      <button className="icon-button close-drawer" onClick={() => setDrawer(false)}><X size={18} /></button>
      <nav><p className="nav-label">消息出口</p>{nav.map(({ id: item, label, icon: Icon }) => <button key={item} className={page === item ? 'active' : ''} onClick={() => { setPage(item); setDrawer(false) }}><Icon size={17} /><span>{label}</span></button>)}</nav>
      <div className="sidebar-foot"><div><i className={`status-dot ${state ? 'healthy' : 'down'}`} /><span>{state ? '服务已连接' : '等待连接'}</span></div><small>独立于 Pocket48 运行</small></div>
    </aside>
    {drawer && <button className="drawer-scrim" onClick={() => setDrawer(false)} />}
    <div className="workspace">
      <header className="topbar"><div><button className="icon-button menu-button" onClick={() => setDrawer(true)}><Menu size={19} /></button><span>Message Gateway</span><small>/ {nav.find((item) => item.id === page)?.label}</small></div><span className="top-status"><i />系统运行正常</span></header>
      {loading && !state ? <main className="boot"><span /></main> : error && !state ? <main className="load-error"><CircleAlert /><h1>无法读取网关状态</h1><p>{error}</p><Button onClick={refresh}><RefreshCw size={15} />重新连接</Button></main> : <div key={page} className="page-enter">{content}</div>}
    </div>
    {saved && <div className="toast"><Check size={16} />{saved}</div>}
  </div>
}

function PageHeading({ eyebrow, title, description, children }: { eyebrow: string; title: string; description: string; children?: React.ReactNode }) {
  return <header className="page-heading"><div><p className="eyebrow">{eyebrow}</p><h1>{title}</h1><p>{description}</p></div>{children && <div className="heading-actions">{children}</div>}</header>
}

function Overview({ state, navigate, refresh }: { state: GatewayState; navigate: (page: Page) => void; refresh: () => void }) {
  const platforms = (['qq', 'feishu', 'telegram'] as Platform[]).map((key) => ({ key, enabled: state.config[key].enabled }))
  const enabled = platforms.filter((item) => item.enabled).length
  const failed = state.records.filter((item) => item.status === 'failed').length
  return <main className="page-content">
    <PageHeading eyebrow="MESSAGE DELIVERY" title="消息出口总览" description="集中管理所有项目的消息平台、目标地址与投递路由。"><span className="updated">运行 {state.uptime}</span><button className="icon-button" onClick={refresh}><RefreshCw size={16} /></button></PageHeading>
    <div className="attention-band"><Activity size={18} /><div><strong>统一出口服务正在运行</strong><p>已启用 {enabled} 个平台，保存的目标地址 {state.config.targets.length} 个，当前队列 {state.queueDepth} 条。</p></div><button onClick={() => navigate('platforms')}>管理平台 <ChevronRight size={15} /></button></div>
    <section className="section-block">
      <div className="section-title"><div><h2>平台连接</h2><p>平台负责处理各自的富文本、多媒体上传与格式差异。</p></div><Button onClick={() => navigate('platforms')}><Plus size={14} />管理平台</Button></div>
      <div className="platform-table table-head"><span>平台名称</span><span>状态</span><span>连接方式</span><span>账号 / 标识</span><span>操作</span></div>
      {platforms.map(({ key, enabled: on }) => <div className="platform-table table-row" key={key}><div className={`platform-icon ${key}`}>{platformMeta[key].short}</div><div><strong>{platformMeta[key].name}</strong><small>{platformMeta[key].description}</small></div><span><i className={`status-dot ${on ? 'healthy' : ''}`} />{on ? '已启用' : '未启用'}</span><span>{key === 'qq' ? 'WebSocket' : key === 'feishu' ? 'Open API' : 'Bot API'}</span><span className="mono">{key === 'qq' ? state.config.qq.wsUrl : key === 'feishu' ? state.config.feishu.appId || '—' : '—'}</span><button className="text-button" onClick={() => navigate('platforms')}>配置</button></div>)}
    </section>
    <div className="overview-grid">
      <section className="section-block"><div className="section-title"><div><h2>最近投递记录</h2><p>展示网关最近接收到的消息。</p></div><button className="text-button" onClick={() => navigate('deliveries')}>查看全部 <ChevronRight size={14} /></button></div>{state.records.length ? <div className="delivery-list">{state.records.slice(0, 5).map((record, index) => <div className="delivery-row" key={`${record.time}-${index}`}><time>{when(record.time)}</time><i className={`status-dot ${record.status === 'failed' ? 'down' : 'healthy'}`} /><div><strong>{record.target || '未命名目标'} <span>{record.platform}</span></strong><p>{record.summary}</p></div><em>{record.status === 'failed' ? '失败' : '已接收'}</em></div>)}</div> : <Empty>尚无投递记录。Pocket48 接入统一接口后，记录会显示在这里。</Empty>}</section>
      <section className="section-block"><div className="section-title"><div><h2>队列与运行状态</h2><p>当前网关进程的实时摘要。</p></div></div><div className="stat-grid"><div><span>等待投递</span><strong>{state.queueDepth}</strong><small>条</small></div><div><span>失败记录</span><strong>{failed}</strong><small>条</small></div></div><dl className="runtime-list"><div><dt>系统运行时间</dt><dd>{state.uptime}</dd></div><div><dt>已配置路由</dt><dd>{state.config.routes.length} 条</dd></div><div><dt>常用目标</dt><dd>{state.config.targets.filter((item) => item.favorite).length} 个</dd></div></dl></section>
    </div>
    <section className="section-block targets-preview"><div className="section-title"><div><h2>常用目标地址</h2><p>保存常用群聊或私聊目标，Pocket48 配置时可直接选择。</p></div><Button onClick={() => navigate('targets')}><Plus size={14} />添加目标</Button></div><TargetTable targets={state.config.targets.filter((item) => item.favorite).slice(0, 5)} /></section>
  </main>
}

function Platforms({ state, save }: { state: GatewayState; save: (config: GatewayConfig) => Promise<void> }) {
  const [editing, setEditing] = useState<Platform | null>(null)
  const [draft, setDraft] = useState(state.config)
  const [busy, setBusy] = useState(false)
  useEffect(() => setDraft(state.config), [state.config])
  async function submit() { setBusy(true); try { await save(draft); setEditing(null) } finally { setBusy(false) } }
  return <main className="page-content"><PageHeading eyebrow="PLATFORM ADAPTERS" title="平台连接" description="每个平台保留自己的发送规则；业务消息不需要了解平台 API。" />
    <div className="platform-cards">{(['qq', 'feishu', 'telegram'] as Platform[]).map((key) => { const config = draft[key]; return <section className="platform-card" key={key}><div className={`platform-icon large ${key}`}>{platformMeta[key].short}</div><div><div className="card-heading"><h2>{platformMeta[key].name}</h2><span className={`status-pill ${config.enabled ? 'healthy' : ''}`}>{config.enabled ? '已启用' : '未启用'}</span></div><p>{platformMeta[key].description}</p><small>{key === 'qq' ? draft.qq.wsUrl : key === 'feishu' ? draft.feishu.appId || '尚未填写 App ID' : '适配器暂未实现，仅保留配置入口'}</small></div><Button onClick={() => setEditing(key)}><Settings2 size={14} />配置</Button></section> })}</div>
    <aside className="note"><CircleAlert size={17} /><div><strong>OneBot 服务仍然需要保留</strong><p>它继续负责真正连接 QQ；Message Gateway 位于业务项目与 OneBot 之间，统一管理出口和路由。停用 QQ 出口后才可以停止 OneBot。</p></div></aside>
    {editing && <Modal title={`配置${platformMeta[editing].name}`} subtitle={platformMeta[editing].description} onClose={() => setEditing(null)}><div className="modal-body">
      <label className="switch-line"><div><strong>启用平台</strong><small>允许路由向这个平台投递消息</small></div><input type="checkbox" checked={draft[editing].enabled} onChange={(e) => setDraft({ ...draft, [editing]: { ...draft[editing], enabled: e.target.checked } })} /><i /></label>
      {editing === 'qq' && <><Field label="WebSocket 地址" hint="NapCat / LLOneBot 正向 WebSocket"><input value={draft.qq.wsUrl} onChange={(e) => setDraft({ ...draft, qq: { ...draft.qq, wsUrl: e.target.value } })} placeholder="ws://127.0.0.1:3001" /></Field><Field label="访问令牌"><input type="password" value={draft.qq.accessToken || ''} onChange={(e) => setDraft({ ...draft, qq: { ...draft.qq, accessToken: e.target.value } })} /></Field></>}
      {editing === 'feishu' && <><Field label="App ID"><input value={draft.feishu.appId} onChange={(e) => setDraft({ ...draft, feishu: { ...draft.feishu, appId: e.target.value } })} placeholder="cli_..." /></Field><Field label="App Secret"><input type="password" value={draft.feishu.appSecret || ''} onChange={(e) => setDraft({ ...draft, feishu: { ...draft.feishu, appSecret: e.target.value } })} /></Field><div className="field-pair"><Field label="并行上传数"><input type="number" min="1" max="8" value={draft.feishu.uploadConcurrency} onChange={(e) => setDraft({ ...draft, feishu: { ...draft.feishu, uploadConcurrency: Number(e.target.value) } })} /></Field><Field label="视频等待秒数"><input type="number" min="1" max="30" value={draft.feishu.videoWaitSeconds} onChange={(e) => setDraft({ ...draft, feishu: { ...draft.feishu, videoWaitSeconds: Number(e.target.value) } })} /></Field></div><Field label="小视频上限（MB）" hint="小于该体积时优先等待上传并和主体一起投递"><input type="number" min="1" value={Math.round(draft.feishu.smallVideoBytes / 1048576)} onChange={(e) => setDraft({ ...draft, feishu: { ...draft.feishu, smallVideoBytes: Number(e.target.value) * 1048576 } })} /></Field></>}
      {editing === 'telegram' && <Field label="Bot Token"><input type="password" value={draft.telegram.botToken || ''} onChange={(e) => setDraft({ ...draft, telegram: { ...draft.telegram, botToken: e.target.value } })} placeholder="123456:ABC..." /></Field>}
    </div><footer className="modal-actions"><Button onClick={() => setEditing(null)}>取消</Button><Button tone="primary" disabled={busy} onClick={submit}><Save size={14} />{busy ? '保存中…' : '保存配置'}</Button></footer></Modal>}
  </main>
}

function TargetTable({ targets, actions }: { targets: Target[]; actions?: (target: Target) => React.ReactNode }) {
  if (!targets.length) return <Empty>还没有目标地址。</Empty>
  return <div className="data-table target-table"><div className="data-head"><span>目标名称</span><span>类型</span><span>关联平台</span><span>目标标识</span><span>说明</span>{actions && <span>操作</span>}</div>{targets.map((target) => <div className="data-row" key={target.id}><strong>{target.name}{target.favorite && <span className="favorite">常用</span>}</strong><span>{target.kind === 'group' ? '群聊' : '私聊'}</span><span>{platformMeta[target.platform]?.name || target.platform}</span><code>{target.address}</code><span className="muted">{target.description || '—'}</span>{actions && <div className="row-actions">{actions(target)}</div>}</div>)}</div>
}

function Targets({ state, save }: { state: GatewayState; save: (config: GatewayConfig, note?: string) => Promise<void> }) {
  const empty: Target = { id: '', name: '', platform: 'qq', kind: 'group', address: '', favorite: true, description: '' }
  const [draft, setDraft] = useState<Target | null>(null)
  const [busy, setBusy] = useState(false)
  async function submit() { if (!draft) return; setBusy(true); try { const next = { ...draft, id: draft.id || id('target') }; const targets = state.config.targets.some((item) => item.id === next.id) ? state.config.targets.map((item) => item.id === next.id ? next : item) : [...state.config.targets, next]; await save({ ...state.config, targets }, '目标地址已保存'); setDraft(null) } finally { setBusy(false) } }
  async function remove(target: Target) { if (!confirm(`删除目标“${target.name}”？使用它的路由也需要随后调整。`)) return; await save({ ...state.config, targets: state.config.targets.filter((item) => item.id !== target.id) }, '目标地址已删除') }
  return <main className="page-content"><PageHeading eyebrow="ADDRESS BOOK" title="目标地址簿" description="统一保存 QQ 群、QQ 私聊、飞书群聊和飞书账号，配置转发时直接选择。"><Button tone="primary" onClick={() => setDraft(empty)}><Plus size={15} />添加目标</Button></PageHeading>
    <section className="section-block"><div className="section-title"><div><h2>全部目标</h2><p>共 {state.config.targets.length} 个地址，其中 {state.config.targets.filter((item) => item.favorite).length} 个标记为常用。</p></div></div><TargetTable targets={state.config.targets} actions={(target) => <><button title="测试发送" onClick={() => void api.test(target.id)}><Send size={14} /></button><button title="编辑" onClick={() => setDraft(target)}><Edit3 size={14} /></button><button className="danger-icon" title="删除" onClick={() => void remove(target)}><Trash2 size={14} /></button></>} /></section>
    {draft && <Modal title={draft.id ? '编辑目标地址' : '添加目标地址'} subtitle="平台之间的目标相互独立，不会被强制绑定。" onClose={() => setDraft(null)}><div className="modal-body"><Field label="显示名称"><input value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} placeholder="例如：运营通知群" /></Field><div className="field-pair"><Field label="出口平台"><select value={draft.platform} onChange={(e) => setDraft({ ...draft, platform: e.target.value as Platform })}><option value="qq">QQ / OneBot</option><option value="feishu">飞书</option><option value="telegram">Telegram</option></select></Field><Field label="会话类型"><select value={draft.kind} onChange={(e) => setDraft({ ...draft, kind: e.target.value as Target['kind'] })}><option value="group">群聊</option><option value="private">私聊</option></select></Field></div><Field label="目标标识" hint={draft.platform === 'qq' ? 'QQ群号或 QQ 号' : draft.platform === 'feishu' ? '飞书 Chat ID 或 Open ID' : 'Chat ID'}><input value={draft.address} onChange={(e) => setDraft({ ...draft, address: e.target.value })} /></Field><Field label="说明"><input value={draft.description || ''} onChange={(e) => setDraft({ ...draft, description: e.target.value })} placeholder="这个目标主要接收什么消息" /></Field><label className="check-line"><input type="checkbox" checked={draft.favorite} onChange={(e) => setDraft({ ...draft, favorite: e.target.checked })} />加入常用目标，供 Pocket48 快速选择</label></div><footer className="modal-actions"><Button onClick={() => setDraft(null)}>取消</Button><Button tone="primary" disabled={busy || !draft.name || !draft.address} onClick={submit}><Save size={14} />保存目标</Button></footer></Modal>}
  </main>
}

function Routes({ state, save }: { state: GatewayState; save: (config: GatewayConfig, note?: string) => Promise<void> }) {
  const empty: Route = { id: '', name: '', project: 'pocket48', event: '*', targetIds: [], enabled: true }
  const [draft, setDraft] = useState<Route | null>(null)
  async function persist(route: Route) { const next = { ...route, id: route.id || id('route') }; const routes = state.config.routes.some((item) => item.id === next.id) ? state.config.routes.map((item) => item.id === next.id ? next : item) : [...state.config.routes, next]; await save({ ...state.config, routes }, '路由规则已保存'); setDraft(null) }
  async function remove(route: Route) { if (confirm(`删除路由“${route.name}”？`)) await save({ ...state.config, routes: state.config.routes.filter((item) => item.id !== route.id) }, '路由规则已删除') }
  return <main className="page-content"><PageHeading eyebrow="ROUTING" title="路由规则" description="同一来源可以投递到不同平台和不同目标，不要求 QQ 与飞书完全一致。"><Button tone="primary" onClick={() => setDraft(empty)}><Plus size={15} />添加路由</Button></PageHeading>
    <aside className="note"><RouteIcon size={17} /><div><strong>迁移映射只是路由的一种</strong><p>可以按项目和事件分别配置目标；填写旧 QQ 地址时可兼容现有绑定，完成迁移后可改成完全独立的目标选择。</p></div></aside>
    <section className="section-block route-list">{state.config.routes.length ? state.config.routes.map((route) => <article className="route-row" key={route.id}><i className={`status-dot ${route.enabled ? 'healthy' : ''}`} /><div><strong>{route.name}</strong><p>{route.project || '*'} / {route.event || '*'}{route.legacyAddress ? ` · 旧地址 ${route.legacyAddress}` : ''}</p></div><div className="route-targets">{route.targetIds.map((targetId) => <span key={targetId}>{state.config.targets.find((item) => item.id === targetId)?.name || '已删除目标'}</span>)}</div><div className="row-actions"><button onClick={() => setDraft(route)}><Edit3 size={14} /></button><button className="danger-icon" onClick={() => void remove(route)}><Trash2 size={14} /></button></div></article>) : <Empty>还没有路由规则。可以先建立 QQ 到飞书的一对一迁移路由。</Empty>}</section>
    {draft && <Modal title={draft.id ? '编辑路由规则' : '添加路由规则'} subtitle="项目和事件可使用 * 匹配全部消息。" onClose={() => setDraft(null)}><div className="modal-body"><Field label="路由名称"><input value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} placeholder="例如：Pocket48 主群通知" /></Field><div className="field-pair"><Field label="来源项目"><input value={draft.project} onChange={(e) => setDraft({ ...draft, project: e.target.value })} /></Field><Field label="事件类型"><input value={draft.event} onChange={(e) => setDraft({ ...draft, event: e.target.value })} /></Field></div><Field label="投递目标" hint="可以同时选择多个平台"><div className="choice-list">{state.config.targets.map((target) => <label key={target.id}><input type="checkbox" checked={draft.targetIds.includes(target.id)} onChange={(e) => setDraft({ ...draft, targetIds: e.target.checked ? [...draft.targetIds, target.id] : draft.targetIds.filter((value) => value !== target.id) })} /><span>{target.name}<small>{platformMeta[target.platform].name} · {target.kind === 'group' ? '群聊' : '私聊'}</small></span></label>)}</div></Field><details className="legacy-fields"><summary>现有 QQ 绑定兼容映射（可选）</summary><div className="field-pair"><Field label="旧平台"><select value={draft.legacyPlatform || ''} onChange={(e) => setDraft({ ...draft, legacyPlatform: e.target.value })}><option value="">不限制</option><option value="qq">QQ</option></select></Field><Field label="旧会话类型"><select value={draft.legacyKind || ''} onChange={(e) => setDraft({ ...draft, legacyKind: e.target.value })}><option value="">不限制</option><option value="group">群聊</option><option value="private">私聊</option></select></Field></div><Field label="旧 QQ 号 / 群号"><input value={draft.legacyAddress || ''} onChange={(e) => setDraft({ ...draft, legacyAddress: e.target.value })} /></Field></details><label className="check-line"><input type="checkbox" checked={draft.enabled} onChange={(e) => setDraft({ ...draft, enabled: e.target.checked })} />启用这条路由</label></div><footer className="modal-actions"><Button onClick={() => setDraft(null)}>取消</Button><Button tone="primary" disabled={!draft.name || !draft.targetIds.length} onClick={() => void persist(draft)}><Save size={14} />保存路由</Button></footer></Modal>}
  </main>
}

function Deliveries({ state }: { state: GatewayState }) {
  const [query, setQuery] = useState('')
  const records = useMemo(() => state.records.filter((item) => `${item.project} ${item.event} ${item.summary} ${item.target} ${item.platform}`.toLowerCase().includes(query.toLowerCase())), [state.records, query])
  return <main className="page-content"><PageHeading eyebrow="DELIVERY HISTORY" title="投递记录" description="查看业务消息经过统一出口后的目标、平台和处理结果。" />
    <section className="section-block"><div className="list-toolbar"><div className="search"><MessageSquare size={15} /><input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="搜索来源、目标或消息内容" /></div><span>{records.length} 条记录</span></div>{records.length ? <div className="delivery-table"><div className="data-head"><span>时间</span><span>来源</span><span>目标</span><span>消息摘要</span><span>耗时</span><span>状态</span></div>{records.map((record, index) => <div className="data-row" key={`${record.time}-${index}`}><time>{when(record.time)}</time><span>{record.project}<small>{record.event}</small></span><span>{record.target}<small>{platformMeta[record.platform]?.name || record.platform}</small></span><p title={record.summary}>{record.summary}</p><span>{record.latencyMs} ms</span><span className={`status-label ${record.status}`}>{record.status === 'failed' ? '失败' : record.status === 'retrying' ? '重试中' : '已接收'}</span></div>)}</div> : <Empty>没有符合条件的投递记录。</Empty>}</section>
  </main>
}

function Settings({ state, save }: { state: GatewayState; save: (config: GatewayConfig) => Promise<void> }) {
  const [draft, setDraft] = useState(state.config)
  const [key, setKey] = useState(getAPIKey())
  return <main className="page-content"><PageHeading eyebrow="GATEWAY SETTINGS" title="网关设置" description="配置服务监听地址，以及 Pocket48 调用统一出口时使用的访问密钥。"><Button tone="primary" onClick={() => void save(draft)}><Save size={15} />保存设置</Button></PageHeading>
    <section className="section-block settings-form"><div className="section-title"><div><h2>服务配置</h2><p>修改监听地址或访问密钥后需要重启 Message Gateway。</p></div></div><div className="settings-fields"><Field label="监听地址" hint="建议仅监听内网或本机"><input value={draft.address} onChange={(e) => setDraft({ ...draft, address: e.target.value })} /></Field><Field label="服务端 API Key" hint="Pocket48 等调用方需要携带这个密钥"><input type="password" value={draft.apiKey} onChange={(e) => setDraft({ ...draft, apiKey: e.target.value })} placeholder="留空表示不鉴权" /></Field><Field label="本浏览器使用的 API Key" hint="只保存在当前浏览器，用于重新访问启用鉴权后的面板"><div className="copy-field"><input type="password" value={key} onChange={(e) => setKey(e.target.value)} /><Button onClick={() => { setAPIKey(key); location.reload() }}><KeyRound size={14} />应用</Button></div></Field></div></section>
    <section className="section-block api-help"><div className="section-title"><div><h2>Pocket48 接入地址</h2><p>业务端只调用统一消息协议，不再直接调用 NapCat。</p></div></div><div className="endpoint"><code>POST http://{draft.address}/api/v1/messages</code><button onClick={() => navigator.clipboard.writeText(`http://${draft.address}/api/v1/messages`)}><Copy size={14} /></button></div><a href="https://open.feishu.cn/app" target="_blank" rel="noreferrer">前往飞书开放平台创建应用 <ExternalLink size={13} /></a></section>
  </main>
}
