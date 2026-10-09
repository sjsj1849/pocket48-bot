import { useEffect, useMemo, useState } from 'react'
import { api } from '../api'

export type Target = { id: string; platform: string; kind: string; address: string; name: string }

function targetLabel(target: Target) {
  const kind = target.kind === 'private' ? '私聊' : '群聊'
  const platform = target.platform === 'qq' ? 'QQ' : target.platform === 'feishu' ? '飞书' : target.platform
  return `${platform}${kind} · ${target.name || target.address}`
}

// useTargets loads the delivery address book once and exposes helpers to render
// selected target ids as readable labels. Shared by TargetSelector and the
// subscription list so both show the same names.
export function useTargets() {
  const [targets, setTargets] = useState<Target[]>([])

  useEffect(() => {
    const controller = new AbortController()
    api<{ targets: Target[] }>('outbound/targets', { signal: controller.signal })
      .then(r => setTargets(r.targets ?? []))
      .catch(() => { /* selector surfaces its own error state */ })
    return () => controller.abort()
  }, [])

  const targetsById = useMemo(() => {
    const map = new Map<string, Target>()
    for (const t of targets) map.set(t.id, t)
    return map
  }, [targets])

  const formatTargetIds = useMemo(() => {
    return (ids?: string[]) => {
      if (!ids || !ids.length) return ''
      const labels = ids
        .map(id => targetsById.get(id))
        .filter((t): t is Target => Boolean(t))
        .map(t => targetLabel(t))
      return labels.join(' / ')
    }
  }, [targetsById])

  return { targets, targetsById, formatTargetIds }
}

// TargetSelector is a multi-select of the available delivery targets (QQ
// groups, QQ private, Feishu groups, Feishu private). It loads the target list
// once and reports the selected target ids upward.
export function TargetSelector({ value, onChange }: { value: string[]; onChange: (ids: string[]) => void }) {
  const { targets } = useTargets()
  const [error, setError] = useState('')

  useEffect(() => {
    const controller = new AbortController()
    api<{ targets: Target[] }>('outbound/targets', { signal: controller.signal })
      .then(() => setError(''))
      .catch(e => { if (!controller.signal.aborted) setError(e instanceof Error ? e.message : '加载目标失败') })
    return () => controller.abort()
  }, [])

  function toggle(id: string) {
    const has = value.includes(id)
    onChange(has ? value.filter(v => v !== id) : [...value, id])
  }

  if (error) return <p className="muted">{error}</p>
  if (!targets.length) return <p className="muted">加载投递目标中…</p>

  return <div className="target-selector">
    {targets.map(t => (
      <label key={t.id}>
        <input type="checkbox" checked={value.includes(t.id)} onChange={() => toggle(t.id)} />
        <span>{targetLabel(t)}</span>
      </label>
    ))}
    {value.length === 0 && <small className="muted">未勾选：该订阅将不会投递消息。</small>}
  </div>
}
