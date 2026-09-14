// Chromium background targets keep scheduled lookups from stealing the login tab.
const queues = new WeakMap()
export function newBackgroundPage(context) {
  const previous = queues.get(context) || Promise.resolve()
  const pending = previous.catch(() => {}).then(() => createBackgroundPage(context))
  queues.set(context, pending)
  return pending
}
async function createBackgroundPage(context) {
  const anchor = context.pages().find(page => !page.isClosed())
  if (!anchor) return context.newPage()
  const session = await context.newCDPSession(anchor)
  let targetId
  try {
    const { targetInfo: anchorInfo } = await session.send('Target.getTargetInfo')
    const pending = context.waitForEvent('page', { timeout: 10000 })
    // Attach a rejection handler even when target creation fails first.
    pending.catch(() => {})
    ;({ targetId } = await session.send('Target.createTarget', { url: 'about:blank', background: true, ...(anchorInfo.browserContextId ? { browserContextId: anchorInfo.browserContextId } : {}) }))
    const created = await pending
    const target = await context.newCDPSession(created)
    try {
      const { targetInfo } = await target.send('Target.getTargetInfo')
      if (targetInfo.targetId !== targetId) throw new Error('background target mismatch')
    } finally { await target.detach() }
    return created
  } catch (error) {
    if (targetId) await session.send('Target.closeTarget', { targetId }).catch(() => {})
    throw error
  } finally { await session.detach() }
}
