import { useEffect, useState } from 'react'
import { api } from '../api'
import { PlatformField, PlatformToggle } from '../components/PlatformField'

type Subscription = { id: string; artistId: string; artistName: string; groupId: number; enabled: boolean; releases: boolean; magazines: boolean; photos: boolean; artistNotes: boolean; musicWave: boolean; atAll: boolean; atAllAuthorNames?: string[] }
type Settings = { enabled: boolean; pollSeconds: number; musicWavePollSeconds: number; proxyURL?: string; subscriptions: Subscription[] }
type Artist = { id: string; name: string; avatar?: string; url: string }
type Event = { id: string; kind: string; title: string; body: string; url: string; time: number; images: string[]; author?: string }
type Result = { settings: Settings; status: { lastCheck?: string; error?: string }; musicWaveStatus?: { lastCheck?: string; error?: string } }

const defaults = { releases: true, magazines: true, photos: true, artistNotes: true, musicWave: true, atAll: true, atAllAuthorNames: ['STELLA'] as string[] }
const kinds = [['musicWave', 'Music Wave 实时聊天'], ['artistNotes', 'Artist Note（专辑留言）'], ['releases', '新专辑 / MV'], ['magazines', 'Melon 杂志 / 活动'], ['photos', '照片 / Story'], ['atAll', '@全体成员']] as const

