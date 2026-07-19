import { useCallback, useEffect, useMemo, useState } from 'react'
import { Check, Eye, EyeOff, Plus, Save, Trash2 } from 'lucide-react'
import { api } from '../api'
import type { ConfigField } from '../types'
import { ErrorState } from '../App'

type ConfigResponse = { groups: Record<string, ConfigField[]>; groupOrder: string[] }
type SubItem = {
  groupId: number
  userId?: string
  secUserId?: string
  uid?: string
  roomId?: number
  profileUrl?: string
  name?: string
  atAll?: boolean
  lastNoteId?: string
  liveActive?: boolean
  liveId?: string
}
type SubResponse = { subscriptions: SubItem[] }

const groupBlurb: Record<string, string> = {
  Bot: 'QQ 连接、媒体发送与邮件告警（跨平台通用）',
  口袋48: '账号、实时消息与房间订阅',
  微博: 'Cookie/浏览器登录、超话与 UID 订阅',
  抖音: '作品/直播监控、IM 与创作者订阅',
  小红书: '帖子轮询与创作者订阅（xhslink / 主页链接）',
}

function Field({ field, value, onChange }: { field: ConfigField; value: unknown; onChange: (value: unknown) => void }) {
  const [reveal, setReveal] = useState(false)
  if (field.kind === 'boolean') {
    return (
      <label className="config-row toggle-row">
        <span>
          <strong>{field.label}</strong>
          <small>{field.description}</small>
        </span>
        <input type="checkbox" checked={Boolean(value)} onChange={(event) => onChange(event.target.checked)} />
        <i />
      </label>
    )
  }
  if (field.kind === 'stringList') {
    return (
      <label className="config-row">
        <span>
          <strong>{field.label}</strong>
          <small>{field.description}</small>
        </span>
        <textarea rows={3} value={Array.isArray(value) ? value.join('\n') : ''} onChange={(event) => onChange(event.target.value.split('\n'))} />
      </label>
    )
  }
  // MEDIA_DELIVERY as select-like free text is fine; placeholder guides values
  const placeholder =
    field.key === 'MEDIA_DELIVERY'
      ? 'local 或 remote'
      : field.kind === 'secret' && field.configured
        ? '已配置，留空保持不变'
        : ''
  return (
    <label className="config-row">
      <span>
        <strong>{field.label}</strong>
        <small>{field.description}</small>
      </span>
      <div className="input-wrap">
        <input
          type={field.kind === 'secret' && !reveal ? 'password' : field.kind === 'integer' ? 'number' : 'text'}
          min={field.kind === 'integer' ? 0 : undefined}
          placeholder={placeholder}
          value={typeof value === 'string' || typeof value === 'number' ? value : ''}
          onChange={(event) => onChange(field.kind === 'integer' ? Number(event.target.value) : event.target.value)}
        />
        {field.kind === 'secret' ? (
          <button type="button" onClick={() => setReveal((shown) => !shown)} aria-label={reveal ? '隐藏' : '显示'}>
            {reveal ? <EyeOff size={17} /> : <Eye size={17} />}
          </button>
        ) : null}
      </div>
    </label>
  )
}

type ManagerProps = {
  title: string
  hint: string
  items: SubItem[]
  saving: boolean
  onAdd: (groupId: string, target: string, atAll: boolean) => Promise<void>
  onRemove: (item: SubItem) => Promise<void>
  targetPlaceholder: string
  showAtAll?: boolean
  roomMode?: boolean
  labelOf: (item: SubItem) => string
  metaOf: (item: SubItem) => string
}

function SubscriptionManager({ title, hint, items, saving, onAdd, onRemove, targetPlaceholder, showAtAll = true, roomMode, labelOf, metaOf }: ManagerProps) {
  const [groupID, setGroupID] = useState('')
  const [target, setTarget] = useState('')
  const [atAll, setAtAll] = useState(false)
  return (
    <div className="sub-manager">
      <div className="sub-manager-title">
        <strong>{title}</strong>
        <p>{hint}</p>
      </div>
      <div className="sub-add">
        <input type="number" min="1" placeholder="目标 QQ 群号" value={groupID} onChange={(e) => setGroupID(e.target.value)} />
        <input type={roomMode ? 'number' : 'text'} min={roomMode ? 1 : undefined} placeholder={targetPlaceholder} value={target} onChange={(e) => setTarget(e.target.value)} />
        {showAtAll ? (
          <label>
            <input type="checkbox" checked={atAll} onChange={(e) => setAtAll(e.target.checked)} /> @全体
          </label>
        ) : (
          <span />
        )}
        <button
          className="secondary-button"
          disabled={saving || !groupID || !target.trim()}
          onClick={() => void onAdd(groupID, target, atAll).then(() => {
            setTarget('')
            setAtAll(false)
          })}
        >
          <Plus size={16} />
          添加
        </button>
      </div>
      <div className="sub-list">
        {items.length ? (
          items.map((item) => (
            <div className="sub-item" key={metaOf(item) + labelOf(item)}>
              <div>
                <strong>{labelOf(item)}</strong>
                <small>{metaOf(item)}</small>
              </div>
              <button className="icon-button" disabled={saving} aria-label="删除" onClick={() => void onRemove(item)}>
                <Trash2 size={16} />
              </button>
            </div>
          ))
        ) : (
          <p className="muted">暂无订阅。在上方填写后添加；首次扫描通常只建基线、不刷历史。</p>
        )}
      </div>
    </div>
  )
}

