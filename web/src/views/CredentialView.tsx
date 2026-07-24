import { type FormEvent, useCallback, useEffect, useState } from 'react'

import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'

interface GitCredential {
  id: string
  name: string
  provider: string
  auth_type: 'token' | 'ssh_key'
  username?: string
  secret_hint: string
  created_at: string
  updated_at: string
}

const emptyForm = { name: '', provider: 'github', auth_type: 'token' as 'token' | 'ssh_key', username: '', secret: '' }

function providerLabel(provider: string) {
  return ({ github: 'GitHub', gitlab: 'GitLab', gitea: 'Gitea', gitee: 'Gitee', generic: '普通 Git' } as Record<string, string>)[provider] || provider
}

export default function CredentialView() {
  const user = useAuthStore((state) => state.user)
  const canManage = Boolean(user?.is_superuser || user?.permissions.includes('credential.manage'))
  const [items, setItems] = useState<GitCredential[]>([])
  const [form, setForm] = useState(emptyForm)
  const [editingID, setEditingID] = useState('')
  const [formOpen, setFormOpen] = useState(false)
  const [revealed, setRevealed] = useState<Record<string, string>>({})
  const [loading, setLoading] = useState(true)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')

  const refresh = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const response = await client.get<{ credentials: GitCredential[] }>('/git-credentials')
      setItems(response.data.credentials)
    } catch (loadError) {
      setError(apiErrorMessage(loadError))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { void refresh() }, [refresh])

  function closeForm() {
    setFormOpen(false)
    setEditingID('')
    setForm(emptyForm)
  }

  function edit(item: GitCredential) {
    setEditingID(item.id)
    setForm({ name: item.name, provider: item.provider, auth_type: item.auth_type, username: item.username || '', secret: '' })
    setFormOpen(true)
    window.scrollTo({ top: 0, behavior: 'smooth' })
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setSubmitting(true)
    setError('')
    try {
      const payload = { ...form, secret: form.secret || null }
      if (editingID) await client.put(`/git-credentials/${editingID}`, payload)
      else await client.post('/git-credentials', payload)
      closeForm()
      await refresh()
    } catch (submitError) {
      setError(apiErrorMessage(submitError))
    } finally {
      setSubmitting(false)
    }
  }

  async function reveal(item: GitCredential) {
    if (revealed[item.id]) {
      setRevealed((current) => { const next = { ...current }; delete next[item.id]; return next })
      return
    }
    setError('')
    try {
      const response = await client.get<{ secret: string }>(`/git-credentials/${item.id}/secret`)
      setRevealed((current) => ({ ...current, [item.id]: response.data.secret }))
    } catch (revealError) {
      setError(apiErrorMessage(revealError))
    }
  }

  async function remove(item: GitCredential) {
    if (!window.confirm(`确认删除令牌“${item.name}”？`)) return
    setError('')
    try {
      await client.delete(`/git-credentials/${item.id}`)
      await refresh()
    } catch (deleteError) {
      setError(apiErrorMessage(deleteError))
    }
  }

  return <section className="credential-page page-enter">
    <div className="page-heading modern-heading">
      <div><span className="section-label">个人安全空间</span><h2>我的 Git 令牌</h2><p>每个账户只能查看和管理自己保存的令牌，仓库只保存引用关系。</p></div>
      <div className="heading-actions"><button className="icon-button" type="button" onClick={() => void refresh()} disabled={loading}>↻</button>{canManage && <button className="primary-button" type="button" onClick={() => setFormOpen((value) => !value)}>＋ 保存令牌</button>}</div>
    </div>
    {error && <div className="form-alert error system-alert" role="alert">{error}</div>}
    {formOpen && <form className="create-sheet credential-sheet" onSubmit={(event) => void submit(event)}>
      <div className="sheet-header"><div><h3>{editingID ? '修改 Git 令牌' : '保存 Git 令牌'}</h3><p>令牌使用 ZRT 主密钥加密，只有当前用户可以通过界面读取。</p></div><button type="button" onClick={closeForm}>×</button></div>
      <div className="form-grid">
        <label>名称<input required maxLength={128} value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} placeholder="例如：GitHub 生产账号" /></label>
        <label>平台<select value={form.provider} onChange={(event) => setForm({ ...form, provider: event.target.value })}><option value="github">GitHub</option><option value="gitlab">GitLab</option><option value="gitea">Gitea</option><option value="gitee">Gitee</option><option value="generic">普通 Git</option></select></label>
        <label>类型<select value={form.auth_type} onChange={(event) => setForm({ ...form, auth_type: event.target.value as 'token' | 'ssh_key' })}><option value="token">访问令牌</option><option value="ssh_key">SSH 私钥</option></select></label>
        <label>用户名<input maxLength={255} value={form.username} onChange={(event) => setForm({ ...form, username: event.target.value })} placeholder="部分平台可留空" /></label>
        <label className="span-2">{form.auth_type === 'token' ? '令牌' : 'SSH 私钥'}<textarea required={!editingID} rows={form.auth_type === 'ssh_key' ? 8 : 3} value={form.secret} onChange={(event) => setForm({ ...form, secret: event.target.value })} placeholder={editingID ? '留空表示保持原值' : '请输入凭据内容'} /></label>
      </div>
      <div className="form-actions"><button className="secondary-button" type="button" onClick={closeForm}>取消</button><button className="primary-button" type="submit" disabled={submitting}>{submitting ? '保存中…' : '保存'}</button></div>
    </form>}
    <div className="credential-grid">{items.map((item) => <article className="credential-card" key={item.id}>
      <div className="credential-card-head"><span className="credential-provider">{providerLabel(item.provider).slice(0, 2)}</span><div><h3>{item.name}</h3><p>{providerLabel(item.provider)} · {item.auth_type === 'token' ? '访问令牌' : 'SSH 私钥'}</p></div></div>
      <div className="credential-secret"><code>{revealed[item.id] || item.secret_hint}</code><button type="button" onClick={() => void reveal(item)}>{revealed[item.id] ? '隐藏' : '查看'}</button></div>
      <div className="meta-row"><span>{item.username || '未设置用户名'}</span><span>{new Date(item.updated_at).toLocaleString('zh-CN', { hour12: false })}</span></div>
      {canManage && <div className="card-actions"><button type="button" onClick={() => edit(item)}>编辑</button><button className="danger-action" type="button" onClick={() => void remove(item)}>删除</button></div>}
    </article>)}{!loading && items.length === 0 && <div className="empty-state modern-empty"><span className="empty-icon">⌁</span><h3>还没有保存令牌</h3><p>保存后可以在创建代码仓库时直接选择。</p></div>}</div>
  </section>
}
