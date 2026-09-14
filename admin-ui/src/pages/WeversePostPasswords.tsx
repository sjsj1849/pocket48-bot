import { useEffect, useState } from 'react'
import { api } from '../api'
import { PlatformField } from '../components/PlatformField'

type Post = { postId: string; url: string; verifiedAt: string }
export function WeversePostPasswords() {
  const [posts, setPosts] = useState<Post[]>([])
  const [url, setURL] = useState('')
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  useEffect(() => { const c = new AbortController(); api<{ posts: Post[] }>('weverse/post-passwords', { signal: c.signal }).then(r => setPosts(r.posts)).catch(e => { if (!c.signal.aborted) setError(e.message) }); return () => c.abort() }, [])
  async function change(remove?: string) {
    setBusy(true); setError(''); setMessage('')
    try {
      const r = await api<{ posts: Post[] }>(`weverse/post-passwords${remove ? `?postId=${encodeURIComponent(remove)}` : ''}`, { method: remove ? 'DELETE' : 'PUT', body: remove ? undefined : JSON.stringify({ url, password }) })
      setPosts(r.posts); setPassword(''); setMessage(remove ? '已移除帖子密码' : '密码验证通过并已保存，后续采集自动使用')
    } catch (e) { setError(e instanceof Error ? e.message : '操作失败') } finally { setBusy(false) }
  }
  return <section className="platform-section">
    <h3>私密帖密码</h3>
    <p className="muted">填写帖子链接与已知密码，也支持评论链接。验证成功后按帖子保存，后续采集、上下文和报表读取复用；此前的消息不会重复转发。会员权限仍需登录账号具备对应资格。</p>
    <PlatformField label="帖子链接"><input type="url" value={url} onChange={e => setURL(e.target.value)} placeholder="https://weverse.io/hearts2hearts/artist/…" /></PlatformField>
    <PlatformField label="帖子密码"><input type="password" autoComplete="new-password" value={password} onChange={e => setPassword(e.target.value)} /></PlatformField>
    {error && <p role="alert" className="inline-error">{error}</p>}{message && <p role="status" className="wv-notice">{message}</p>}
    <button className="primary-button" disabled={busy || !url || !password} onClick={() => void change()}>{busy ? '验证中…' : '验证并保存密码'}</button>
    {posts.map(post => <div className="platform-section" key={post.postId}><a href={post.url} target="_blank" rel="noreferrer">帖子 {post.postId}</a><p className="muted">验证通过：{new Date(post.verifiedAt).toLocaleString()}</p><button disabled={busy} onClick={() => void change(post.postId)}>移除密码</button></div>)}
    {!posts.length && <p className="muted">暂无已保存的帖子密码</p>}
  </section>
}
