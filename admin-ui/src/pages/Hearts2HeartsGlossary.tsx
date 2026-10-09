import { useEffect, useState } from 'react'
import { api } from '../api'
import { PlatformField } from '../components/PlatformField'
import { useDraftedSettings } from '../drafts'

type Term = { source: string; target: string; note?: string }
type Glossary = { context: string; terms: Term[] }

function formatTerms(terms: Term[]) {
  return terms.map(term => `${term.source} => ${term.target}${term.note ? ` | ${term.note}` : ''}`).join('\n')
}

function parseTerms(value: string): Term[] {
  return value.split('\n').map(line => line.trim()).filter(Boolean).map((line, index) => {
    const arrow = line.indexOf('=>')
    if (arrow < 1) throw new Error(`术语表第 ${index + 1} 行缺少 =>`)
    const source = line.slice(0, arrow).trim()
    const remainder = line.slice(arrow + 2).trim()
    const separator = remainder.indexOf('|')
    const target = (separator >= 0 ? remainder.slice(0, separator) : remainder).trim()
    const note = separator >= 0 ? remainder.slice(separator + 1).trim() : ''
    if (!source || !target) throw new Error(`术语表第 ${index + 1} 行的原词或译法为空`)
    return { source, target, ...(note ? { note } : {}) }
  })
}

export function Hearts2HeartsGlossary() {
  const [context, setContext] = useState('')
  const [terms, setTerms] = useState('')
  const [baseline, setBaseline] = useState<{ context: string; terms: string }>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')

  useEffect(() => {
    const controller = new AbortController()
    api<Glossary>('hearts2hearts/glossary', { signal: controller.signal }).then(value => {
      setContext(value.context); setTerms(formatTerms(value.terms))
      setBaseline({ context: value.context, terms: formatTerms(value.terms) })
    }).catch(e => { if (!controller.signal.aborted) setError(e instanceof Error ? e.message : '术语表加载失败') })
    return () => controller.abort()
  }, [])

  // 术语表跟随右上角「保存更改」一起提交
  useDraftedSettings('h2h-glossary', '共享术语表',
    baseline ? { context, terms } : undefined,
    baseline,
    async (next: { context: string; terms: string }) => {
      setBusy(true); setError(''); setMessage('')
      try {
        const value = await api<Glossary>('hearts2hearts/glossary', { method: 'PUT', body: JSON.stringify({ context: next.context, terms: parseTerms(next.terms) }) })
        const normalized = { context: value.context, terms: formatTerms(value.terms) }
        setContext(normalized.context); setTerms(normalized.terms); setBaseline(normalized)
        setMessage('共享术语表已保存，Melon 和 Weverse 下一次翻译自动生效')
        return normalized
      } finally { setBusy(false) }
    })

  return <section className="platform-section">
    <h3>Hearts2Hearts 共享术语表</h3>
    <p className="muted">Melon 上下文翻译与 Weverse AI 整理共用这一份配置。核心词会在 Melon 翻译前锁定，避免模型把团内外号和粉丝称呼自行改写。修改后点右上角「保存更改」。</p>
    {error && <p role="alert" className="inline-error">{error}</p>}
    {message && <p role="status" className="wv-notice">{message}</p>}
    <PlatformField label="固定语境" description="只保留长期有效的团体背景，不要粘贴临时聊天内容。"><textarea rows={3} value={context} onChange={e => setContext(e.target.value)} /></PlatformField>
    <PlatformField label="固定术语" description="每行：韩文原词 => 固定译法 | 可选说明。长词会优先匹配。"><textarea rows={12} value={terms} onChange={e => setTerms(e.target.value)} spellCheck={false} /></PlatformField>
  </section>
}
