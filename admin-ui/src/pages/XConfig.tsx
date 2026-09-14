import { useEffect, useState } from 'react'
import { Pencil, Search, Trash2 } from 'lucide-react'
import { api } from '../api'
import { PlatformField, PlatformToggle } from '../components/PlatformField'

type User = { id: string; username: string; name: string; avatar: string; protected: boolean }
type Subscription = { id: string; userId: string; username: string; name: string; groupId: number; enabled: boolean; posts: boolean; replies: boolean; reposts: boolean; atAll: boolean }
type Settings = { enabled: boolean; pollSeconds: number; proxyURL: string; subscriptions: Subscription[] }
type Result = { settings: Settings; sessionConfigured: boolean; status: { lastCheck?: string; lastSuccess?: string; error?: string; targets?: Record<string,string> } }
type Event = { id: string; body: string; url: string; time: number; author: User; media: Array<{kind:string;cover?:string;url?:string}> }
const choices = [['posts','帖子 / 引用'],['replies','回复'],['reposts','转发'],['atAll','@全体成员']] as const
const defaults = {posts:true,replies:false,reposts:false,atAll:false}

export function XConfig({ defaultGroup }: { defaultGroup: string }) {
 const [data,setData]=useState<Result>()
 const [settings,setSettings]=useState<Settings>()
 const [query,setQuery]=useState('https://x.com/nekomo_st')
 const [users,setUsers]=useState<User[]>([])
 const [selected,setSelected]=useState<User>()
 const [group,setGroup]=useState(defaultGroup)
 const [draft,setDraft]=useState(defaults)
 const [editing,setEditing]=useState('')
 const [busy,setBusy]=useState('')
 const [error,setError]=useState('')
 const [message,setMessage]=useState('')
 const [preview,setPreview]=useState<Event[]>([])
 useEffect(()=>{const c=new AbortController();api<Result>('x/settings',{signal:c.signal}).then(r=>{setData(r);setSettings(r.settings)}).catch(e=>{if(!c.signal.aborted)setError(e.message)});return()=>c.abort()},[])
 async function action(name:string,work:()=>Promise<void>){setBusy(name);setError('');setMessage('');try{await work()}catch(e){setError(e instanceof Error?e.message:'操作失败')}finally{setBusy('')}}
 async function save(next:Settings){const r=await api<{settings:Settings}>('x/settings',{method:'PUT',body:JSON.stringify(next)});setSettings(r.settings);setMessage('已保存，下一次检查生效，无需重启')}
 function edit(s:Subscription){setSelected({id:s.userId,username:s.username,name:s.name,avatar:'',protected:false});setGroup(String(s.groupId));setDraft({posts:s.posts,replies:s.replies,reposts:s.reposts,atAll:s.atAll});setEditing(s.id)}
 function reset(){setSelected(undefined);setEditing('');setDraft(defaults);setGroup(defaultGroup)}
 if(!settings)return <div>{error?<p className="inline-error">{error}</p>:<p className="muted">正在加载 X 配置…</p>}</div>
 return <div className="platform-config x-config">
  {error&&<p className="inline-error" role="alert">{error}</p>}{message&&<p className="platform-notice" role="status">{message}</p>}
  <section className="platform-section"><h3>登录与运行</h3>
   <p className="muted">{data?.sessionConfigured?'已保存登录态，浏览器 Cookie 自动同步至采集程序。':'尚未保存 X 登录态。'} 首次启用只建立基线，不补发历史帖子。</p>
   <div className="inline-actions"><button className="secondary-button" disabled={!!busy} onClick={()=>window.dispatchEvent(new CustomEvent('pocket48-navigate',{detail:'browser'}))}>打开登录浏览器</button><button className="secondary-button" disabled={!!busy} onClick={()=>void action('sync',async()=>{await api('browser/x',{method:'POST',body:JSON.stringify({action:'sync'})});setData(await api<Result>('x/settings'));setMessage('浏览器登录态已同步')})}>同步登录态</button></div>
   <PlatformToggle label="启用 X 监控" description="监控订阅账号的新帖子，图片随文字发送，视频单独发送。" checked={settings.enabled} onChange={enabled=>setSettings({...settings,enabled})}/>
   <PlatformField label="检查间隔（秒）" description="30–3600 秒，建议 120 秒。"><input type="number" min="30" max="3600" value={settings.pollSeconds} onChange={e=>setSettings({...settings,pollSeconds:Number(e.target.value)})}/></PlatformField>
   <PlatformField label="网络代理（可选）" description="留空使用服务器网络。"><input value={settings.proxyURL||''} placeholder="http:// 或 socks5://" onChange={e=>setSettings({...settings,proxyURL:e.target.value})}/></PlatformField>
   <div className="inline-actions"><button className="primary-button" disabled={!!busy} onClick={()=>void action('save',()=>save(settings))}>保存运行设置</button></div>
   {data?.status.lastCheck&&<p className="muted">最近检查：{new Date(data.status.lastCheck).toLocaleString()} · {data.status.error||'正常'}</p>}
  </section>
  <section className="platform-section"><h3>搜索与订阅</h3><p className="muted">支持 @用户名、X 主页链接或昵称。用户名和链接优先精确查找，昵称可返回多个账号。</p>
   <form className="platform-search" onSubmit={e=>{e.preventDefault();void action('search',async()=>{const r=await api<{users:User[]}>(`x/search?q=${encodeURIComponent(query)}`);setUsers(r.users);if(!r.users.length)setMessage('没有找到匹配账号')})}}><input aria-label="搜索 X 账号" value={query} onChange={e=>setQuery(e.target.value)}/><button className="secondary-button" disabled={!!busy}><Search size={16}/>{busy==='search'?'搜索中…':'搜索'}</button></form>
   <div className="platform-results">{users.map(u=><button className={`platform-result${selected?.id===u.id?' selected':''}`} key={u.id} disabled={!!busy} onClick={()=>{setSelected(u);setEditing('');setGroup(defaultGroup);setDraft(defaults)}}>{u.avatar&&<img src={u.avatar} alt="" loading="lazy"/>}<span><strong>{u.name}</strong><small>@{u.username}{u.protected?' · 私密账号，读取取决于登录账号权限':''}</small></span></button>)}</div>
   {selected&&<div className="platform-editor"><h4>{editing?'编辑订阅':'添加订阅'} · {selected.name} (@{selected.username})</h4><PlatformField label="推送 QQ 群号"><input inputMode="numeric" value={group} onChange={e=>setGroup(e.target.value)}/></PlatformField><div className="platform-choices">{choices.map(([key,label])=><label key={key}><input type="checkbox" checked={draft[key]} onChange={e=>setDraft({...draft,[key]:e.target.checked})}/>{label}</label>)}</div><div className="inline-actions"><button className="primary-button" disabled={!!busy||!/^\d+$/.test(group)||Number(group)<=0||(!draft.posts&&!draft.replies&&!draft.reposts)} onClick={()=>void action('subscribe',async()=>{const sub:Subscription={...draft,id:editing||crypto.randomUUID(),userId:selected.id,username:selected.username,name:selected.name,groupId:Number(group),enabled:true};await save({...settings,subscriptions:[...settings.subscriptions.filter(s=>s.id!==sub.id),sub]});reset()})}>{editing?'保存订阅':'添加订阅'}</button><button className="secondary-button" disabled={!!busy} onClick={reset}>取消</button><button className="secondary-button" disabled={!!busy} onClick={()=>void action('preview',async()=>{const r=await api<{events:Event[];message:string}>('x/preview',{method:'POST',body:JSON.stringify({username:selected.username})});setPreview(r.events);setMessage(r.message)})}>{busy==='preview'?'测试中…':'只读测试（不推送）'}</button></div></div>}
  </section>
  <section className="platform-section"><h3>已订阅账号</h3>{!settings.subscriptions.length&&<p className="muted">还没有订阅，请先搜索账号并设置推送群。</p>}{settings.subscriptions.map(s=><article className="douyin-card" key={s.id}><div className="douyin-card-main"><div className="douyin-card-heading"><strong>{s.name||s.username}</strong><span className="quiet-badge">{s.enabled?'已启用':'已暂停'}</span></div><p className="muted">@{s.username} · QQ 群 {s.groupId}</p><p className="muted">{choices.filter(([key])=>s[key]).map(([,label])=>label).join(' / ')}</p>{data?.status.targets?.[s.id]&&<p className="muted">{data.status.targets[s.id]}</p>}</div><div className="sub-item-actions"><button className="secondary-button" disabled={!!busy} onClick={()=>void action('toggle',()=>save({...settings,subscriptions:settings.subscriptions.map(v=>v.id===s.id?{...v,enabled:!v.enabled}:v)}))}>{s.enabled?'暂停':'启用'}</button><button className="icon-button" aria-label={`编辑 X 订阅 ${s.username}`} disabled={!!busy} onClick={()=>edit(s)}><Pencil size={15}/></button><button className="icon-button danger" aria-label={`删除 X 订阅 ${s.username}`} disabled={!!busy} onClick={()=>{if(window.confirm(`删除 @${s.username} 的订阅？`))void action('delete',()=>save({...settings,subscriptions:settings.subscriptions.filter(v=>v.id!==s.id)}))}}><Trash2 size={16}/></button></div></article>)}</section>
  {!!preview.length&&<section className="platform-section"><h3>只读预览</h3>{preview.map(e=><article className="platform-preview" key={e.id}><strong>{e.author.name}</strong><p>{e.body}</p><p className="muted">{e.media.map(m=>m.kind==='video'?'视频':(m.kind==='gif'||m.kind==='animated')?'动图':'图片').join(' / ')||'纯文本'}</p><a href={e.url} target="_blank" rel="noreferrer">查看原帖</a></article>)}</section>}
 </div>
}