export function Configuration() {
  const [data, setData] = useState<ConfigResponse | null>(null)
  const [values, setValues] = useState<Record<string, unknown>>({})
  const [active, setActive] = useState('')
  const [error, setError] = useState<unknown>(null)
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)
  const [subSaving, setSubSaving] = useState(false)
  const [xhs, setXhs] = useState<SubItem[]>([])
  const [douyin, setDouyin] = useState<SubItem[]>([])
  const [weibo, setWeibo] = useState<SubItem[]>([])
  const [rooms, setRooms] = useState<SubItem[]>([])

  const load = useCallback(async () => {
    try {
      const [response, xhsR, dyR, wbR, roomR] = await Promise.all([
        api<ConfigResponse>('config'),
        api<SubResponse>('xiaohongshu/subscriptions').catch(() => ({ subscriptions: [] as SubItem[] })),
        api<SubResponse>('douyin/subscriptions').catch(() => ({ subscriptions: [] as SubItem[] })),
        api<SubResponse>('weibo/subscriptions').catch(() => ({ subscriptions: [] as SubItem[] })),
        api<SubResponse>('pocket/rooms').catch(() => ({ subscriptions: [] as SubItem[] })),
      ])
      const next: Record<string, unknown> = {}
      Object.values(response.groups)
        .flat()
        .forEach((field) => {
          next[field.key] = field.value ?? (field.kind === 'boolean' ? false : field.kind === 'stringList' ? [] : '')
        })
      setData(response)
      setValues(next)
      setXhs(xhsR.subscriptions || [])
      setDouyin(dyR.subscriptions || [])
      setWeibo(wbR.subscriptions || [])
      setRooms(roomR.subscriptions || [])
      setActive((current) => current || response.groupOrder[0])
      setError(null)
    } catch (reason) {
      setError(reason)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const changed = useMemo(() => {
    if (!data) return false
    return Object.values(data.groups)
      .flat()
      .some((field) => JSON.stringify(values[field.key]) !== JSON.stringify(field.value ?? (field.kind === 'boolean' ? false : field.kind === 'stringList' ? [] : '')))
  }, [data, values])

  async function save() {
    if (!data || !changed) return
    const patch: Record<string, unknown> = {}
    Object.values(data.groups)
      .flat()
      .forEach((field) => {
        const original = field.value ?? (field.kind === 'boolean' ? false : field.kind === 'stringList' ? [] : '')
        if (JSON.stringify(values[field.key]) !== JSON.stringify(original)) patch[field.key] = values[field.key]
      })
    setSaving(true)
    setError(null)
    try {
      await api('config', { method: 'PUT', body: JSON.stringify({ values: patch }) })
      setSaved(true)
      window.setTimeout(() => setSaved(false), 2200)
      await load()
    } catch (reason) {
      setError(reason)
    } finally {
      setSaving(false)
    }
  }

  async function withSub(action: () => Promise<void>) {
    setSubSaving(true)
    setError(null)
    try {
      await action()
      await load()
    } catch (reason) {
      setError(reason)
    } finally {
      setSubSaving(false)
    }
  }

  if (error && !data) {
    return (
      <main className="page-content">
        <ErrorState error={error} retry={() => void load()} />
      </main>
    )
  }
  if (!data) {
    return (
      <main className="page-content">
        <div className="skeleton hero-skeleton" />
      </main>
    )
  }

  return (
    <main className="page-content config-page">
      <section className="page-heading">
        <div>
          <p className="eyebrow">SYSTEM CONFIGURATION</p>
          <h1>配置</h1>
          <p>按 Bot / 口袋48 / 微博 / 抖音 / 小红书 分组。保存后会安全重启 Bot。</p>
        </div>
        <button className="primary-button" disabled={!changed || saving} onClick={() => void save()}>
          {saved ? <Check size={17} /> : <Save size={17} />}
          {saved ? '已保存' : saving ? '保存中…' : '保存更改'}
        </button>
      </section>
      {error ? <div className="inline-error">{error instanceof Error ? error.message : '操作失败'}</div> : null}
      <div className="config-layout">
        <nav className="config-tabs">
          {data.groupOrder.map((group) => (
            <button key={group} className={active === group ? 'active' : ''} onClick={() => setActive(group)}>
              {group}
              <span>{data.groups[group]?.length || 0}</span>
            </button>
          ))}
        </nav>
        <section className="section-block config-section">
          <div className="section-title">
            <div>
              <h2>{active}</h2>
              <p>{groupBlurb[active] || '管理相关运行参数'}</p>
            </div>
          </div>
          <div className="config-fields">
            {(data.groups[active] || []).map((field) => (
              <Field key={field.key} field={field} value={values[field.key]} onChange={(value) => setValues((current) => ({ ...current, [field.key]: value }))} />
            ))}

            {active === '口袋48' ? (
              <SubscriptionManager
                title="房间订阅"
                hint="把口袋房间消息转发到指定 QQ 群。可用「bot search 名字」查房间 ID。"
                items={rooms}
                saving={subSaving}
                roomMode
                showAtAll={false}
                targetPlaceholder="口袋房间 ID（数字）"
                labelOf={(item) => `房间 ${item.roomId}`}
                metaOf={(item) => `QQ 群 ${item.groupId}`}
                onAdd={(groupId, target) =>
                  withSub(async () => {
                    await api('pocket/rooms', {
                      method: 'POST',
                      body: JSON.stringify({ groupId: Number(groupId), roomId: Number(target) }),
                    })
                  })
                }
                onRemove={(item) =>
                  withSub(async () => {
                    await api('pocket/rooms', {
                      method: 'DELETE',
                      body: JSON.stringify({ groupId: item.groupId, roomId: item.roomId }),
                    })
                  })
                }
              />
            ) : null}

            {active === '微博' ? (
              <SubscriptionManager
                title="微博 UID 订阅"
                hint="监控用户发博并转发到 QQ 群。Cookie 建议在上方开启浏览器登录后扫码。"
                items={weibo}
                saving={subSaving}
                targetPlaceholder="微博 UID（纯数字）"
                labelOf={(item) => `UID ${item.uid}`}
                metaOf={(item) => `群 ${item.groupId}${item.atAll ? ' · @全体' : ''}`}
                onAdd={(groupId, target, atAll) =>
                  withSub(async () => {
                    await api('weibo/subscriptions', {
                      method: 'POST',
                      body: JSON.stringify({ groupId: Number(groupId), uid: target.trim(), atAll }),
                    })
                  })
                }
                onRemove={(item) =>
                  withSub(async () => {
                    await api('weibo/subscriptions', {
                      method: 'DELETE',
                      body: JSON.stringify({ groupId: item.groupId, uid: item.uid }),
                    })
                  })
                }
              />
            ) : null}

            {active === '抖音' ? (
              <SubscriptionManager
                title="创作者订阅"
                hint="监控作品/开播。可多群分别添加。群聊 IM 用上方「目标群号」指定一个抖音群（当前实现单群）。"
                items={douyin}
                saving={subSaving}
                targetPlaceholder="抖音主页链接或 sec_user_id"
                labelOf={(item) => item.name || item.secUserId || '待获取昵称'}
                metaOf={(item) => `群 ${item.groupId} · ${item.secUserId || ''}${item.atAll ? ' · @全体' : ''}${item.liveId ? ` · live ${item.liveId}` : ''}`}
                onAdd={(groupId, target, atAll) =>
                  withSub(async () => {
                    await api('douyin/subscriptions', {
                      method: 'POST',
                      body: JSON.stringify({ groupId: Number(groupId), target: target.trim(), atAll }),
                    })
                  })
                }
                onRemove={(item) =>
                  withSub(async () => {
                    await api('douyin/subscriptions', {
                      method: 'DELETE',
                      body: JSON.stringify({ groupId: item.groupId, secUserId: item.secUserId }),
                    })
                  })
                }
              />
            ) : null}

            {active === '小红书' ? (
              <SubscriptionManager
                title="创作者订阅"
                hint="请粘贴 xhslink 或个人主页链接，不要填小红书号。可多群添加。"
                items={xhs}
                saving={subSaving}
                targetPlaceholder="xhslink 或个人主页链接"
                labelOf={(item) => item.name || '待获取昵称'}
                metaOf={(item) => `群 ${item.groupId} · ${item.userId || ''}${item.atAll ? ' · @全体' : ''}${item.liveActive ? ' · 直播中' : ''}`}
                onAdd={(groupId, target, atAll) =>
                  withSub(async () => {
                    await api('xiaohongshu/subscriptions', {
                      method: 'POST',
                      body: JSON.stringify({ groupId: Number(groupId), target: target.trim(), userId: '', profileUrl: '', atAll }),
                    })
                  })
                }
                onRemove={(item) =>
                  withSub(async () => {
                    await api('xiaohongshu/subscriptions', {
                      method: 'DELETE',
                      body: JSON.stringify({ groupId: item.groupId, userId: item.userId }),
                    })
                  })
                }
              />
            ) : null}
          </div>
        </section>
      </div>
    </main>
  )
}
