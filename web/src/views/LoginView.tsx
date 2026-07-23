import { type FormEvent, useState } from 'react'
import axios from 'axios'
import { useLocation, useNavigate, useSearchParams } from 'react-router-dom'

import { useAuthStore } from '@/stores/auth'

interface APIError {
  code?: string
  message?: string
  request_id?: string
}

function safeRedirect(value: string | null): string {
  return value?.startsWith('/') && !value.startsWith('//') ? value : '/'
}

export default function LoginView() {
  const login = useAuthStore((state) => state.login)
  const [searchParams] = useSearchParams()
  const location = useLocation()
  const navigate = useNavigate()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [loading, setLoading] = useState(false)
  const [errorMessage, setErrorMessage] = useState('')
  const unavailable = searchParams.get('reason') === 'unavailable'

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!username.trim() || !password) {
      setErrorMessage('请输入用户名和密码。')
      return
    }

    setLoading(true)
    setErrorMessage('')
    try {
      await login(username, password)
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

  return (
    <main className="login-page" key={location.key}>
      <section className="login-brand-panel">
        <div className="login-brand">
          <span className="brand-mark large">Z</span>
          <strong>ZRT</strong>
        </div>
        <div className="login-copy">
          <p className="eyebrow">CONTAINER OPERATIONS PLATFORM</p>
          <h1>让每一次发布<br />都有依据，也有退路。</h1>
          <p>面向 Docker 与 Kubernetes 的发布、审计和运行控制平台。</p>
        </div>
        <div className="login-foundation">
          <span>Go</span><span>NATS JetStream</span><span>Redis</span><span>Kubernetes</span>
        </div>
      </section>

      <section className="login-form-panel">
        <form className="login-form" onSubmit={submit}>
          <div>
            <p className="section-label">SECURE ACCESS</p>
            <h2>登录 ZRT</h2>
            <p className="form-description">会话凭据仅保存在安全 Cookie 中，不写入浏览器本地存储。</p>
          </div>

          {unavailable && (
            <div className="form-alert" role="alert">
              暂时无法读取登录状态，请确认 ZRT API、Redis 与数据库均已启动。
            </div>
          )}
          {errorMessage && <div className="form-alert error" role="alert">{errorMessage}</div>}

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
        </form>
        <p className="login-footer">ZRT · 所有关键操作均应经过权限校验与审计</p>
      </section>
    </main>
  )
}
