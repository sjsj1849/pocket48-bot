import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'
import { Check, Eye, EyeOff, Pencil, Plus, Save, Search, Trash2, X } from 'lucide-react'
import { api } from '../api'
import type { ConfigField } from '../types'
import { ErrorState } from '../App'

type ConfigResponse = { groups: Record<string, ConfigField[]>; groupOrder: string[] }
type SubItem = {
  groupId: number
  groupIds?: number[]
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
  enabled?: boolean
  source?: string
  status?: string
  worksEnabled?: boolean
  liveEnabled?: boolean
}
type SubResponse = { subscriptions: SubItem[] }
type SuperTopic = { oid: string; name?: string; groupKey?: string; groupName?: string; reportSign?: number; lastSignDate?: string; lastSignStatus?: string; lastSignRank?: number }
type SuperGroup = { key: string; name: string }
type SuperResponse = { topics: SuperTopic[]; groups?: SuperGroup[] }
type PocketSearchHit = {
  serverId: number
  serverName: string
  ownerName?: string
  roomId: number
  channelName: string
  status: string
  note: string
  lastMsgAt?: string
  lastMsgAgo?: string
  isLiveRoom?: boolean
}

const groupBlurb: Record<string, string> = {
  Bot: 'QQ 连接、媒体与邮件。详细说明见左侧「说明」。',
  口袋48: '账号 / NIM / 房间订阅。',
  微博: '登录态、动态 UID、超话签到、超话日报（分渠道）。',
  抖音: '创作者订阅 ≠ 群聊 IM。',
  小红书: '创作者订阅；登录走浏览器页。',
}

