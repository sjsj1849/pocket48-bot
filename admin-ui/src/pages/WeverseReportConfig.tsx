import { useEffect, useState } from 'react'
import { api } from '../api'
import { PlatformField, PlatformToggle } from '../components/PlatformField'

type Settings = { enabled: boolean; monthly: boolean; firstHalf: boolean; annual: boolean; sendTime: string; communityId: number; communityName: string; enabledAt?: string }
type Status = { settings: Settings; emailTo: string; emailEnabled: boolean; memberCount: number; recordingStarted: string; state?: { lastSuccess?: string; error?: string }; backfill: { running: boolean; total: number; done: number; records: number; error?: string } }
type Report = { complete: boolean; unknownRootReplies: number; members: Array<{ id: string; name: string; posts: number; replies: number; imageMessages: number; images: number; videoMessages: number; lives: number; teammateReplies: number; postPhotos:number; postComments:number; postLikes:number; commentPosts:number; likePosts:number; fanReplies:number; memberReplies:number; selfReplies:number; unknownReplies:number; videos:number; moments:number }> }

export function WeverseReportConfig() {
  const [data, setData] = useState<Status>()
  const [settings, setSettings] = useState<Settings>()
  const [kind, setKind] = useState('monthly')
  const [year, setYear] = useState(new Date().getFullYear())
  const [month, setMonth] = useState(new Date().getMonth() + 1)
  const [report, setReport] = useState<Report>()
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  useEffect(() => {
    const c = new AbortController()
    api<Status>('weverse/reports', { signal: c.signal }).then(r => { setData(r); setSettings(r.settings) }).catch(e => { if (!c.signal.aborted) setError(e.message) })
    return () => c.abort()
  }, [])
  useEffect(() => {
    if (!data?.backfill.running) return
    const c = new AbortController()
    const t = setInterval(() => { api<Status>('weverse/reports', { signal: c.signal }).then(setData).catch(e => { if (!c.signal.aborted) setError(e.message) }) }, 5000)
    return () => { c.abort(); clearInterval(t) }
  }, [data?.backfill.running])
  useEffect(() => { setReport(undefined) }, [kind, year, month])
  const query = `kind=${kind}&year=${year}&month=${month}`
  async function action(name: string, work: () => Promise<void>) {
    setBusy(name); setError(''); setMessage('')
    try { await work() } catch (e) { setError(e instanceof Error ? e.message : '操作失败') } finally { setBusy('') }
  }
  return <section className="platform-section">
    <h3>成员活动月报 / 半年报 / 年报</h3>
    <p className="muted">持续记录成员发帖、回复、图片、视频和直播。回复按直接被回复者区分粉丝、队友和自己；另保留队友主帖下回复统计。累计评论、点赞采用最新采集值，Moment 历史可能缺失。半年报、年报包含逐月对照图。定期报告发送邮件，附件包含 Excel 明细表和 PNG 汇总图片。</p>
    {error && <p role="alert" className="inline-error">{error}</p>}
    {message && <p role="status" className="wv-notice">{message}</p>}
    {settings && <>
      <p className="muted">目标：{settings.communityName} · 已记录 {data?.memberCount || 0} 位成员 · 收件邮箱：{data?.emailTo || '未配置'}{!data?.emailEnabled && '（邮件功能未开启）'}</p>
      <PlatformToggle label="启用定期活动报告邮件" checked={settings.enabled} onChange={enabled => setSettings({ ...settings, enabled })} />
      <div>
        <PlatformToggle label="月报：次月 1 日" checked={settings.monthly} onChange={checked => setSettings({ ...settings, monthly: checked })} />
        <PlatformToggle label="上半年报：7 月 1 日（1–6 月）" checked={settings.firstHalf} onChange={checked => setSettings({ ...settings, firstHalf: checked })} />
        <PlatformToggle label="年报：次年 1 月 1 日" checked={settings.annual} onChange={checked => setSettings({ ...settings, annual: checked })} />
        <PlatformField label="报告发送时间（北京时间）"><input type="time" value={settings.sendTime} onChange={e => setSettings({ ...settings, sendTime: e.target.value })} /></PlatformField>
      </div>
      <button className="primary-button" disabled={!!busy} onClick={() => void action('save', async () => { const r = await api<Status>('weverse/reports', { method: 'PUT', body: JSON.stringify(settings) }); setData(r); setSettings(r.settings); setMessage('报告配置已保存，自动生效') })}>保存报告配置</button>
      <p className="muted">报表沿用微博日报的发件与收件配置。发送前自动回采当期可见内容；失败后重试，成功后不重复发送。首次启用不会自动补发早期报告。当前月份是截至已采集时刻的预览。</p>
      <div className="wv-fields">
        <label>报表类型<select disabled={!!busy} value={kind} onChange={e => setKind(e.target.value)}><option value="monthly">月报</option><option value="firstHalf">上半年报</option><option value="annual">年报</option></select></label>
        <label>报表年份<input type="number" disabled={!!busy} min="2025" max="2100" value={year} onChange={e => setYear(Number(e.target.value))} /></label>
        {kind === 'monthly' && <label>报表月份<input type="number" disabled={!!busy} min="1" max="12" value={month} onChange={e => setMonth(Number(e.target.value))} /></label>}
      </div>
      <div className="inline-actions">
        <button className="secondary-button" disabled={!!busy} onClick={() => void action('preview', async () => { setReport(await api<Report>(`weverse/reports/preview?${query}`)) })}>预览统计</button>
        <a className="secondary-button" href={`/api/weverse/reports/download?${query}`}>下载 Excel</a>
        <button className="secondary-button" disabled={!!busy || data?.backfill.running} onClick={() => void action('backfill', async () => { await api(`weverse/reports/backfill?${query}`, { method: 'POST' }); setData(await api<Status>('weverse/reports')); setMessage('历史回采已启动，原始记录会逐步补齐，不补发旧动态') })}>回采当期历史</button>
        <button className="secondary-button" disabled={!!busy || !data?.emailEnabled} onClick={() => void action('send', async () => { await api(`weverse/reports/send?${query}`, { method: 'POST' }); setMessage(`报表和附件已发送至 ${data?.emailTo}`) })}>{busy === 'send' ? '发送中…' : '发送至邮箱'}</button>
      </div>
      {data?.backfill.running && <p role="status" className="muted">历史回采：{data.backfill.done} / {data.backfill.total} 条主帖，已记录 {data.backfill.records} 条当期内容</p>}
      {data?.backfill.error && <p className="inline-error">{data.backfill.error}</p>}
      {data?.state?.error && <p className="inline-error">定期报告：{data.state.error}</p>}
      {data?.state?.lastSuccess && <p className="muted">最近定期报告发送：{new Date(data.state.lastSuccess).toLocaleString()}</p>}
      {report && <>
        {!report.complete && <p className="wv-notice">本期历史尚未完整回采，以下数字仅代表已采集记录，不代表成员全部发布量。</p>}
        {!!report.unknownRootReplies && <p className="muted">{report.unknownRootReplies} 条回复暂缺主帖作者，未计入队友帖回复。</p>}
        <div style={{ overflowX: 'auto' }}><table className="data-table"><thead><tr><th>成员</th><th>发帖</th><th>回复</th><th>帖子照片</th><th>累计评论</th><th>累计点赞</th><th>粉丝 / 队友 / 自己 / 未知</th><th>视频</th><th>Moment*</th><th>直播</th><th>队友帖回复</th></tr></thead><tbody>{report.members.map(m => <tr key={m.id}><td>{m.name}</td><td>{m.posts}</td><td>{m.replies}</td><td>{m.postPhotos}</td><td>{m.posts && !m.commentPosts ? "缺失" : `${m.postComments}${m.commentPosts<m.posts ? "（部分）" : ""}`}</td><td>{m.posts && !m.likePosts ? "缺失" : `${m.postLikes}${m.likePosts<m.posts ? "（部分）" : ""}`}</td><td>{m.fanReplies} / {m.memberReplies} / {m.selfReplies} / {m.unknownReplies}</td><td>{m.videos}</td><td>{m.moments}*</td><td>{m.lives}</td><td>{m.teammateReplies}</td></tr>)}</tbody></table></div>
      </>}
    </>}
  </section>
}