export function MelonConfig({ defaultGroup }: { defaultGroup: string }) {
  const [data, setData] = useState<Result>()
  const [settings, setSettings] = useState<Settings>()
  const [query, setQuery] = useState('Hearts2Hearts')
  const [artists, setArtists] = useState<Artist[]>([])
  const [artist, setArtist] = useState<Artist>()
  const [group, setGroup] = useState(defaultGroup)
  const [draft, setDraft] = useState(defaults)
  const [editing, setEditing] = useState('')
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  const [preview, setPreview] = useState<Event[]>([])

  useEffect(() => {
    const controller = new AbortController()
    api<Result>('melon/settings', { signal: controller.signal }).then(r => { setData(r); setSettings(r.settings) }).catch(e => { if (!controller.signal.aborted) setError(e.message) })
    return () => controller.abort()
  }, [])

  async function action(key: string, work: () => Promise<void>) {
    setBusy(key); setError(''); setMessage('')
    try { await work() } catch (e) { setError(e instanceof Error ? e.message : '操作失败') } finally { setBusy('') }
  }
  async function save(next: Settings) {
    const result = await api<{ settings: Settings }>('melon/settings', { method: 'PUT', body: JSON.stringify(next) })
    setSettings(result.settings); setMessage('已保存，下一次检查自动生效；首次扫描只建立基线')
  }
  async function search() {
    await action('search', async () => {
      const result = await api<{ artists: Artist[] }>(`melon/search?q=${encodeURIComponent(query)}`)
      setArtists(result.artists); if (!result.artists.length) setMessage('没有找到匹配艺人')
    })
  }
  function edit(sub: Subscription) {
    setArtist({ id: sub.artistId, name: sub.artistName, url: `https://www.melon.com/artist/detail.htm?artistId=${sub.artistId}` })
    setGroup(String(sub.groupId)); setDraft({ releases: sub.releases, magazines: sub.magazines, photos: sub.photos, artistNotes: sub.artistNotes, musicWave: sub.musicWave, atAll: sub.atAll, atAllAuthorNames: sub.atAllAuthorNames || [] }); setEditing(sub.id)
  }

  if (!settings) return <div className="platform-config melon-config">{error ? <p className="inline-error">{error}</p> : <p>正在加载 Melon 配置…</p>}</div>
  return <div className="platform-config melon-config">
    <p className="muted">监控 Music Wave 成员实时聊天、Artist Note、专辑、MV、活动杂志和照片。公开读取无需登录；首次启用不补发历史内容。</p>
    {error && <div role="alert" className="inline-error">{error}</div>}
    {message && <p role="status" className="wv-notice">{message}</p>}
    <section className="platform-section">
      <h3>运行设置</h3>
      <PlatformToggle label="启用 Melon 监控" description="按已启用订阅转发艺人频道更新。" checked={settings.enabled} onChange={enabled => setSettings({ ...settings, enabled })} />
      <PlatformField label="检查间隔（秒）" description="60–3600 秒，建议 120 秒。"><input type="number" min="60" max="3600" value={settings.pollSeconds} onChange={e => setSettings({ ...settings, pollSeconds: Number(e.target.value) })} /></PlatformField>
      <PlatformField label="Music Wave 检查间隔（秒）" description="5–60 秒，建议 5 秒；自动绕过接口缓存，每条 QQ 消息最多合并 8 条发言。"><input type="number" min="5" max="60" value={settings.musicWavePollSeconds} onChange={e => setSettings({ ...settings, musicWavePollSeconds: Number(e.target.value) })} /></PlatformField>
      <PlatformField label="网络代理（可选）" description="留空使用服务器网络。"><input value={settings.proxyURL || ''} placeholder="留空使用服务器网络" onChange={e => setSettings({ ...settings, proxyURL: e.target.value })} /></PlatformField>
      <button className="primary-button" disabled={!!busy} onClick={() => void action('save', () => save(settings))}>保存运行设置</button>
      {data?.status.lastCheck && <p className="muted">最近检查：{new Date(data.status.lastCheck).toLocaleString()}　{data.status.error || '正常'}</p>}
      {data?.musicWaveStatus?.lastCheck && <p className="muted">Music Wave：{new Date(data.musicWaveStatus.lastCheck).toLocaleString()}　{data.musicWaveStatus.error || '正常'}</p>}
    </section>
    <section className="platform-section">
      <h3>添加艺人订阅</h3>
      <p className="muted">支持 Melon artistId、艺人页完整链接；Hearts2Hearts / H2H 会自动识别为官方艺人页。</p>
      <form className="wv-actions" onSubmit={e => { e.preventDefault(); void search() }}>
        <input aria-label="搜索 Melon 艺人" value={query} onChange={e => setQuery(e.target.value)} />
        <button className="secondary-button" type="submit" disabled={!!busy}>{busy === 'search' ? '查询中…' : '查询'}</button>
      </form>
      <div className="wv-results">{artists.map(item => <button key={item.id} className={`wv-result ${artist?.id === item.id ? 'selected' : ''}`} onClick={() => { setArtist(item); setEditing(''); setDraft(defaults) }}><strong>{item.name}</strong><small>artistId {item.id}</small></button>)}</div>
      {artist && <div className="wv-editor">
        <h4>{editing ? '编辑订阅' : '添加订阅'} · {artist.name}</h4>
        <label>推送 QQ 群号<input aria-label="推送 QQ 群号" inputMode="numeric" value={group} onChange={e => setGroup(e.target.value.replace(/\D/g, ''))} /></label>
        <div className="wv-members">{kinds.map(([key, label]) => <label key={key} className="wv-check"><input type="checkbox" checked={draft[key]} onChange={e => setDraft({ ...draft, [key]: e.target.checked })} />{label}</label>)}</div>
        {draft.atAll && <label>需要 @全体成员的成员<input value={draft.atAllAuthorNames.join(', ')} placeholder="例如 STELLA；留空表示所有成员" onChange={e => setDraft({ ...draft, atAllAuthorNames: e.target.value.split(/[,，]/).map(v => v.trim()).filter(Boolean) })} /></label>}
        <div className="wv-actions">
          <button className="primary-button" disabled={!!busy || !/^\d+$/.test(group) || (!draft.releases && !draft.magazines && !draft.photos && !draft.artistNotes && !draft.musicWave)} onClick={() => void action('subscribe', async () => {
            const item: Subscription = { ...draft, id: editing || crypto.randomUUID(), artistId: artist.id, artistName: artist.name, groupId: Number(group), enabled: true }
            await save({ ...settings, subscriptions: [...settings.subscriptions.filter(v => v.id !== item.id), item] }); setArtist(undefined); setEditing('')
          })}>{editing ? '保存订阅' : '添加订阅'}</button>
          <button className="secondary-button" disabled={!!busy} onClick={() => void action('preview', async () => { const result = await api<{ events: Event[]; message: string }>('melon/preview', { method: 'POST', body: JSON.stringify({ artistId: artist.id }) }); setPreview(result.events); setMessage(result.message) })}>{busy === 'preview' ? '测试中…' : '只读测试'}</button>
        </div>
      </div>}
    </section>
    <section className="platform-section"><h3>已订阅</h3>
      {!settings.subscriptions.length && <p className="muted">还没有订阅。查询 Hearts2Hearts 后选择目标群即可添加。</p>}
      {settings.subscriptions.map(sub => <div className="wv-subscription douyin-card" key={sub.id}><div><strong>{sub.artistName}</strong><p>QQ群 {sub.groupId} · {kinds.filter(([key]) => sub[key]).map(([, label]) => label).join(' / ')}{sub.atAll && sub.atAllAuthorNames?.length ? `（仅 ${sub.atAllAuthorNames.join('、')}）` : ''}</p></div><div className="wv-actions">
        <button className="secondary-button" disabled={!!busy} onClick={() => void action('toggle', () => save({ ...settings, subscriptions: settings.subscriptions.map(v => v.id === sub.id ? { ...v, enabled: !v.enabled } : v) }))}>{sub.enabled ? '暂停' : '启用'}</button>
        <button className="secondary-button" disabled={!!busy} onClick={() => edit(sub)}>编辑</button>
        <button className="secondary-button" disabled={!!busy} onClick={() => { if (window.confirm(`删除 ${sub.artistName} 的这条订阅？`)) void action('delete', () => save({ ...settings, subscriptions: settings.subscriptions.filter(v => v.id !== sub.id) })) }}>删除</button>
      </div></div>)}
    </section>
    {preview.length > 0 && <section className="platform-section"><h3>只读预览</h3>{preview.slice(0, 30).map(event => <article className="wv-preview" key={event.id}><strong>【{artist?.name || 'Hearts2Hearts'}|{event.kind === 'music_wave' ? 'Melon Music Wave' : 'Melon'}】</strong><p>{event.author && `${event.author}：`}{event.body}</p>{event.images[0] && <img src={event.images[0]} alt="" loading="lazy" />}<a href={event.url} target="_blank" rel="noreferrer">查看原文</a><time>{new Date(event.time).toLocaleString()}</time></article>)}</section>}
  </div>
}
