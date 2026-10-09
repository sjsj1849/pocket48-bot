import { useEffect, useState } from 'react'
import { Pencil, Search, Trash2 } from 'lucide-react'
import { api } from '../api'
import { PlatformField, PlatformToggle } from '../components/PlatformField'
import { markDraftClean, useDraftedSettings } from '../drafts'
import { TargetSelector, useTargets } from '../components/TargetSelector'

type User = { id: string; username: string; name: string; avatar: string; protected: boolean }
type Subscription = { id: string; userId: string; username: string; name: string; groupId: number; targetIds?: string[]; enabled: boolean; posts: boolean; reels: boolean; stories: boolean; atAll: boolean }
type Settings = { enabled: boolean; pollSeconds: number; proxyURL: string; subscriptions: Subscription[] }
type Recovery = { deviceOnline:boolean; deviceState:string; deviceSeenAt?:string; lastSyncAt?:string; message?:string; jobId?:string; status?:string; jobMessage?:string; expiresAt?:string }
type Result = { feedProbe?:{pending:boolean;success:boolean;username?:string;error?:string}; requestState?: {requestsLastHour:number;nextRetryAt?:string;reason?:string}; mobileSession?: {configured:boolean;size?:number;insecurePermissions?:string}; recovery?:Recovery; settings: Settings; sessionConfigured: boolean; sessionUsername?: string; status: { lastCheck?: string; lastSuccess?: string; error?: string; targets?: Record<string,string> } }
type Event = { id: string; body: string; url: string; time: number; author: User; media: Array<{kind:string;cover?:string;url?:string}> }
const choices = [['posts','帖子'],['reels','Reels'],['stories','Story'],['atAll','@全体成员']] as const
const defaults = {posts:true,reels:true,stories:false,atAll:false}