function Field({ field, value, onChange }: { field: ConfigField; value: unknown; onChange: (value: unknown) => void }) {
  const [reveal, setReveal] = useState(false)
  if (field.kind === 'boolean') {
    return (
      <label className="config-row toggle-row">
        <span>
          <strong>
            {field.label}
            {field.restartRequired ? <em className="restart-tag">需重启</em> : null}
          </strong>
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
          <strong>
            {field.label}
            {field.restartRequired ? <em className="restart-tag">需重启</em> : null}
          </strong>
          <small>{field.description}</small>
        </span>
        <textarea rows={3} value={Array.isArray(value) ? value.join('\n') : ''} onChange={(event) => onChange(event.target.value.split('\n'))} />
      </label>
    )
  }
  const placeholder =
    field.key === 'MEDIA_DELIVERY'
      ? 'local 或 remote'
      : field.key === 'WEIBO_SUPER_COUNT_DELIVERY'
        ? 'email / qq / both'
        : field.key === 'WEIBO_SUPER_COUNT_QQ'
          ? '额外 QQ 号，多个用逗号分隔；留空=仅管理员'
          : field.key === 'DOUYIN_IM_GROUP_NUMBER'
            ? '例如 296090848505，多个用逗号分隔'
            : field.kind === 'secret' && field.configured
              ? '已配置，留空保持不变'
              : ''
  if (field.key === 'WEIBO_SUPER_COUNT_DELIVERY' || field.key === 'MEDIA_DELIVERY') {
    const options =
      field.key === 'WEIBO_SUPER_COUNT_DELIVERY'
        ? [
            { value: 'email', label: '仅邮件（默认）' },
            { value: 'qq', label: '仅 QQ' },
            { value: 'both', label: '邮件 + QQ' },
          ]
        : [
            { value: 'local', label: 'local（本机下载转发）' },
            { value: 'remote', label: 'remote（直传链接）' },
          ]
    const current = typeof value === 'string' && value.trim() ? value.trim() : options[0].value
    return (
      <label className="config-row">
        <span>
          <strong>
            {field.label}
            {field.restartRequired ? <em className="restart-tag">需重启</em> : null}
          </strong>
          <small>{field.description}</small>
        </span>
        <select className="field-select config-select" value={current} onChange={(event) => onChange(event.target.value)}>
          {options.map((opt) => (
            <option key={opt.value} value={opt.value}>
              {opt.label}
            </option>
          ))}
        </select>
      </label>
    )
  }
  return (
    <label className="config-row">
      <span>
        <strong>
          {field.label}
          {field.restartRequired ? <em className="restart-tag">需重启</em> : null}
        </strong>
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

function HelpBox({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="help-box">
      <strong>{title}</strong>
      <div className="help-box-body">{children}</div>
    </div>
  )
}

type ManagerProps = {
  title: string
  hint: string
  items: SubItem[]
  saving: boolean
  onAdd: (groupId: string, target: string, atAll: boolean, name?: string, worksEnabled?: boolean, liveEnabled?: boolean) => Promise<void>
  onRemove: (item: SubItem) => Promise<void>
  onEdit?: (item: SubItem, groupId: string, target: string, atAll: boolean, name?: string, worksEnabled?: boolean, liveEnabled?: boolean) => Promise<void>
  onToggle?: (item: SubItem, enabled: boolean) => Promise<void>
  targetPlaceholder: string
  showAtAll?: boolean
  roomMode?: boolean
  showName?: boolean
  namePlaceholder?: string
  showMonitorKinds?: boolean
  lockIdentityOnEdit?: boolean
  defaultGroup?: string
  labelOf: (item: SubItem) => string
  metaOf: (item: SubItem) => string
  targetOf: (item: SubItem) => string
}

function SubscriptionManager({
  title,
  hint,
  items,
  saving,
  onAdd,
  onRemove,
  onEdit,
  onToggle,
  targetPlaceholder,
  showAtAll = true,
  roomMode,
  showName,
  namePlaceholder,
  showMonitorKinds,
  lockIdentityOnEdit,
  defaultGroup = '',
  labelOf,
  metaOf,
  targetOf,
}: ManagerProps) {
  const memoryKey = `p48-subscription-group:${title}`
  const [groupID, setGroupID] = useState(() => {
    try { return window.localStorage.getItem(memoryKey) || defaultGroup }
    catch { return defaultGroup }
  })
  const [target, setTarget] = useState('')
  const [name, setName] = useState('')
  const [atAll, setAtAll] = useState(false)
  const [worksEnabled, setWorksEnabled] = useState(true)
  const [liveEnabled, setLiveEnabled] = useState(true)
  const [editing, setEditing] = useState<SubItem | null>(null)
  const [editGroup, setEditGroup] = useState('')
  const [editTarget, setEditTarget] = useState('')
  const [editName, setEditName] = useState('')
  const [editAtAll, setEditAtAll] = useState(false)
  const [editWorksEnabled, setEditWorksEnabled] = useState(true)
  const [editLiveEnabled, setEditLiveEnabled] = useState(true)

  useEffect(() => {
    if (defaultGroup && !groupID) setGroupID(defaultGroup)
  }, [defaultGroup, groupID])

  function updateGroup(value: string) {
    setGroupID(value)
    try { window.localStorage.setItem(memoryKey, value) } catch { /* storage can be disabled */ }
  }

  function startEdit(item: SubItem) {
    setEditing(item)
    setEditGroup(String(item.groupId || ''))
    setEditTarget(targetOf(item))
    setEditName(item.name || '')
    setEditAtAll(Boolean(item.atAll))
    setEditWorksEnabled(item.worksEnabled !== false)
    setEditLiveEnabled(item.liveEnabled !== false)
  }

  return (
    <div className="sub-manager">
      <div className="sub-manager-title">
        <strong>{title}</strong>
        <p>{hint}</p>
      </div>
      <div className={`sub-add${showName ? ' with-name' : ''}`}>
        <input type="text" inputMode="numeric" pattern="[0-9]*" placeholder="目标 QQ 群号" value={groupID} onChange={(e) => updateGroup(e.target.value.replace(/\D/g, ''))} />
        <input type={roomMode ? 'number' : 'text'} min={roomMode ? 1 : undefined} placeholder={targetPlaceholder} value={target} onChange={(e) => setTarget(e.target.value)} />
        {showName ? <input type="text" placeholder={namePlaceholder || '昵称（可选）'} value={name} onChange={(e) => setName(e.target.value)} /> : null}
        {showMonitorKinds ? (
          <div className="monitor-kind-options">
            <label><input type="checkbox" checked={worksEnabled} onChange={(e) => setWorksEnabled(e.target.checked)} /> 作品</label>
            <label><input type="checkbox" checked={liveEnabled} onChange={(e) => setLiveEnabled(e.target.checked)} /> 直播</label>
          </div>
        ) : null}
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
          onClick={() =>
            void onAdd(groupID, target, atAll, name, worksEnabled, liveEnabled).then(() => {
              setTarget('')
              setName('')
              setAtAll(false)
            })
          }
        >
          <Plus size={16} />
          添加
        </button>
      </div>
      <div className="sub-list">
        {items.length ? (
          items.map((item) => {
            const key = metaOf(item) + labelOf(item) + targetOf(item)
            const isEditing = editing && targetOf(editing) === targetOf(item) && editing.groupId === item.groupId
            if (isEditing && onEdit) {
              return (
                <div className="sub-item editing" key={key}>
                  <div className="sub-edit-grid">
                    <input type="text" inputMode="numeric" pattern="[0-9]*" placeholder="QQ 群号" value={editGroup} onChange={(e) => setEditGroup(e.target.value.replace(/\D/g, ''))} />
                    <input
                      type={roomMode ? 'number' : 'text'}
                      placeholder={targetPlaceholder}
                      value={editTarget}
                      readOnly={lockIdentityOnEdit}
                      aria-label={lockIdentityOnEdit ? '绑定 ID（不可修改）' : undefined}
                      title={lockIdentityOnEdit ? '绑定 ID 不可修改；如需更换账号，请删除后重新添加' : undefined}
                      onChange={(e) => setEditTarget(e.target.value)}
                    />
                    {showName ? <input type="text" placeholder={namePlaceholder || '昵称'} value={editName} onChange={(e) => setEditName(e.target.value)} /> : null}
                    {showMonitorKinds ? (
                      <div className="monitor-kind-options">
                        <label><input type="checkbox" checked={editWorksEnabled} onChange={(e) => setEditWorksEnabled(e.target.checked)} /> 作品</label>
                        <label><input type="checkbox" checked={editLiveEnabled} onChange={(e) => setEditLiveEnabled(e.target.checked)} /> 直播</label>
                      </div>
                    ) : null}
                    {showAtAll ? (
                      <label>
                        <input type="checkbox" checked={editAtAll} onChange={(e) => setEditAtAll(e.target.checked)} /> @全体
                      </label>
                    ) : null}
                  </div>
                  <div className="sub-item-actions">
                    <button
                      className="secondary-button"
                      disabled={saving || !editGroup || !editTarget.trim()}
                      onClick={() =>
                        void onEdit(item, editGroup, editTarget, editAtAll, editName, editWorksEnabled, editLiveEnabled).then(() => setEditing(null))
                      }
                    >
                      <Check size={15} />
                      保存
                    </button>
                    <button className="icon-button" disabled={saving} aria-label="取消" onClick={() => setEditing(null)}>
                      <X size={16} />
                    </button>
                  </div>
                </div>
              )
            }
            return (
              <div className="sub-item" key={key}>
                <div>
                  <strong>{labelOf(item)}</strong>
                  <small>{metaOf(item)}</small>
                </div>
                <div className="sub-item-actions">
                  {onToggle ? (
                    <button className="secondary-button" disabled={saving} onClick={() => void onToggle(item, item.enabled === false)}>
                      {item.enabled === false ? '启用' : '停用'}
                    </button>
                  ) : null}
                  {onEdit ? (
                    <button className="icon-button" disabled={saving} aria-label="编辑" onClick={() => startEdit(item)}>
                      <Pencil size={15} />
                    </button>
                  ) : null}
                  <button className="icon-button" disabled={saving} aria-label="删除" onClick={() => void onRemove(item)}>
                    <Trash2 size={16} />
                  </button>
                </div>
              </div>
            )
          })
        ) : (
          <p className="muted">暂无订阅。在上方填写后添加；首次扫描通常只建基线、不刷历史。</p>
        )}
      </div>
    </div>
  )
}

function parseGroupIDs(value: string): number[] {
  return [...new Set((value.match(/\d+/g) || []).map(Number).filter((id) => id > 0))]
}

function DouyinSubscriptionManager({
  items,
  saving,
  defaultGroup,
  onAdd,
  onEdit,
  onRemove,
}: {
  items: SubItem[]
  saving: boolean
  defaultGroup: string
  onAdd: (groupIds: number[], target: string, atAll: boolean, name: string, worksEnabled: boolean, liveEnabled: boolean) => Promise<void>
  onEdit: (item: SubItem, groupIds: number[], atAll: boolean, worksEnabled: boolean, liveEnabled: boolean) => Promise<void>
  onRemove: (item: SubItem) => Promise<void>
}) {
  const memoryKey = 'p48-subscription-group:抖音创作者'
  const [groups, setGroups] = useState(() => {
    try { return window.localStorage.getItem(memoryKey) || defaultGroup }
    catch { return defaultGroup }
  })
  const [target, setTarget] = useState('')
  const [name, setName] = useState('')
  const [atAll, setAtAll] = useState(false)
  const [worksEnabled, setWorksEnabled] = useState(true)
  const [liveEnabled, setLiveEnabled] = useState(true)
  const [editingID, setEditingID] = useState('')
  const [editGroups, setEditGroups] = useState('')
  const [editAtAll, setEditAtAll] = useState(false)
  const [editWorksEnabled, setEditWorksEnabled] = useState(true)
  const [editLiveEnabled, setEditLiveEnabled] = useState(true)

  useEffect(() => {
    if (defaultGroup && !groups) setGroups(defaultGroup)
  }, [defaultGroup, groups])

  function updateGroups(value: string) {
    setGroups(value)
    try { window.localStorage.setItem(memoryKey, value) } catch { /* storage can be disabled */ }
  }

  function startEdit(item: SubItem) {
    setEditingID(item.secUserId || '')
    setEditGroups((item.groupIds?.length ? item.groupIds : [item.groupId]).join(', '))
    setEditAtAll(Boolean(item.atAll))
    setEditWorksEnabled(item.worksEnabled !== false)
    setEditLiveEnabled(item.liveEnabled !== false)
  }

  const addGroupIDs = parseGroupIDs(groups)

  return (
    <div className="sub-manager douyin-manager">
      <div className="sub-manager-title">
        <strong>创作者订阅（作品/开播）</strong>
        <p>主页链接只在新增时用于解析。添加后以 User ID 绑定，可调整转发群、@全体以及作品/直播监控。</p>
      </div>
      <div className="douyin-add">
        <input
          type="text"
          inputMode="numeric"
          placeholder="目标 QQ 群号，多个用逗号分隔"
          value={groups}
          onChange={(event) => updateGroups(event.target.value)}
        />
        <input type="text" placeholder="抖音主页链接或 sec_user_id" value={target} onChange={(event) => setTarget(event.target.value)} />
        <input type="text" placeholder="昵称或备注（可选）" value={name} onChange={(event) => setName(event.target.value)} />
        <div className="monitor-kind-options">
          <label><input type="checkbox" checked={worksEnabled} onChange={(event) => setWorksEnabled(event.target.checked)} /> 作品</label>
          <label><input type="checkbox" checked={liveEnabled} onChange={(event) => setLiveEnabled(event.target.checked)} /> 直播</label>
          <label><input type="checkbox" checked={atAll} onChange={(event) => setAtAll(event.target.checked)} /> @全体</label>
        </div>
        <button
          className="secondary-button"
          disabled={saving || addGroupIDs.length === 0 || !target.trim()}
          onClick={() => void onAdd(addGroupIDs, target, atAll, name, worksEnabled, liveEnabled).then(() => {
            setTarget('')
            setName('')
            setAtAll(false)
          })}
        >
          <Plus size={16} />
          添加
        </button>
      </div>
      <div className="douyin-list">
        {items.length ? items.map((item) => {
          const userID = item.secUserId || ''
          const groupIDs = item.groupIds?.length ? item.groupIds : [item.groupId]
          const isEditing = editingID === userID
          if (isEditing) {
            const nextGroupIDs = parseGroupIDs(editGroups)
            return (
              <article className="douyin-card editing" key={userID}>
                <div className="douyin-edit-grid">
                  <label>
                    <span>绑定 User ID</span>
                    <input value={userID} readOnly aria-label="绑定 User ID（不可修改）" title="User ID 不可修改；如需更换账号，请删除后重新添加" />
                  </label>
                  <label>
                    <span>转发到 QQ 群（可多个）</span>
                    <input value={editGroups} inputMode="numeric" onChange={(event) => setEditGroups(event.target.value)} placeholder="多个群号用逗号分隔" />
                  </label>
                </div>
                <div className="douyin-edit-options">
                  <label><input type="checkbox" checked={editWorksEnabled} onChange={(event) => setEditWorksEnabled(event.target.checked)} /> 作品监控</label>
                  <label><input type="checkbox" checked={editLiveEnabled} onChange={(event) => setEditLiveEnabled(event.target.checked)} /> 直播监控</label>
                  <label><input type="checkbox" checked={editAtAll} onChange={(event) => setEditAtAll(event.target.checked)} /> @全体成员</label>
                </div>
                <div className="sub-item-actions">
                  <button
                    className="secondary-button"
                    disabled={saving || nextGroupIDs.length === 0}
                    onClick={() => void onEdit(item, nextGroupIDs, editAtAll, editWorksEnabled, editLiveEnabled).then(() => setEditingID(''))}
                  >
                    <Check size={15} /> 保存
                  </button>
                  <button className="icon-button" disabled={saving} aria-label="取消编辑" onClick={() => setEditingID('')}>
                    <X size={16} />
                  </button>
                </div>
              </article>
            )
          }
          return (
            <article className="douyin-card" key={userID}>
              <div className="douyin-card-main">
                <div className="douyin-card-heading">
                  <strong>{item.name || '昵称解析中…'}</strong>
                  <div className="subscription-badges">
                    {item.worksEnabled !== false ? <span>作品</span> : null}
                    {item.liveEnabled !== false ? <span>直播</span> : null}
                    {item.atAll ? <span className="attention">@全体</span> : null}
                    {item.enabled === false ? <span className="off">已停用</span> : null}
                    {item.worksEnabled === false && item.liveEnabled === false ? <span className="off">监控已关闭</span> : null}
                  </div>
                </div>
                <div className="douyin-identity" title={userID}><span>User ID</span><code>{userID}</code></div>
                <div className="douyin-groups"><span>转发群</span>{groupIDs.map((id) => <b key={id}>{id}</b>)}</div>
                {item.status ? <p>{item.status}</p> : null}
              </div>
              <div className="sub-item-actions">
                <button className="icon-button" disabled={saving} aria-label={`编辑 ${item.name || userID}`} onClick={() => startEdit(item)}>
                  <Pencil size={15} />
                </button>
                <button className="icon-button danger" disabled={saving} aria-label={`删除 ${item.name || userID}`} onClick={() => void onRemove(item)}>
                  <Trash2 size={16} />
                </button>
              </div>
            </article>
          )
        }) : <p className="muted">暂无订阅。在上方填写后添加；首次扫描通常只建基线、不刷历史。</p>}
      </div>
    </div>
  )
}

function PocketSearchPanel({
  saving,
  defaultGroup,
  onAddRoom,
  subscribedRoomIds = [] as number[],
}: {
  saving: boolean
  defaultGroup: string
  onAddRoom: (groupId: string, roomId: number) => Promise<void>
  subscribedRoomIds?: number[]
}) {
  const [q, setQ] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [note, setNote] = useState('')
  const [hits, setHits] = useState<PocketSearchHit[]>([])
  const [groupID, setGroupID] = useState(defaultGroup)

  useEffect(() => {
    if (defaultGroup && !groupID) setGroupID(defaultGroup)
  }, [defaultGroup, groupID])

  // clear results when input is cleared
  useEffect(() => {
    if (!q.trim()) { setHits([]); setNote(''); setError('') }
  }, [q])

  async function search() {
    const query = q.trim()
    if (!query) return
    setLoading(true)
    setError('')
    try {
      const res = await api<{ results: PocketSearchHit[]; note?: string }>(`pocket/search?q=${encodeURIComponent(query)}`)
      setHits(res.results || [])
      setNote(res.note || '')
    } catch (e) {
      setHits([])
      setNote('')
      setError(e instanceof Error ? e.message : '搜索失败')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="sub-manager pocket-search">
      <div className="sub-manager-title">
        <strong>按名字搜索房间</strong>
        <p>输入小偶像名字，探测房间是否可访问 / 是否近期有消息。选中后一键加入订阅。</p>
      </div>
      <div className="sub-add">
        <input type="number" min="0" placeholder="加入到的 QQ 群号（0 可能历史遗留）" value={groupID} onChange={(e) => setGroupID(e.target.value)} />
        <input
          type="text"
          placeholder="小偶像名字，例如 胡晓慧"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') void search()
          }}
        />
        <button className="secondary-button" disabled={loading || !q.trim()} onClick={() => void search()}>
          <Search size={16} />
          {loading ? '搜索中…' : '搜索'}
        </button>
      </div>
      {error ? <div className="inline-error">{error}</div> : null}
      {note ? <p className="muted compact">{note}</p> : null}
      <div className="sub-list">
        {hits.map((hit) => (
          <div className="sub-item" key={`${hit.serverId}-${hit.roomId}`}>
            <div>
              <strong>
                {hit.channelName || hit.serverName || '未命名'} · 房间 {hit.roomId || '—'}
                {hit.isLiveRoom ? <span className="live-badge">直播</span> : null}
              </strong>
              <small>
                {hit.isLiveRoom ? '直播频道' : hit.status === 'open' ? '可访问' : hit.status === 'closed' ? '已关闭' : '未知'} · {hit.note}
                {hit.lastMsgAt ? ` · 最近 ${hit.lastMsgAt}` : ''}
                {hit.ownerName ? ` · ${hit.ownerName}` : ''}
              </small>
            </div>
            <button
              className="secondary-button"
              disabled={saving || !groupID || !hit.roomId || hit.status === 'closed' || subscribedRoomIds.includes(hit.roomId) || hit.isLiveRoom}
              onClick={() => void onAddRoom(groupID, hit.roomId)}
            >
              <Plus size={15} />
              {hit.isLiveRoom ? '直播' : subscribedRoomIds.includes(hit.roomId) ? '已订阅' : '加入'}
            </button>
          </div>
        ))}
      </div>
    </div>
  )
}

function SuperSignManager({
  items,
  saving,
  onAdd,
  onRemove,
  onEdit,
}: {
  items: SuperTopic[]
  saving: boolean
  onAdd: (oid: string, name: string) => Promise<void>
  onRemove: (item: SuperTopic) => Promise<void>
  onEdit: (item: SuperTopic, oid: string, name: string) => Promise<void>
}) {
  const [oid, setOid] = useState('')
  const [name, setName] = useState('')
  const [editing, setEditing] = useState<SuperTopic | null>(null)
  const [eOid, setEOid] = useState('')
  const [eName, setEName] = useState('')

  return (
    <div className="sub-manager">
      <div className="sub-manager-title">
        <strong>超话签到列表（自动签到）</strong>
        <p>对应 WEIBO_SUPER_TOPICS。与下方「日报列表」不是同一份数据：这里只决定每天自动签哪些超话。</p>
      </div>
      <p className="muted compact">OID 从超话 URL 的 containerid 复制。与日报列表独立。详见「说明」。</p>
      <div className="sub-add with-name">
        <input type="text" placeholder="超话 OID（100808…）" value={oid} onChange={(e) => setOid(e.target.value)} />
        <input type="text" placeholder="显示名（可选）" value={name} onChange={(e) => setName(e.target.value)} />
        <span />
        <button
          className="secondary-button"
          disabled={saving || !oid.trim()}
          onClick={() =>
            void onAdd(oid, name).then(() => {
              setOid('')
              setName('')
            })
          }
        >
          <Plus size={16} />
          添加
        </button>
      </div>
      <div className="sub-list">
        {items.length ? (
          items.map((item) => {
            if (editing && editing.oid === item.oid) {
              return (
                <div className="sub-item editing" key={item.oid}>
                  <div className="sub-edit-grid two">
                    <input value={eOid} onChange={(e) => setEOid(e.target.value)} placeholder="OID" />
                    <input value={eName} onChange={(e) => setEName(e.target.value)} placeholder="名称" />
                  </div>
                  <div className="sub-item-actions">
                    <button className="secondary-button" disabled={saving} onClick={() => void onEdit(item, eOid, eName).then(() => setEditing(null))}>
                      <Check size={15} />
                      保存
                    </button>
                    <button className="icon-button" onClick={() => setEditing(null)}>
                      <X size={16} />
                    </button>
                  </div>
                </div>
              )
            }
            return (
              <div className="sub-item" key={item.oid}>
                <div>
                  <strong>{item.name || item.oid}</strong>
                  <small>
                    {item.oid}
                    {item.lastSignDate ? ` · 最近 ${item.lastSignDate}` : ''}
                    {item.lastSignStatus ? ` · ${item.lastSignStatus}` : ''}
                  </small>
                </div>
                <div className="sub-item-actions">
                  <button
                    className="icon-button"
                    disabled={saving}
                    aria-label="编辑"
                    onClick={() => {
                      setEditing(item)
                      setEOid(item.oid)
                      setEName(item.name || '')
                    }}
                  >
                    <Pencil size={15} />
                  </button>
                  <button className="icon-button" disabled={saving} aria-label="删除" onClick={() => void onRemove(item)}>
                    <Trash2 size={16} />
                  </button>
                </div>
              </div>
            )
          })
        ) : (
          <p className="muted">暂无签到超话。开启上方「超话自动签到」后在此添加。</p>
        )}
      </div>
    </div>
  )
}

function SuperCountManager({
  items,
  groups,
  saving,
  onAdd,
  onRemove,
  onEdit,
}: {
  items: SuperTopic[]
  groups: SuperGroup[]
  saving: boolean
  onAdd: (oid: string, name: string, groupName: string) => Promise<void>
  onRemove: (item: SuperTopic) => Promise<void>
  onEdit: (item: SuperTopic, oid: string, name: string, groupName: string) => Promise<void>
}) {
  const [oid, setOid] = useState('')
  const [name, setName] = useState('')
  const [groupName, setGroupName] = useState('')
  const [editing, setEditing] = useState<SuperTopic | null>(null)
  const [eOid, setEOid] = useState('')
  const [eName, setEName] = useState('')
  const [eGroup, setEGroup] = useState('')

  return (
    <div className="sub-manager">
      <div className="sub-manager-title">
        <strong>超话日报列表</strong>
        <p>按分组统计与推送（与上方签到列表独立）。发送渠道在上方「日报发送渠道」。</p>
      </div>
      {groups.length ? (
        <p className="muted compact">已有分组：{groups.map((g) => g.name).join('、')}</p>
      ) : null}
      <div className="sub-add with-name">
        <input type="text" placeholder="超话 OID（100808…）" value={oid} onChange={(e) => setOid(e.target.value)} />
        <input type="text" placeholder="显示名（可选）" value={name} onChange={(e) => setName(e.target.value)} />
        <input
          type="text"
          className="group-input"
          placeholder="分组显示名（如 X姐姐们）"
          value={groupName}
          onChange={(e) => setGroupName(e.target.value)}
          list="super-count-groups"
        />
        <datalist id="super-count-groups">
          {groups.map((g) => (
            <option key={g.key} value={g.name} />
          ))}
        </datalist>
        <button
          className="secondary-button"
          disabled={saving || !oid.trim()}
          onClick={() =>
            void onAdd(oid, name, groupName).then(() => {
              setOid('')
              setName('')
              setGroupName('')
            })
          }
        >
          <Plus size={16} />
          添加
        </button>
      </div>
      <div className="sub-list">
        {items.length ? (
          items.map((item) => {
            if (editing && editing.oid === item.oid) {
              return (
                <div className="sub-item editing" key={item.oid}>
                  <div className="sub-edit-grid">
                    <input value={eOid} onChange={(e) => setEOid(e.target.value)} placeholder="OID" />
                    <input value={eName} onChange={(e) => setEName(e.target.value)} placeholder="名称" />
                    <input
                      value={eGroup}
                      onChange={(e) => setEGroup(e.target.value)}
                      placeholder="分组显示名"
                      list="super-count-groups"
                      className="group-input"
                    />
                  </div>
                  <div className="sub-item-actions">
                    <button className="secondary-button" disabled={saving} onClick={() => void onEdit(item, eOid, eName, eGroup).then(() => setEditing(null))}>
                      <Check size={15} />
                      保存
                    </button>
                    <button className="icon-button" onClick={() => setEditing(null)}>
                      <X size={16} />
                    </button>
                  </div>
                </div>
              )
            }
            return (
              <div className="sub-item" key={item.oid}>
                <div>
                  <strong>{item.name || item.oid}</strong>
                  <small>
                    {item.groupName ? `分组 ${item.groupName}` : '未分组'}
                    {item.groupKey && item.groupKey !== item.groupName ? `（${item.groupKey}）` : ''}
                    {` · ${item.oid}`}
                    {item.reportSign ? ` · report_sign=${item.reportSign}` : ''}
                  </small>
                </div>
                <div className="sub-item-actions">
                  <button
                    className="icon-button"
                    disabled={saving}
                    aria-label="编辑"
                    onClick={() => {
                      setEditing(item)
                      setEOid(item.oid)
                      setEName(item.name || '')
                      setEGroup(item.groupName || '')
                    }}
                  >
                    <Pencil size={15} />
                  </button>
                  <button className="icon-button" disabled={saving} aria-label="删除" onClick={() => void onRemove(item)}>
                    <Trash2 size={16} />
                  </button>
                </div>
              </div>
            )
          })
        ) : (
          <p className="muted">暂无日报超话。开启上方「超话日报」后在此添加。</p>
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
  const [superTopics, setSuperTopics] = useState<SuperTopic[]>([])
  const [signTopics, setSignTopics] = useState<SuperTopic[]>([])
  const [superGroups, setSuperGroups] = useState<SuperGroup[]>([])

  const load = useCallback(async () => {
    try {
      const [response, xhsR, dyR, wbR, roomR, superR, signR] = await Promise.all([
        api<ConfigResponse>('config'),
        api<SubResponse>('xiaohongshu/subscriptions').catch(() => ({ subscriptions: [] as SubItem[] })),
        api<SubResponse>('douyin/subscriptions').catch(() => ({ subscriptions: [] as SubItem[] })),
        api<SubResponse>('weibo/subscriptions').catch(() => ({ subscriptions: [] as SubItem[] })),
        api<SubResponse>('pocket/rooms').catch(() => ({ subscriptions: [] as SubItem[] })),
        api<SuperResponse>('weibo/super-count/topics').catch(() => ({ topics: [] as SuperTopic[], groups: [] as SuperGroup[] })),
        api<SuperResponse>('weibo/super-sign/topics').catch(() => ({ topics: [] as SuperTopic[] })),
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
      setSuperTopics(superR.topics || [])
      setSuperGroups(superR.groups || [])
      setSignTopics(signR.topics || [])
      setActive((current) => current || response.groupOrder[0])
      setError(null)
    } catch (reason) {
      setError(reason)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  useEffect(() => {
    if (active !== '抖音' || !values.DOUYIN_ENABLED || !douyin.some((item) => !item.name && item.enabled !== false)) return
    const timer = window.setInterval(() => {
      void api<SubResponse>('douyin/subscriptions')
        .then((response) => setDouyin(response.subscriptions || []))
        .catch(() => undefined)
    }, 5000)
    return () => window.clearInterval(timer)
  }, [active, douyin, values.DOUYIN_ENABLED])

  const boundGroup = useMemo(() => String(values.BOUND_GROUP_ID || ''), [values.BOUND_GROUP_ID])
  const activeMasterKey = active === '抖音' ? 'DOUYIN_ENABLED' : active === '小红书' ? 'XIAOHONGSHU_ENABLED' : ''
  const activePlatformEnabled = !activeMasterKey || Boolean(values[activeMasterKey])

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
          <p>按分组管理参数与订阅。能热重载的会直接生效；带「需重启」的保存后会重启 Bot。</p>
        </div>
        <div className="heading-actions">
          <button type="button" className="secondary-button" onClick={() => window.dispatchEvent(new CustomEvent('pocket48-navigate', { detail: 'docs' }))}>
            配置说明
          </button>
          <button className="primary-button" disabled={!changed || saving} onClick={() => void save()}>
          {saved ? <Check size={17} /> : <Save size={17} />}
          {saved ? '已保存' : saving ? '保存中…' : '保存更改'}
        </button>
        </div>
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
            {(data.groups[active] || []).filter((field) => activePlatformEnabled || field.key === activeMasterKey).map((field) => (
              <Field key={field.key} field={field} value={values[field.key]} onChange={(value) => setValues((current) => ({ ...current, [field.key]: value }))} />
            ))}

            {active === '口袋48' ? (
              <>
                <PocketSearchPanel
                  saving={subSaving}
                  defaultGroup={boundGroup}
                  subscribedRoomIds={rooms.map((r) => r.roomId || 0)}
                  onAddRoom={(groupId, roomId) =>
                    withSub(async () => {
                      await api('pocket/rooms', {
                        method: 'POST',
                        body: JSON.stringify({ groupId: Number(groupId), roomId }),
                      })
                    })
                  }
                />
                <SubscriptionManager
                  title="房间订阅"
                  defaultGroup={boundGroup}
                  hint="已订阅房间列表。可用上方搜索加入；也可直接填房间 ID。铅笔可改 QQ 群或房间 ID。"
                  items={rooms}
                  saving={subSaving}
                  roomMode
                  showAtAll={false}
                  targetPlaceholder="口袋房间 ID（数字）"
                  labelOf={(item) => item.name || `房间 ${item.roomId}`}
                  metaOf={(item) => `QQ 群 ${item.groupId} · 房间 ${item.roomId}`}
                  targetOf={(item) => String(item.roomId || '')}
                  onAdd={(groupId, target) =>
                    withSub(async () => {
                      await api('pocket/rooms', {
                        method: 'POST',
                        body: JSON.stringify({ groupId: Number(groupId), roomId: Number(target) }),
                      })
                    })
                  }
                  onEdit={(item, groupId, target) =>
                    withSub(async () => {
                      await api('pocket/rooms', {
                        method: 'PUT',
                        body: JSON.stringify({
                          oldGroupId: item.groupId,
                          oldRoomId: item.roomId,
                          groupId: Number(groupId),
                          roomId: Number(target),
                        }),
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
              </>
            ) : null}

            {active === '微博' ? (
              <>
                <p className="muted compact">Cookie 是登录态不是密码；UID/OID 与日报说明见「说明」页。浏览器扫码在左侧「浏览器」。</p>
                <SubscriptionManager
                  title="微博动态 UID 订阅"
                  defaultGroup={boundGroup}
                  hint="监控用户发博并转发到 QQ 群。群 0 多为历史私信误加，请用铅笔改到绑定群。昵称优先展示，UID 在副标题。"
                  items={weibo}
                  saving={subSaving}
                  showName
                  lockIdentityOnEdit
                  namePlaceholder="昵称（可选，便于识别）"
                  targetPlaceholder="微博 UID（纯数字）"
                  labelOf={(item) => (item.name ? `${item.name}` : `UID ${item.uid}`)}
                  metaOf={(item) => `群 ${item.groupId}${item.name ? ` · UID ${item.uid}` : ''}${item.atAll ? ' · @全体' : ''}`}
                  targetOf={(item) => item.uid || ''}
                  onAdd={(groupId, target, atAll, name) =>
                    withSub(async () => {
                      await api('weibo/subscriptions', {
                        method: 'POST',
                        body: JSON.stringify({ groupId: Number(groupId), uid: target.trim(), atAll, name: name?.trim() || undefined }),
                      })
                    })
                  }
                  onEdit={(item, groupId, target, atAll, name) =>
                    withSub(async () => {
                      await api('weibo/subscriptions', {
                        method: 'PUT',
                        body: JSON.stringify({
                          oldGroupId: item.groupId,
                          oldUid: item.uid,
                          groupId: Number(groupId),
                          uid: item.uid || target.trim(),
                          atAll,
                          name: name?.trim() || undefined,
                        }),
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
                <SuperSignManager
                  items={signTopics}
                  saving={subSaving}
                  onAdd={(oid, name) =>
                    withSub(async () => {
                      await api('weibo/super-sign/topics', {
                        method: 'POST',
                        body: JSON.stringify({ oid: oid.trim(), name: name.trim() || undefined }),
                      })
                    })
                  }
                  onEdit={(_item, oid, name) =>
                    withSub(async () => {
                      await api('weibo/super-sign/topics', {
                        method: 'PUT',
                        body: JSON.stringify({ oid: oid.trim(), name: name.trim() || undefined }),
                      })
                    })
                  }
                  onRemove={(item) =>
                    withSub(async () => {
                      await api('weibo/super-sign/topics', {
                        method: 'DELETE',
                        body: JSON.stringify({ oid: item.oid }),
                      })
                    })
                  }
                />
                <SuperCountManager
                  items={superTopics}
                  groups={superGroups}
                  saving={subSaving}
                  onAdd={(oid, name, groupName) =>
                    withSub(async () => {
                      await api('weibo/super-count/topics', {
                        method: 'POST',
                        body: JSON.stringify({ oid: oid.trim(), name: name.trim() || undefined, groupName: groupName.trim() || undefined }),
                      })
                    })
                  }
                  onEdit={(_item, oid, name, groupName) =>
                    withSub(async () => {
                      await api('weibo/super-count/topics', {
                        method: 'PUT',
                        body: JSON.stringify({ oid: oid.trim(), name: name.trim() || undefined, groupName: groupName.trim() || undefined }),
                      })
                    })
                  }
                  onRemove={(item) =>
                    withSub(async () => {
                      await api('weibo/super-count/topics', {
                        method: 'DELETE',
                        body: JSON.stringify({ oid: item.oid }),
                      })
                    })
                  }
                />
              </>
            ) : null}

            {active === '抖音' && activePlatformEnabled ? (
              <>
                <p className="muted compact">创作者订阅与群聊 IM 是两套配置；登录扫码见「浏览器」，细则见「说明」。</p>
                <DouyinSubscriptionManager
                  items={douyin}
                  saving={subSaving}
                  defaultGroup={boundGroup}
                  onAdd={(groupIds, target, atAll, name, worksEnabled, liveEnabled) =>
                    withSub(async () => {
                      await api('douyin/subscriptions', {
                        method: 'POST',
                        body: JSON.stringify({ groupIds, target: target.trim(), name: name.trim() || undefined, atAll, enabled: true, worksEnabled, liveEnabled }),
                      })
                    })
                  }
                  onEdit={(item, groupIds, atAll, worksEnabled, liveEnabled) =>
                    withSub(async () => {
                      await api('douyin/subscriptions', {
                        method: 'PUT',
                        body: JSON.stringify({
                          oldGroupIds: item.groupIds?.length ? item.groupIds : [item.groupId],
                          oldSecUserId: item.secUserId,
                          groupIds,
                          secUserId: item.secUserId,
                          atAll,
                          enabled: true,
                          worksEnabled,
                          liveEnabled,
                        }),
                      })
                    })
                  }
                  onRemove={(item) =>
                    withSub(async () => {
                      await api('douyin/subscriptions', {
                        method: 'DELETE',
                        body: JSON.stringify({ groupIds: item.groupIds?.length ? item.groupIds : [item.groupId], secUserId: item.secUserId }),
                      })
                    })
                  }
                />
              </>
            ) : null}

            {active === '小红书' && activePlatformEnabled ? (
              <>
                <p className="muted compact">用主页/xhslink；登录与 notes 验收见「浏览器 / 说明」。</p>
                <SubscriptionManager
                  title="创作者订阅"
                  defaultGroup={boundGroup}
                  hint="可多群添加。铅笔可改 QQ 群或目标链接。"
                  items={xhs}
                  saving={subSaving}
                  targetPlaceholder="xhslink 或个人主页链接"
                  lockIdentityOnEdit
                  labelOf={(item) => item.name || '待获取昵称'}
                  metaOf={(item) => `群 ${item.groupId} · ${item.userId || ''}${item.atAll ? ' · @全体' : ''}${item.liveActive ? ' · 直播中' : ''}`}
                  targetOf={(item) => item.userId || item.profileUrl || ''}
                  onAdd={(groupId, target, atAll) =>
                    withSub(async () => {
                      await api('xiaohongshu/subscriptions', {
                        method: 'POST',
                        body: JSON.stringify({ groupId: Number(groupId), target: target.trim(), userId: '', profileUrl: '', atAll }),
                      })
                    })
                  }
                  onEdit={(item, groupId, target, atAll) =>
                    withSub(async () => {
                      await api('xiaohongshu/subscriptions', {
                        method: 'PUT',
                        body: JSON.stringify({
                          oldGroupId: item.groupId,
                          oldUserId: item.userId,
                          groupId: Number(groupId),
                          userId: item.userId || target.trim(),
                          atAll,
                        }),
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
              </>
            ) : null}
          </div>
        </section>
      </div>
    </main>
  )
}
