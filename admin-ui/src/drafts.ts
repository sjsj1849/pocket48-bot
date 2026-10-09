import { useEffect, useRef } from 'react'

// 各平台面板的运行设置存放在独立的 settings.json / 独立接口里，
// 但用户只有一个保存动作。面板把自己的「待保存值 + 落盘函数」注册到
// 这里，配置页右上角的「保存更改」会统一提交所有已注册的草稿。
export type Draft = {
  // 面板内展示用的名字，仅用于错误提示
  label: string
  // 当前待保存的值；undefined 表示不参与统一保存
  value: unknown
  // 把草稿写回服务端；resolve 后视为已保存
  commit: (value: unknown) => Promise<unknown>
  // 草稿是否已改动
  dirty: boolean
}

type Listener = () => void

const drafts = new Map<string, Draft>()
const listeners = new Set<Listener>()

function emit() {
  for (const listener of listeners) listener()
}

export function registerDraft(key: string, draft: Draft) {
  const previous = drafts.get(key)
  drafts.set(key, draft)
  // 仅在「脏状态」这个布尔值变化时通知，避免每次输入都重渲染配置页
  if (!previous || previous.dirty !== draft.dirty) emit()
}

export function unregisterDraft(key: string) {
  if (!drafts.has(key)) return
  drafts.delete(key)
  emit()
}

export function hasDirtyDrafts() {
  for (const draft of drafts.values()) if (draft.dirty) return true
  return false
}

export function subscribeDrafts(listener: Listener) {
  listeners.add(listener)
  return () => { listeners.delete(listener) }
}

// 提交所有已注册且已改动的草稿。返回失败明细，便于在页面上提示。
export async function commitDirtyDrafts(): Promise<string[]> {
  const failed: string[] = []
  for (const [key, draft] of Array.from(drafts.entries())) {
    if (!draft.dirty) continue
    try {
      await draft.commit(draft.value)
      const current = drafts.get(key)
      if (current) registerDraft(key, { ...current, dirty: false })
    } catch (reason) {
      failed.push(`${draft.label}：${reason instanceof Error ? reason.message : '保存失败'}`)
    }
  }
  return failed
}

// 面板内提交订阅增删等「立即生效」的操作时使用：写完顺手把该面板
// 的运行设置草稿标记为已保存，避免右上角再重复提交一次。
export function markDraftClean(key: string) {
  const draft = drafts.get(key)
  if (draft?.dirty) registerDraft(key, { ...draft, dirty: false })
}

function stable(value: unknown) {
  return JSON.stringify(value ?? null)
}

/**
 * 把面板的 settings 接入统一保存。
 * - baseline 是最后一次从服务端读到的值（订阅增删后要同步更新）
 * - value 是当前编辑中的值
 */
export function useDraftedSettings<T>(key: string, label: string, value: T | undefined, baseline: T | undefined, commit: (value: T) => Promise<unknown>) {
  const commitRef = useRef(commit)
  commitRef.current = commit
  const dirty = !!value && !!baseline && stable(value) !== stable(baseline)

  useEffect(() => {
    if (!value) return
    registerDraft(key, {
      label,
      value,
      dirty,
      commit: (next) => commitRef.current(next as T),
    })
    return () => unregisterDraft(key)
  }, [key, label, value, dirty])

  return dirty
}
