import { useEffect, useState } from 'react'
import { api } from '../api'

type Settings = { enabled: boolean; baseUrl: string; model: string; idleSeconds: number; requestTimeoutSeconds: number }
type Result = { settings: Settings; keyConfigured: boolean; active: number; pending: number; lastSuccess?: string; error?: string }

export function WeverseAIConfig() {
  const [data, setData] = useState<Result>()
  const [settings, setSettings] = useState<Settings>()
  const [key, setKey] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  useEffect(() => {
    const controller = new AbortController()
    api<Result>('weverse/ai', { signal: controller.signal }).then(r => { setData(r); setSettings(r.settings) }).catch(e => { if (!controller.signal.aborted) setError(e.message) })
    return () => controller.abort()
  }, [])
  async function save() {
    setBusy(true); setError(''); setMessage('')
    try {
      const r = await api<Result>('weverse/ai', { method: 'PUT', body: JSON.stringify({ ...settings, apiKey: key }) })
      setData(r); setSettings(r.settings); setKey(''); setMessage('AI 配置已保存，自动生效')
    } catch (e) { setError(e instanceof Error ? e.message : '保存失败') } finally { setBusy(false) }
  }
  return <section className="wv-card">
    <h3>AI 聊天整理</h3>
    <p className="muted">实时消息继续使用 Weverse 翻译。收集新回复和被回复的原文，连续一段时间没有新回复后，补发完整中文翻译，下方附对话总结和必要的梗解释。成员与粉丝的对话按成员分别整理；同一帖子里的成员间互动合并整理。间隔按消息判断，不代表检测到了成员下线；不补扫历史消息，AI 不分析图片内容。</p>
    {error && <p role="alert" className="inline-error">{error}</p>}
    {message && <p role="status" className="wv-notice">{message}</p>}
    {settings && <>
      <label className="wv-check"><input type="checkbox" checked={settings.enabled} onChange={e => setSettings({ ...settings, enabled: e.target.checked })} />启用 AI 聊天整理</label>
      <div className="wv-fields">
        <label>AI 接口地址<input type="url" value={settings.baseUrl} onChange={e => setSettings({ ...settings, baseUrl: e.target.value })} placeholder="https://example.com/v1" /></label>
        <label>模型<input value={settings.model} onChange={e => setSettings({ ...settings, model: e.target.value })} /></label>
        <label>无新回复间隔（秒）<input type="number" min="60" max="3600" value={settings.idleSeconds} onChange={e => setSettings({ ...settings, idleSeconds: Number(e.target.value) })} /></label>
        <label>AI 请求等待上限（秒）<input type="number" min="30" max="600" value={settings.requestTimeoutSeconds} onChange={e => setSettings({ ...settings, requestTimeoutSeconds: Number(e.target.value) })} /></label>
        <label>API Key<input type="password" autoComplete="new-password" value={key} onChange={e => setKey(e.target.value)} placeholder={data?.keyConfigured ? '已保存，留空保留现有 Key' : '填写 API Key'} /></label>
      </div>
      <p className="muted">聊天原文将发送至上方 AI 接口。整理通知沿用订阅的群和 @全体规则；AI 请求在后台串行执行，失败后逐步延长重试间隔，不影响实时转发。</p>
      <button className="primary-button" disabled={busy} onClick={() => void save()}>{busy ? '保存中…' : '保存 AI 配置'}</button>
      <p className="muted">收集中：{data?.active || 0} 段　待整理：{data?.pending || 0} 段{data?.lastSuccess && `　最近完成：${new Date(data.lastSuccess).toLocaleString()}`}</p>
      {data?.error && <p className="inline-error">{data.error}</p>}
    </>}
  </section>
}
