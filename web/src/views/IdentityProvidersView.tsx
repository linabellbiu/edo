import { type FormEvent, useEffect, useState } from 'react'

import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'

type ProviderType = 'ldap' | 'generic_oauth' | 'feishu' | 'google' | 'github' | 'gitlab'

interface Provider {
  id: string
  type: ProviderType
  name: string
  display_name: string
  is_active: boolean
  auto_create: boolean
  default_role_id: string
  client_id: string
  authorization_url: string
  token_url: string
  user_info_url: string
  redirect_url: string
  scopes: string
  subject_field: string
  username_field: string
  nickname_field: string
  email_field: string
  email_verified_field: string
  ldap_url: string
  ldap_base_dn: string
  ldap_bind_dn: string
  ldap_user_filter: string
  ldap_username_attribute: string
  ldap_nickname_attribute: string
  ldap_email_attribute: string
  ldap_start_tls: boolean
  allow_insecure: boolean
  has_client_secret: boolean
  has_bind_password: boolean
}

interface Role { id: string; display_name: string }

interface ProviderForm extends Omit<Provider, 'id' | 'has_client_secret' | 'has_bind_password'> {
  client_secret: string
  ldap_bind_password: string
}

const typeLabels: Record<ProviderType, string> = {
  ldap: 'LDAP', generic_oauth: '通用 OAuth', feishu: '飞书', google: 'Google', github: 'GitHub', gitlab: 'GitLab',
}

const presets: Record<ProviderType, Partial<ProviderForm>> = {
  ldap: {
    display_name: '企业 LDAP', ldap_url: 'ldaps://ldap.example.com:636', ldap_user_filter: '(uid={username})',
    ldap_username_attribute: 'uid', ldap_nickname_attribute: 'displayName', ldap_email_attribute: 'mail',
  },
  generic_oauth: { display_name: '企业账号', scopes: 'openid profile email', subject_field: 'sub', username_field: 'preferred_username', nickname_field: 'name', email_field: 'email' },
  feishu: {
    display_name: '飞书', authorization_url: 'https://accounts.feishu.cn/open-apis/authen/v1/authorize',
    token_url: 'https://open.feishu.cn/open-apis/authen/v2/oauth/token', user_info_url: 'https://open.feishu.cn/open-apis/authen/v1/user_info',
    subject_field: 'open_id', username_field: 'email', nickname_field: 'name', email_field: 'email',
  },
  google: {
    display_name: 'Google', authorization_url: 'https://accounts.google.com/o/oauth2/v2/auth', token_url: 'https://oauth2.googleapis.com/token',
    user_info_url: 'https://openidconnect.googleapis.com/v1/userinfo', scopes: 'openid profile email', subject_field: 'sub',
    username_field: 'email', nickname_field: 'name', email_field: 'email', email_verified_field: 'email_verified',
  },
  github: {
    display_name: 'GitHub', authorization_url: 'https://github.com/login/oauth/authorize', token_url: 'https://github.com/login/oauth/access_token',
    user_info_url: 'https://api.github.com/user', scopes: 'read:user user:email', subject_field: 'id', username_field: 'login', nickname_field: 'name', email_field: 'email',
  },
  gitlab: {
    display_name: 'GitLab', authorization_url: 'https://gitlab.com/oauth/authorize', token_url: 'https://gitlab.com/oauth/token',
    user_info_url: 'https://gitlab.com/api/v4/user', scopes: 'read_user', subject_field: 'id', username_field: 'username', nickname_field: 'name', email_field: 'email',
  },
}

function emptyForm(type: ProviderType = 'ldap'): ProviderForm {
	const name = type === 'generic_oauth' ? 'company_oauth' : type
  return {
    type, name, display_name: '', is_active: true, auto_create: false, default_role_id: '',
    client_id: '', client_secret: '', authorization_url: '', token_url: '', user_info_url: '', redirect_url: `${window.location.origin}/api/v1/auth/oauth/${name}/callback`, scopes: '',
    subject_field: '', username_field: '', nickname_field: '', email_field: '', email_verified_field: '',
    ldap_url: '', ldap_base_dn: '', ldap_bind_dn: '', ldap_bind_password: '', ldap_user_filter: '', ldap_username_attribute: '',
    ldap_nickname_attribute: '', ldap_email_attribute: '', ldap_start_tls: false, allow_insecure: type !== 'ldap' && window.location.protocol === 'http:',
    ...presets[type],
  }
}

