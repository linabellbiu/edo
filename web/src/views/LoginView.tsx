import { type FormEvent, useEffect, useMemo, useState } from 'react'
import axios from 'axios'
import { useLocation, useNavigate, useSearchParams } from 'react-router-dom'

import { useAuthStore } from '@/stores/auth'
import { getLoginProviders, type LoginProvider } from '@/api/client'

interface APIError {
  code?: string
  message?: string
  request_id?: string
}

function safeRedirect(value: string | null): string {
  return value?.startsWith('/') && !value.startsWith('//') && !value.includes('\\') ? value : '/'
}

export default function LoginView() {
  const login = useAuthStore((state) => state.login)
  const loginLDAP = useAuthStore((state) => state.loginLDAP)
  const [searchParams] = useSearchParams()
  const location = useLocation()
  const navigate = useNavigate()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [loading, setLoading] = useState(false)
  const [errorMessage, setErrorMessage] = useState('')
  const [providers, setProviders] = useState<LoginProvider[]>([])
  const [credentialProvider, setCredentialProvider] = useState('local')
  const unavailable = searchParams.get('reason') === 'unavailable'
  const ldapProviders = useMemo(() => providers.filter((provider) => provider.type === 'ldap'), [providers])
  const oauthProviders = useMemo(() => providers.filter((provider) => provider.type !== 'ldap'), [providers])

  useEffect(() => {
    void getLoginProviders().then(setProviders).catch(() => setProviders([]))
  }, [])

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!username.trim() || !password) {
      setErrorMessage('请输入用户名和密码。')
      return
    }

    setLoading(true)
    setErrorMessage('')
    try {
      if (credentialProvider === 'local') await login(username, password)
      else await loginLDAP(credentialProvider, username, password)
      navigate(safeRedirect(searchParams.get('redirect')), { replace: true })
    } catch (error) {
      if (axios.isAxiosError<APIError>(error)) {
        setErrorMessage(error.response?.data?.message || '登录服务暂时不可用，请稍后重试。')
      } else {
        setErrorMessage('登录服务暂时不可用，请稍后重试。')
      }
    } finally {
      setLoading(false)
    }
  }

  const externalErrorMessages: Record<string, string> = {
    cancelled: '你取消了外部账号授权。', expired: '登录请求已过期，请重新选择登录方式。',
    not_bound: '这个账号尚未绑定，请联系管理员。', email_unverified: '请先在身份平台验证邮箱。', failed: '外部登录失败，请重试。',
  }
  const externalError = searchParams.get('external_error')
  const returnTo = safeRedirect(searchParams.get('redirect'))

  return (
    <main className="login-page" key={location.key}>
      <section className="login-brand-panel">
        <div className="login-brand">
          <span className="brand-mark large">Z</span>
          <strong>ZRT</strong>
        </div>
        <div className="login-copy">
          <p className="eyebrow">持续交付，从这里开始</p>
          <h1>把代码、构建和发布，<br />清清楚楚地串起来。</h1>
          <p>用一套简单明了的流程，管理应用从代码变更到上线的每一步。</p>
        </div>
        <div className="login-foundation">
          <span>流程清晰</span><span>变更可追踪</span><span>失败可回滚</span>
        </div>
      </section>

      <section className="login-form-panel">
        <form className="login-form" onSubmit={submit}>
          <div>
            <p className="section-label">欢迎回来</p>
            <h2>登录 ZRT</h2>
            <p className="form-description">使用你的 ZRT 账户继续。</p>
          </div>

          {unavailable && (
            <div className="form-alert" role="alert">
              暂时无法读取登录状态，请确认 ZRT API、Redis 与数据库均已启动。
            </div>
          )}
          {errorMessage && <div className="form-alert error" role="alert">{errorMessage}</div>}
          {externalError && <div className="form-alert error" role="alert">{externalErrorMessages[externalError] || externalErrorMessages.failed}</div>}

          {ldapProviders.length > 0 && <div className="credential-tabs" role="tablist" aria-label="账号来源">
            <button className={credentialProvider === 'local' ? 'active' : ''} type="button" onClick={() => setCredentialProvider('local')}>ZRT 账号</button>
            {ldapProviders.map((provider) => <button className={credentialProvider === provider.id ? 'active' : ''} key={provider.id} type="button" onClick={() => setCredentialProvider(provider.id)}>{provider.display_name}</button>)}
          </div>}

          <label>
            <span>用户名</span>
            <input
              value={username}
              onChange={(event) => setUsername(event.target.value)}
              name="username"
              autoComplete="username"
              maxLength={32}
              autoFocus
              placeholder="请输入用户名"
            />
          </label>
          <label>
            <span>密码</span>
            <input
              value={password}
              onChange={(event) => setPassword(event.target.value)}
              name="password"
              type="password"
              autoComplete="current-password"
              maxLength={512}
              placeholder="请输入密码"
            />
          </label>
          <button className="login-button" type="submit" disabled={loading}>
            {loading ? '正在验证…' : '登录控制台'}
          </button>
          {oauthProviders.length > 0 && <div className="external-login">
            <div className="login-divider"><span>或者使用</span></div>
            <div className="external-login-grid">{oauthProviders.map((provider) => (
              <button key={provider.id} type="button" onClick={() => window.location.assign(`/api/v1/auth/oauth/${encodeURIComponent(provider.id)}/start?return_to=${encodeURIComponent(returnTo)}`)}>
                <span className={`provider-logo ${provider.type}`}>{provider.type === 'generic_oauth' ? 'O' : provider.display_name.slice(0, 1)}</span>{provider.display_name}
              </button>
            ))}</div>
          </div>}
        </form>
        <p className="login-footer">ZRT · 让交付更简单</p>
      </section>
    </main>
  )
}
