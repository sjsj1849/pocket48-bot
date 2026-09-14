import test from 'node:test'
import assert from 'node:assert/strict'
import { xSessionFromCookies, handleXPanel } from './x-session.mjs'

test('only current X login cookies are imported, X domain preferred', () => {
  const session = xSessionFromCookies([
    { domain: 'evilx.com', name: 'auth_token', value: 'wrong' },
    { domain: '.twitter.com', name: 'auth_token', value: 'legacy' },
    { domain: '.x.com', name: 'auth_token', value: 'current', expires: 200 },
    { domain: '.x.com', name: 'ct0', value: 'csrf', expires: -1 },
    { domain: '.x.com', name: 'unrelated', value: 'not-imported' },
  ], 100)
  assert.deepEqual(session.cookies.map(c => c.value), ['current', 'csrf'])
  assert.equal(xSessionFromCookies([{ domain: '.x.com', name: 'auth_token', value: 'expired', expires: 99 }, { domain: '.x.com', name: 'ct0', value: 'csrf' }], 100), null)
})

test('incomplete login cannot be synced and never opens a page', async () => {
  const result = await handleXPanel({ cookies: async () => [] }, 'sync')
  assert.match(result.error, /验证码/)
  assert.equal(result.session, undefined)
})

test('login opens a visible page and existing tab is reused', async () => {
  let destination = '', fronts = 0
  const page = { url: () => 'https://x.com/home', goto: async url => { destination = url }, bringToFront: async () => { fronts++ }, locator: () => ({ first: () => ({ waitFor: async () => {} }) }) }
  const result = await handleXPanel({ cookies: async () => [], pages: () => [page], newPage: () => { throw Error('unnecessary page') } }, 'open')
  assert(result.opened)
  assert.equal(destination, 'https://x.com/i/jf/onboarding/web')
  assert.equal(fronts, 2)
})