function toForm(provider: Provider): ProviderForm {
  return { ...provider, client_secret: '', ldap_bind_password: '' }
}

export default function IdentityProvidersView({ embedded = false }: { embedded?: boolean }) {
  const canManage = useAuthStore((state) => Boolean(state.user?.is_superuser || state.user?.permissions.includes('identity.manage')))
  const [providers, setProviders] = useState<Provider[]>([])
  const [roles, setRoles] = useState<Role[]>([])
  const [form, setForm] = useState<ProviderForm>(() => emptyForm())
  const [editingID, setEditingID] = useState('')
  const [open, setOpen] = useState(false)
  const [advanced, setAdvanced] = useState(false)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  async function refresh() {
    setLoading(true)
    setError('')
    try {
      const [providerResponse, roleResponse] = await Promise.all([
        client.get<{ providers: Provider[] }>('/identity-providers'),
        client.get<{ roles: Role[] }>('/roles').catch(() => ({ data: { roles: [] as Role[] } })),
      ])
      setProviders(providerResponse.data.providers)
      setRoles(roleResponse.data.roles)
    } catch (loadError) {
      setError(apiErrorMessage(loadError))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { void refresh() }, [])

  function changeType(type: ProviderType) {
    setForm(emptyForm(type))
    setAdvanced(type === 'generic_oauth')
  }

  function startCreate() {
    setEditingID('')
    setForm(emptyForm())
    setAdvanced(false)
    setOpen(true)
  }

  function startEdit(provider: Provider) {
    setEditingID(provider.id)
    setForm(toForm(provider))
    setAdvanced(provider.type === 'generic_oauth')
    setOpen(true)
    window.scrollTo({ top: 0, behavior: 'smooth' })
  }

  async function save(event: FormEvent) {
    event.preventDefault()
    setSaving(true)
    setError('')
    try {
      const payload = {
        ...form,
        client_secret: form.client_secret || undefined,
        ldap_bind_password: form.ldap_bind_password || undefined,
      }
      if (editingID) await client.put(`/identity-providers/${editingID}`, payload)
      else await client.post('/identity-providers', payload)
      setOpen(false)
      await refresh()
    } catch (saveError) {
      setError(apiErrorMessage(saveError))
    } finally {
      setSaving(false)
    }
  }

  async function toggle(provider: Provider) {
    setError('')
    try {
      await client.patch(`/identity-providers/${provider.id}/status`, { is_active: !provider.is_active })
      await refresh()
    } catch (toggleError) {
      setError(apiErrorMessage(toggleError))
    }
  }

  const field = (key: keyof ProviderForm, value: string | boolean) => setForm((current) => ({ ...current, [key]: value }))
  const isLDAP = form.type === 'ldap'

  return (
    <section className={`${embedded ? 'settings-identity-section' : 'management-page'} identity-page`}>
      <div className="page-heading">
        <div><span className="section-label">登录安全</span><h2>登录方式</h2><p>接入企业目录或常用账号，让团队使用熟悉的身份登录 ZRT。</p></div>
        {canManage && <button className="primary-button" type="button" onClick={startCreate}>添加登录方式</button>}
      </div>
      {error && <div className="form-alert error system-alert">{error}</div>}

      {open && (
        <form className="create-sheet identity-form" onSubmit={save}>
          <div className="sheet-header">
            <div><h3>{editingID ? '编辑登录方式' : '添加登录方式'}</h3><p>带“密钥”字样的内容会加密保存，保存后不再显示。</p></div>
            <button type="button" aria-label="关闭" onClick={() => setOpen(false)}>×</button>
          </div>
          {!editingID && <div className="provider-type-grid">
            {(Object.keys(typeLabels) as ProviderType[]).map((type) => (
              <button key={type} className={form.type === type ? 'selected' : ''} type="button" onClick={() => changeType(type)}>
                <span className={`provider-logo ${type}`}>{type === 'generic_oauth' ? 'O' : typeLabels[type].slice(0, 1)}</span>
                <strong>{typeLabels[type]}</strong>
              </button>
            ))}
          </div>}

          <div className="form-grid identity-basics">
            <label><span>显示名称</span><input value={form.display_name} maxLength={64} required onChange={(event) => field('display_name', event.target.value)} placeholder="例如：公司账号" /></label>
            <label><span>内部标识</span><input value={form.name} maxLength={64} required disabled={Boolean(editingID)} onChange={(event) => setForm((current) => ({ ...current, name: event.target.value, redirect_url: `${window.location.origin}/api/v1/auth/oauth/${event.target.value}/callback` }))} placeholder="company_login" /></label>
            <label><span>首次登录后的角色</span><select value={form.default_role_id} onChange={(event) => field('default_role_id', event.target.value)}><option value="">不自动分配角色</option>{roles.map((role) => <option key={role.id} value={role.id}>{role.display_name}</option>)}</select></label>
            <div className="identity-checks">
              <label className="simple-check"><input type="checkbox" checked={form.is_active} onChange={(event) => field('is_active', event.target.checked)} />立即启用</label>
              <label className="simple-check"><input type="checkbox" checked={form.auto_create} onChange={(event) => field('auto_create', event.target.checked)} />首次登录自动创建用户</label>
            </div>
          </div>

          {isLDAP ? <>
            <div className="form-block"><span className="form-block-title">LDAP 连接</span><div className="form-grid">
              <label><span>服务器地址</span><input value={form.ldap_url} required onChange={(event) => field('ldap_url', event.target.value)} placeholder="ldaps://ldap.example.com:636" /></label>
              <label><span>用户所在目录</span><input value={form.ldap_base_dn} required onChange={(event) => field('ldap_base_dn', event.target.value)} placeholder="ou=people,dc=example,dc=com" /></label>
              <label><span>查询服务账号 DN</span><input value={form.ldap_bind_dn} onChange={(event) => field('ldap_bind_dn', event.target.value)} placeholder="cn=zrt,ou=service,dc=example,dc=com" /></label>
              <label><span>查询服务账号密码</span><input type="password" value={form.ldap_bind_password} onChange={(event) => field('ldap_bind_password', event.target.value)} placeholder={editingID ? '留空表示不修改' : '请输入密钥'} /></label>
              <label className="span-2"><span>用户查询条件</span><input value={form.ldap_user_filter} required onChange={(event) => field('ldap_user_filter', event.target.value)} placeholder="(&(objectClass=person)(uid={username}))" /></label>
            </div></div>
            <div className="form-block"><span className="form-block-title">用户字段</span><div className="form-grid">
              <label><span>用户名字段</span><input value={form.ldap_username_attribute} required onChange={(event) => field('ldap_username_attribute', event.target.value)} /></label>
              <label><span>姓名字段</span><input value={form.ldap_nickname_attribute} onChange={(event) => field('ldap_nickname_attribute', event.target.value)} /></label>
              <label><span>邮箱字段</span><input value={form.ldap_email_attribute} onChange={(event) => field('ldap_email_attribute', event.target.value)} /></label>
              <div className="identity-checks"><label className="simple-check"><input type="checkbox" checked={form.ldap_start_tls} onChange={(event) => field('ldap_start_tls', event.target.checked)} />使用 StartTLS</label><label className="simple-check warning-check"><input type="checkbox" checked={form.allow_insecure} onChange={(event) => field('allow_insecure', event.target.checked)} />允许明文 LDAP</label></div>
            </div></div>
          </> : <>
            <div className="form-block"><span className="form-block-title">应用凭据</span><div className="form-grid">
              <label><span>{form.type === 'feishu' ? 'App ID' : 'Client ID'}</span><input value={form.client_id} required onChange={(event) => field('client_id', event.target.value)} /></label>
              <label><span>{form.type === 'feishu' ? 'App Secret' : 'Client Secret'}</span><input type="password" value={form.client_secret} onChange={(event) => field('client_secret', event.target.value)} placeholder={editingID ? '留空表示不修改' : '请输入密钥'} /></label>
              <label className="span-2"><span>ZRT 回调地址</span><input value={form.redirect_url} required onChange={(event) => field('redirect_url', event.target.value)} placeholder="https://zrt.example.com/api/v1/auth/oauth/登录方式ID/callback" /></label>
            </div><p className="field-help">请把这条回调地址原样填写到身份平台，二者必须完全一致。</p></div>
            <div className="form-block">
              <button className="advanced-toggle" type="button" onClick={() => setAdvanced((value) => !value)}>{advanced ? '收起高级设置' : '查看高级设置'}</button>
              {advanced && <div className="form-grid advanced-fields">
                <label className="span-2"><span>授权地址</span><input value={form.authorization_url} required onChange={(event) => field('authorization_url', event.target.value)} /></label>
                <label className="span-2"><span>换取令牌地址</span><input value={form.token_url} required onChange={(event) => field('token_url', event.target.value)} /></label>
                <label className="span-2"><span>用户信息地址</span><input value={form.user_info_url} required onChange={(event) => field('user_info_url', event.target.value)} /></label>
                <label className="span-2"><span>权限范围</span><input value={form.scopes} onChange={(event) => field('scopes', event.target.value)} placeholder="openid profile email" /></label>
                <label><span>用户唯一 ID 字段</span><input value={form.subject_field} required onChange={(event) => field('subject_field', event.target.value)} /></label>
                <label><span>用户名字段</span><input value={form.username_field} required onChange={(event) => field('username_field', event.target.value)} /></label>
                <label><span>姓名字段</span><input value={form.nickname_field} onChange={(event) => field('nickname_field', event.target.value)} /></label>
                <label><span>邮箱字段</span><input value={form.email_field} onChange={(event) => field('email_field', event.target.value)} /></label>
                <label><span>邮箱已验证字段</span><input value={form.email_verified_field} onChange={(event) => field('email_verified_field', event.target.value)} /></label>
                <label className="simple-check warning-check"><input type="checkbox" checked={form.allow_insecure} onChange={(event) => field('allow_insecure', event.target.checked)} />允许 HTTP（仅限开发环境）</label>
              </div>}
            </div>
          </>}
          <div className="form-actions"><button className="secondary-button" type="button" onClick={() => setOpen(false)}>取消</button><button className="primary-button" disabled={saving} type="submit">{saving ? '正在保存…' : '保存登录方式'}</button></div>
        </form>
      )}

      <div className="identity-provider-list">
        {loading ? <div className="modern-empty"><p>正在读取登录方式…</p></div> : providers.length === 0 ? <div className="modern-empty"><span className="empty-icon">⌁</span><h3>还没有外部登录方式</h3><p>本地 admin 账号仍可正常登录。</p></div> : providers.map((provider) => (
          <article className="identity-provider-card" key={provider.id}>
            <span className={`provider-logo ${provider.type}`}>{provider.type === 'generic_oauth' ? 'O' : typeLabels[provider.type].slice(0, 1)}</span>
            <div className="provider-summary"><div><h3>{provider.display_name}</h3><p>{typeLabels[provider.type]} · {provider.auto_create ? '自动创建用户' : '仅限已绑定用户'}</p></div>
              {provider.type !== 'ldap' && <code>{provider.redirect_url}</code>}
              {provider.type === 'ldap' && <code>{provider.ldap_url}</code>}
            </div>
            <span className={`status-pill ${provider.is_active ? 'status-ready' : ''}`}>{provider.is_active ? '已启用' : '已停用'}</span>
            {canManage && <div className="card-actions"><button type="button" onClick={() => startEdit(provider)}>编辑</button><button type="button" onClick={() => void toggle(provider)}>{provider.is_active ? '停用' : '启用'}</button></div>}
          </article>
        ))}
      </div>
    </section>
  )
}