export function InstagramConfig({ defaultGroup }: { defaultGroup: string }) {
 const [data,setData]=useState<Result>()
 const [settings,setSettings]=useState<Settings>()
 const [baseline,setBaseline]=useState<Settings>()
 const [username,setUsername]=useState('')
 const [password,setPassword]=useState('')
 const [code,setCode]=useState('')
 const [query,setQuery]=useState('')
 const [users,setUsers]=useState<User[]>([])
 const [selected,setSelected]=useState<User>()
 const [group,setGroup]=useState(defaultGroup)
 const [targetIds,setTargetIds]=useState<string[]>([])
 const { formatTargetIds } = useTargets()
 const [draft,setDraft]=useState(defaults)
 const [editing,setEditing]=useState('')
 const [busy,setBusy]=useState('')
 const [error,setError]=useState('')
 const [message,setMessage]=useState('')
 const [preview,setPreview]=useState<Event[]>([])
 useEffect(()=>{const c=new AbortController();const load=()=>api<Result>('instagram/settings',{signal:c.signal}).then(r=>{setData(r);setSettings(v=>v??r.settings);setBaseline(v=>v??r.settings)}).catch(e=>{if(!c.signal.aborted)setError(e.message)});void load();const timer=window.setInterval(()=>void load(),5000);return()=>{c.abort();window.clearInterval(timer)}},[])
 // 运行设置跟随右上角「保存更改」一起提交
 useDraftedSettings('instagram','Instagram 运行设置',settings,baseline,async(next:Settings)=>{
  const r=await api<{settings:Settings}>('instagram/settings',{method:'PUT',body:JSON.stringify(next)})
  setSettings(r.settings);setBaseline(r.settings);setData(await api<Result>('instagram/settings'))
  setMessage('Instagram 运行设置已保存，下一次检查生效');return r.settings
 })
 async function action(name:string,work:()=>Promise<void>){setBusy(name);setError('');setMessage('');try{await work()}catch(e){setError(e instanceof Error?e.message:'操作失败')}finally{setBusy('')}}
 // 订阅增删改立即生效，并同步基线
 async function save(next:Settings){const r=await api<{settings:Settings}>('instagram/settings',{method:'PUT',body:JSON.stringify(next)});setSettings(r.settings);setBaseline(r.settings);markDraftClean('instagram');setMessage('已保存，下一次检查生效，无需重启')}
 function edit(s:Subscription){setSelected({id:s.userId,username:s.username,name:s.name,avatar:'',protected:false});setGroup(String(s.groupId));setTargetIds(s.targetIds||[]);setDraft({posts:s.posts,reels:s.reels,stories:s.stories,atAll:s.atAll});setEditing(s.id)}
 function reset(){setSelected(undefined);setEditing('');setDraft(defaults);setGroup(defaultGroup);setTargetIds([])}
 if(!settings)return <div>{error?<p className="inline-error">{error}</p>:<p className="muted">正在加载 Instagram 配置…</p>}</div>
 // 未启用：只展示一栏介绍 + 一栏开关，不渲染任何登录/订阅配置，避免误配
 if(!settings.enabled)return <div className="platform-config x-config platform-gate">
  <section className="platform-section">
   <p className="muted">监控指定账号的帖子、Reels 与 Story。Instagram 的限流与自动化风控会阻断稳定采集，当前仅保留诊断与未来适配入口；未启用时不展示登录、订阅等配置项。开关修改后点右上角「保存更改」。</p>
   <PlatformToggle label="启用 Instagram 监控" checked={settings.enabled} onChange={enabled=>setSettings({...settings,enabled})}/>
  </section>
 </div>
 return <div className="platform-config x-config">
  <p className="platform-notice" role="status">采集走手机端登录态，与采集器共用同一份会话。平台仍可能限流：出现「账号暂不可用」时通常需要等待冷却，下方会显示下一次允许请求的时间。</p>
  {error&&<p className="inline-error" role="alert">{error}</p>}{message&&<p className="platform-notice" role="status">{message}</p>}
  <section className="platform-section"><h3>登录与运行</h3>
   <p className="muted">{data?.mobileSession?.configured?'已保存并启用手机端登录态。':'尚未保存 Instagram 手机端登录态。'} 首次启用只建立基线，不补发历史帖子。</p>
   <p className="muted">采集器近一小时请求：{data?.requestState?.requestsLastHour || 0}{data?.requestState?.nextRetryAt ? ` · 冷却至 ${new Date(data.requestState.nextRetryAt).toLocaleString()}` : ' · 当前没有冷却限制'}</p>
   {data?.feedProbe?.username&&<p className="muted">@{data.feedProbe.username} 帖子替代接口验证：{data.feedProbe.success ? '已通过' : data.feedProbe.pending ? '等待冷却后自动重试（不发送消息）' : '未通过'}{data.feedProbe.error&&` · ${data.feedProbe.error}`}</p>}
   <p className="muted">手机端会话：{data?.mobileSession?.configured ? `已保存（${(data.mobileSession.size ?? 0).toLocaleString()} 字节）` : '尚未安装'}{data?.mobileSession?.insecurePermissions ? ` · 权限 ${data.mobileSession.insecurePermissions} 过宽，请改为 0600` : ''}。日常请求会自动保存平台返回的新状态；彻底失效时才需要手机恢复。</p>
   <p className="muted">恢复手机：{data?.recovery?.deviceOnline ? '在线' : '离线'}{data?.recovery?.deviceSeenAt ? ` · 最近心跳 ${new Date(data.recovery.deviceSeenAt).toLocaleString()}` : ''}{data?.recovery?.lastSyncAt ? ` · 最近同步 ${new Date(data.recovery.lastSyncAt).toLocaleString()}` : ''}</p>
   {(data?.recovery?.jobMessage||data?.recovery?.message)&&<p className="platform-notice" role="status">{data.recovery.jobMessage||data.recovery.message}{data.recovery.expiresAt&&data.recovery.status!=='complete' ? ` · ${new Date(data.recovery.expiresAt).toLocaleTimeString()} 前有效` : ''}</p>}
   <div className="inline-actions"><button className="secondary-button" disabled={!!busy} onClick={()=>void action('refresh',async()=>setData(await api<Result>('instagram/settings')))}>刷新状态</button></div>
   <PlatformField label="Instagram 登录账号"><input autoComplete="username" value={username} onChange={e=>setUsername(e.target.value)} placeholder="用户名、邮箱或带区号的手机号" /></PlatformField>
   <PlatformField label="Instagram 密码" description="只在服务内存中保留 10 分钟，手机取走或任务结束后立即清零，不写入磁盘。"><input type="password" autoComplete="current-password" value={password} onChange={e=>setPassword(e.target.value)} /></PlatformField>
   <div className="inline-actions"><button className="primary-button" disabled={!!busy||!username||!password} onClick={()=>void action('login',async()=>{try{const recovery=await api<Recovery>('instagram/recovery',{method:'POST',body:JSON.stringify({username,password})});setData(v=>v?{...v,recovery}:v);setMessage('恢复任务已创建，等待手机处理')}finally{setPassword('')}})}>让手机恢复登录态</button>{data?.recovery?.jobId&&data.recovery.status!=='complete'&&<button className="secondary-button" disabled={!!busy} onClick={()=>void action('cancel',async()=>{const recovery=await api<Recovery>('instagram/recovery',{method:'DELETE'});setData(v=>v?{...v,recovery}:v);setMessage('恢复任务已取消')})}>取消恢复</button>}</div>
   <PlatformField label="短信或二次验证码" description="只有手机状态显示正在等待验证码时才需要填写。"><input inputMode="numeric" autoComplete="one-time-code" value={code} onChange={e=>setCode(e.target.value)} placeholder="6–16 位验证码" /></PlatformField>
   <button className="secondary-button" disabled={!!busy||code.trim().length<6||code.trim().length>16} onClick={()=>void action('verify',async()=>{const recovery=await api<Recovery>('instagram/recovery',{method:'PUT',body:JSON.stringify({code:code.trim()})});setCode('');setData(v=>v?{...v,recovery}:v);setMessage('验证码已发送到手机')})}>提交验证码</button>
   <div className="inline-actions"><button className="secondary-button" disabled={!!busy||!data?.mobileSession?.configured} onClick={()=>void action('check',async()=>{await api('instagram/session');setMessage('手机端登录态有效')})}>检查登录态</button><button className="secondary-button" disabled={!!busy||!data?.mobileSession?.configured} onClick={()=>void action('clear',async()=>{if(!window.confirm('清除 Instagram 手机端登录态？清除后必须通过手机恢复才能继续采集。'))return;await api('instagram/session',{method:'DELETE'});setData(await api<Result>('instagram/settings'));setMessage('已清除手机端登录态')})}>清除登录态</button></div>
   <PlatformToggle label="启用 Instagram 监控" description="开启后按上面的间隔采集并推送到各订阅配置的目标群。首次启用只建立基线，不补发历史内容。" checked={settings.enabled} onChange={enabled=>setSettings({...settings,enabled})}/>
   <PlatformField label="采集周期（秒）" description="60–3600 秒。数值越小越及时，但平台限流时会更容易触发冷却；出现「账号暂不可用」可适当调大。"><input type="number" min="60" max="3600" value={settings.pollSeconds} onChange={e=>setSettings({...settings,pollSeconds:Number(e.target.value)})}/></PlatformField>
   <PlatformField label="网络代理（可选）" description="留空使用服务器网络。"><input value={settings.proxyURL||''} placeholder="http:// 或 socks5://" onChange={e=>setSettings({...settings,proxyURL:e.target.value})}/></PlatformField>
   <p className="muted">检查间隔与网络代理修改后点右上角「保存更改」。</p>
   {data?.status.lastCheck&&<p className="muted">最近检查：{new Date(data.status.lastCheck).toLocaleString()} · {data.status.error||'正常'}</p>}
  </section>
  <section className="platform-section"><h3>搜索与订阅</h3><p className="muted">支持 @用户名或 Instagram 主页链接，精确查找账号。</p>
   <form className="platform-search" onSubmit={e=>{e.preventDefault();void action('search',async()=>{const r=await api<{users:User[]}>(`instagram/search?q=${encodeURIComponent(query)}`);setUsers(r.users);if(!r.users.length)setMessage('没有找到匹配账号')})}}><input aria-label="搜索 Instagram 账号" value={query} onChange={e=>setQuery(e.target.value)}/><button className="secondary-button" disabled={!!busy}><Search size={16}/>{busy==='search'?'搜索中…':'搜索'}</button></form>
   <div className="platform-results">{users.map(u=><button className={`platform-result${selected?.id===u.id?' selected':''}`} key={u.id} disabled={!!busy} onClick={()=>{setSelected(u);setEditing('');setGroup(defaultGroup);setDraft(defaults)}}>{u.avatar&&<img src={u.avatar} alt="" loading="lazy"/>}<span><strong>{u.name}</strong><small>@{u.username}{u.protected?' · 私密账号，读取取决于登录账号权限':''}</small></span></button>)}</div>
   {selected&&<div className="platform-editor"><h4>{editing?'编辑订阅':'添加订阅'} · {selected.name} (@{selected.username})</h4><PlatformField label="投递目标" description="勾选消息投递目标（可多选）。"><TargetSelector value={targetIds} onChange={setTargetIds} /></PlatformField><div className="platform-choices">{choices.map(([key,label])=><label key={key}><input type="checkbox" checked={draft[key]} onChange={e=>setDraft({...draft,[key]:e.target.checked})}/>{label}</label>)}</div><div className="inline-actions"><button className="primary-button" disabled={!!busy||(!draft.posts&&!draft.reels&&!draft.stories)} onClick={()=>void action('subscribe',async()=>{const sub:Subscription={...draft,id:editing||crypto.randomUUID(),userId:selected.id,username:selected.username,name:selected.name,groupId:editing?(settings.subscriptions.find(v=>v.id===editing)?.groupId??Number(group)):Number(group),targetIds,enabled:true};await save({...settings,subscriptions:editing?settings.subscriptions.map(v=>v.id===sub.id?sub:v):[...settings.subscriptions,sub]});reset()})}>{editing?'保存订阅':'添加订阅'}</button><button className="secondary-button" disabled={!!busy} onClick={reset}>取消</button><button className="secondary-button" disabled={!!busy} onClick={()=>void action('preview',async()=>{const r=await api<{events:Event[];message:string}>('instagram/preview',{method:'POST',body:JSON.stringify({username:selected.username})});setPreview(r.events);setMessage(r.message)})}>{busy==='preview'?'测试中…':'只读测试（不推送）'}</button></div></div>}
  </section>
  <section className="platform-section"><h3>已订阅账号</h3>{!settings.subscriptions.length&&<p className="muted">还没有订阅，请先搜索账号并设置推送群。</p>}{settings.subscriptions.map(s=><article className="douyin-card" key={s.id}><div className="douyin-card-main"><div className="douyin-card-heading"><strong>{s.name||s.username}</strong><span className="quiet-badge">{s.enabled?'已启用':'已暂停'}</span></div><p className="muted">@{s.username} · {formatTargetIds(s.targetIds) || '未设置投递目标'}</p><p className="muted">{choices.filter(([key])=>s[key]).map(([,label])=>label).join(' / ')}</p>{data?.status.targets?.[s.id]&&<p className="muted">{data.status.targets[s.id]}</p>}</div><div className="sub-item-actions"><button className="secondary-button" disabled={!!busy} onClick={()=>void action('toggle',()=>save({...settings,subscriptions:settings.subscriptions.map(v=>v.id===s.id?{...v,enabled:!v.enabled}:v)}))}>{s.enabled?'暂停':'启用'}</button><button className="icon-button" aria-label={`编辑 Instagram 订阅 ${s.username}`} disabled={!!busy} onClick={()=>edit(s)}><Pencil size={15}/></button><button className="icon-button danger" aria-label={`删除 Instagram 订阅 ${s.username}`} disabled={!!busy} onClick={()=>{if(window.confirm(`删除 @${s.username} 的订阅？`))void action('delete',()=>save({...settings,subscriptions:settings.subscriptions.filter(v=>v.id!==s.id)}))}}><Trash2 size={16}/></button></div></article>)}</section>
  {!!preview.length&&<section className="platform-section"><h3>只读预览</h3>{preview.map(e=><article className="platform-preview" key={e.id}><strong>{e.author.name}</strong><p>{e.body}</p><p className="muted">{e.media.map(m=>m.kind==='video'?'视频':(m.kind==='gif'||m.kind==='animated')?'动图':'图片').join(' / ')||'纯文本'}</p><a href={e.url} target="_blank" rel="noreferrer">查看原帖</a></article>)}</section>}
 </div>
}
