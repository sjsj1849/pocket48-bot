import { useEffect, useState } from 'react'
import { Pencil, Search, Trash2 } from 'lucide-react'
import { api } from '../api'
import { PlatformField, PlatformToggle } from '../components/PlatformField'

type User = { id: string; username: string; name: string; avatar: string; protected: boolean }
type Subscription = { id: string; userId: string; username: string; name: string; groupId: number; enabled: boolean; posts: boolean; reels: boolean; stories: boolean; atAll: boolean }
type Settings = { enabled: boolean; pollSeconds: number; proxyURL: string; subscriptions: Subscription[] }
type Result = { settings: Settings; sessionConfigured: boolean; sessionUsername?: string; status: { lastCheck?: string; lastSuccess?: string; error?: string; targets?: Record<string,string> } }
type Event = { id: string; body: string; url: string; time: number; author: User; media: Array<{kind:string;cover?:string;url?:string}> }
const choices = [['posts','帖子'],['reels','Reels'],['stories','Story'],['atAll','@全体成员']] as const
const defaults = {posts:true,reels:true,stories:false,atAll:false}

export function InstagramConfig({ defaultGroup }: { defaultGroup: string }) {
 const [data,setData]=useState<Result>()
 const [settings,setSettings]=useState<Settings>()
 const [username,setUsername]=useState('')
 const [password,setPassword]=useState('')
 const [code,setCode]=useState('')
 const [cookies,setCookies]=useState('')
 const [query,setQuery]=useState('')
 const [users,setUsers]=useState<User[]>([])
 const [selected,setSelected]=useState<User>()
 const [group,setGroup]=useState(defaultGroup)
 const [draft,setDraft]=useState(defaults)
 const [editing,setEditing]=useState('')
 const [busy,setBusy]=useState('')
 const [error,setError]=useState('')
 const [message,setMessage]=useState('')
 const [preview,setPreview]=useState<Event[]>([])
 useEffect(()=>{const c=new AbortController();api<Result>('instagram/settings',{signal:c.signal}).then(r=>{setData(r);setSettings(r.settings)}).catch(e=>{if(!c.signal.aborted)setError(e.message)});return()=>c.abort()},[])
 async function action(name:string,work:()=>Promise<void>){setBusy(name);setError('');setMessage('');try{await work()}catch(e){setError(e instanceof Error?e.message:'操作失败')}finally{setBusy('')}}
 async function save(next:Settings){const r=await api<{settings:Settings}>('instagram/settings',{method:'PUT',body:JSON.stringify(next)});setSettings(r.settings);setMessage('已保存，下一次检查生效，无需重启')}
 function edit(s:Subscription){setSelected({id:s.userId,username:s.username,name:s.name,avatar:'',protected:false});setGroup(String(s.groupId));setDraft({posts:s.posts,reels:s.reels,stories:s.stories,atAll:s.atAll});setEditing(s.id)}
 function reset(){setSelected(undefined);setEditing('');setDraft(defaults);setGroup(defaultGroup)}
 if(!settings)return <div>{error?<p className="inline-error">{error}</p>:<p className="muted">正在加载 Instagram 配置…</p>}</div>
 return <div className="platform-config x-config">
  {error&&<p className="inline-error" role="alert">{error}</p>}{message&&<p className="platform-notice" role="status">{message}</p>}
  <section className="platform-section"><h3>登录与运行</h3>
   <p className="muted">{data?.sessionConfigured?`已保存登录态${data.sessionUsername ? ` · @${data.sessionUsername}` : ''}`:'尚未保存 Instagram 登录态。公开账号可尝试读取，Story 和私密账号需要登录且具备访问权限。'} 首次启用只建立基线，不补发历史帖子。</p>
   <PlatformField label="Instagram 登录账号"><input autoComplete="username" value={username} onChange={e=>setUsername(e.target.value)} placeholder="用户名、邮箱或带区号的手机号" /></PlatformField>
   <PlatformField label="Instagram 密码" description="仅用于登录请求，不保存密码。"><input type="password" autoComplete="current-password" value={password} onChange={e=>setPassword(e.target.value)} /></PlatformField>
   <div className="inline-actions"><button className="primary-button" disabled={!!busy||!username||!password} onClick={()=>void action('login',async()=>{try{await api('instagram/session',{method:'POST',body:JSON.stringify({username,password})});setData(await api<Result>('instagram/settings'));setMessage('登录成功，会话已保存')}finally{setPassword('')}})}>登录并保存会话</button></div>
   <PlatformField label="二次验证码（如需要）"><input inputMode="numeric" autoComplete="one-time-code" value={code} onChange={e=>setCode(e.target.value)} placeholder="登录提示需要二次验证时填写" /></PlatformField>
   <button className="secondary-button" disabled={!!busy||!/^\d{6,8}$/.test(code)} onClick={()=>void action('verify',async()=>{await api('instagram/session',{method:'POST',body:JSON.stringify({code})});setCode('');setData(await api<Result>('instagram/settings'));setMessage('二次验证成功，会话已保存')})}>提交验证码</button>
   <PlatformField label="Instagram Cookie" description="在自己的浏览器登录 Instagram 后，粘贴 Cookie 字符串或 Cookie 导出的 JSON。验证后保存；留空不会覆盖现有登录态。"><textarea aria-label="Instagram Cookie" value={cookies} onChange={e=>setCookies(e.target.value)} autoComplete="off" rows={3}/></PlatformField>
   <div className="inline-actions"><button className="secondary-button" disabled={!!busy||!cookies} onClick={()=>void action('import',async()=>{await api('instagram/session',{method:'POST',body:JSON.stringify({cookies})});setCookies('');setData(await api<Result>('instagram/settings'));setMessage('登录态验证通过并已保存')})}>验证并导入登录态</button><button className="secondary-button" disabled={!!busy||!data?.sessionConfigured} onClick={()=>void action('check',async()=>{await api('instagram/session');setMessage('登录态有效')})}>检查登录态</button><button className="secondary-button" disabled={!!busy||!data?.sessionConfigured} onClick={()=>void action('clear',async()=>{await api('instagram/session',{method:'DELETE'});setData(await api<Result>('instagram/settings'));setMessage('已清除登录态')})}>清除登录态</button></div>
   <PlatformToggle label="启用 Instagram 监控" description="监控订阅账号的新帖子，图片随文字发送，视频单独发送。" checked={settings.enabled} onChange={enabled=>setSettings({...settings,enabled})}/>
   <PlatformField label="检查间隔（秒）" description="60–3600 秒，建议 300 秒。"><input type="number" min="60" max="3600" value={settings.pollSeconds} onChange={e=>setSettings({...settings,pollSeconds:Number(e.target.value)})}/></PlatformField>
   <PlatformField label="网络代理（可选）" description="留空使用服务器网络。"><input value={settings.proxyURL||''} placeholder="http:// 或 socks5://" onChange={e=>setSettings({...settings,proxyURL:e.target.value})}/></PlatformField>
   <div className="inline-actions"><button className="primary-button" disabled={!!busy} onClick={()=>void action('save',()=>save(settings))}>保存运行设置</button></div>
   {data?.status.lastCheck&&<p className="muted">最近检查：{new Date(data.status.lastCheck).toLocaleString()} · {data.status.error||'正常'}</p>}
  </section>
  <section className="platform-section"><h3>搜索与订阅</h3><p className="muted">支持 @用户名或 Instagram 主页链接，精确查找账号。</p>
   <form className="platform-search" onSubmit={e=>{e.preventDefault();void action('search',async()=>{const r=await api<{users:User[]}>(`instagram/search?q=${encodeURIComponent(query)}`);setUsers(r.users);if(!r.users.length)setMessage('没有找到匹配账号')})}}><input aria-label="搜索 Instagram 账号" value={query} onChange={e=>setQuery(e.target.value)}/><button className="secondary-button" disabled={!!busy}><Search size={16}/>{busy==='search'?'搜索中…':'搜索'}</button></form>
   <div className="platform-results">{users.map(u=><button className={`platform-result${selected?.id===u.id?' selected':''}`} key={u.id} disabled={!!busy} onClick={()=>{setSelected(u);setEditing('');setGroup(defaultGroup);setDraft(defaults)}}>{u.avatar&&<img src={u.avatar} alt="" loading="lazy"/>}<span><strong>{u.name}</strong><small>@{u.username}{u.protected?' · 私密账号，读取取决于登录账号权限':''}</small></span></button>)}</div>
   {selected&&<div className="platform-editor"><h4>{editing?'编辑订阅':'添加订阅'} · {selected.name} (@{selected.username})</h4><PlatformField label="推送 QQ 群号"><input inputMode="numeric" value={group} onChange={e=>setGroup(e.target.value)}/></PlatformField><div className="platform-choices">{choices.map(([key,label])=><label key={key}><input type="checkbox" checked={draft[key]} onChange={e=>setDraft({...draft,[key]:e.target.checked})}/>{label}</label>)}</div><div className="inline-actions"><button className="primary-button" disabled={!!busy||!/^\d+$/.test(group)||Number(group)<=0||(!draft.posts&&!draft.reels&&!draft.stories)} onClick={()=>void action('subscribe',async()=>{const sub:Subscription={...draft,id:editing||crypto.randomUUID(),userId:selected.id,username:selected.username,name:selected.name,groupId:Number(group),enabled:true};await save({...settings,subscriptions:[...settings.subscriptions.filter(s=>s.id!==sub.id),sub]});reset()})}>{editing?'保存订阅':'添加订阅'}</button><button className="secondary-button" disabled={!!busy} onClick={reset}>取消</button><button className="secondary-button" disabled={!!busy} onClick={()=>void action('preview',async()=>{const r=await api<{events:Event[];message:string}>('instagram/preview',{method:'POST',body:JSON.stringify({username:selected.username})});setPreview(r.events);setMessage(r.message)})}>{busy==='preview'?'测试中…':'只读测试（不推送）'}</button></div></div>}
  </section>
  <section className="platform-section"><h3>已订阅账号</h3>{!settings.subscriptions.length&&<p className="muted">还没有订阅，请先搜索账号并设置推送群。</p>}{settings.subscriptions.map(s=><article className="douyin-card" key={s.id}><div className="douyin-card-main"><div className="douyin-card-heading"><strong>{s.name||s.username}</strong><span className="quiet-badge">{s.enabled?'已启用':'已暂停'}</span></div><p className="muted">@{s.username} · QQ 群 {s.groupId}</p><p className="muted">{choices.filter(([key])=>s[key]).map(([,label])=>label).join(' / ')}</p>{data?.status.targets?.[s.id]&&<p className="muted">{data.status.targets[s.id]}</p>}</div><div className="sub-item-actions"><button className="secondary-button" disabled={!!busy} onClick={()=>void action('toggle',()=>save({...settings,subscriptions:settings.subscriptions.map(v=>v.id===s.id?{...v,enabled:!v.enabled}:v)}))}>{s.enabled?'暂停':'启用'}</button><button className="icon-button" aria-label={`编辑 Instagram 订阅 ${s.username}`} disabled={!!busy} onClick={()=>edit(s)}><Pencil size={15}/></button><button className="icon-button danger" aria-label={`删除 Instagram 订阅 ${s.username}`} disabled={!!busy} onClick={()=>{if(window.confirm(`删除 @${s.username} 的订阅？`))void action('delete',()=>save({...settings,subscriptions:settings.subscriptions.filter(v=>v.id!==s.id)}))}}><Trash2 size={16}/></button></div></article>)}</section>
  {!!preview.length&&<section className="platform-section"><h3>只读预览</h3>{preview.map(e=><article className="platform-preview" key={e.id}><strong>{e.author.name}</strong><p>{e.body}</p><p className="muted">{e.media.map(m=>m.kind==='video'?'视频':(m.kind==='gif'||m.kind==='animated')?'动图':'图片').join(' / ')||'纯文本'}</p><a href={e.url} target="_blank" rel="noreferrer">查看原帖</a></article>)}</section>}
 </div>
}
