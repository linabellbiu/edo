import { useCallback, useEffect, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'

import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'
import IdentityProvidersView from '@/views/IdentityProvidersView'

interface ExternalGitWebhookSettings {
  enabled: boolean
  version: number
  path_template: string
  max_body_bytes: number
  providers: string[]
  events: string[]
}

interface LoginLockoutSettings {
  enabled: boolean
  version: number
  max_failures: number
  window_seconds: number
}

const providerLabels: Record<string, string> = {
  generic: '普通 Git', github: 'GitHub', gitlab: 'GitLab', gitea: 'Gitea', gitee: 'Gitee',
}

const eventLabels: Record<string, string> = {
  branch_push: '分支 Push', tag_push: 'Tag Push', pull_request: 'PR / MR',
}

export default function SettingsView() {
  const user = useAuthStore((state) => state.user)
  const isSuperuser = Boolean(user?.is_superuser)
  const canReadGeneral = Boolean(isSuperuser || user?.permissions.includes('config.read'))
  const canReadIdentity = Boolean(isSuperuser || user?.permissions.includes('identity.read'))
  const canManage = Boolean(user?.is_superuser || user?.permissions.includes('config.manage'))
  const [searchParams, setSearchParams] = useSearchParams()
  const sections = [
    ...(canReadGeneral ? [{ id: 'general', label: '安全与接入' }] : []),
    ...(canReadIdentity ? [{ id: 'login-methods', label: '登录方式' }] : []),
  ]
  const requestedSection = searchParams.get('section') || 'general'
  const activeSection = sections.some((section) => section.id === requestedSection) ? requestedSection : sections[0]?.id
  const [webhookSettings, setWebhookSettings] = useState<ExternalGitWebhookSettings | null>(null)
  const [loginLockoutSettings, setLoginLockoutSettings] = useState<LoginLockoutSettings | null>(null)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState<'webhook' | 'login-lockout' | ''>('')
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')

  const load = useCallback(async () => {
    if (!canReadGeneral) {
      setLoading(false)
      return
    }
    setLoading(true)
    setError('')
    try {
      const [webhookResponse, loginLockoutResponse] = await Promise.all([
        client.get<ExternalGitWebhookSettings>('/settings/external-git-webhook'),
        client.get<LoginLockoutSettings>('/settings/login-lockout'),
      ])
      setWebhookSettings(webhookResponse.data)
      setLoginLockoutSettings(loginLockoutResponse.data)
    } catch (loadError) {
      setError(apiErrorMessage(loadError))
    } finally {
      setLoading(false)
    }
  }, [canReadGeneral])

  useEffect(() => { void load() }, [load])

  async function toggleWebhook() {
    if (!webhookSettings || !canManage) return
    setSaving('webhook')
    setError('')
    setMessage('')
    try {
      const response = await client.put<ExternalGitWebhookSettings>('/settings/external-git-webhook', {
        enabled: !webhookSettings.enabled,
        expected_version: webhookSettings.version,
      })
      setWebhookSettings(response.data)
      setMessage(response.data.enabled ? '外部 Git Webhook API 已启用。' : '外部 Git Webhook API 已关闭。')
    } catch (saveError) {
      const saveMessage = apiErrorMessage(saveError)
      await load()
      setError(saveMessage)
    } finally {
      setSaving('')
    }
  }

  async function toggleLoginLockout() {
    if (!loginLockoutSettings || !canManage) return
    setSaving('login-lockout')
    setError('')
    setMessage('')
    try {
      const response = await client.put<LoginLockoutSettings>('/settings/login-lockout', {
        enabled: !loginLockoutSettings.enabled,
        expected_version: loginLockoutSettings.version,
      })
      setLoginLockoutSettings(response.data)
      setMessage(response.data.enabled ? '登录失败锁定已启用。' : '登录失败锁定已关闭。')
    } catch (saveError) {
      const saveMessage = apiErrorMessage(saveError)
      await load()
      setError(saveMessage)
    } finally {
      setSaving('')
    }
  }

  function selectSection(section: string) {
    const next = new URLSearchParams(searchParams)
    if (section === 'general') next.delete('section')
    else next.set('section', section)
    setSearchParams(next, { replace: true })
  }

  return <section className="settings-page">
    <div className="page-heading">
      <div><span className="section-label">ZRT SETTINGS</span><h2>系统设置</h2><p>集中管理登录方式、全局安全和外部接入能力。</p></div>
      {activeSection === 'general' && <button className="refresh-button" type="button" disabled={loading || Boolean(saving)} onClick={() => void load()}>{loading ? '加载中…' : '刷新'}</button>}
    </div>

    <div className="tab-bar settings-tabs" role="tablist" aria-label="系统设置分类">
      {sections.map((section) => <button
        className={activeSection === section.id ? 'active' : ''}
        type="button"
        role="tab"
        aria-selected={activeSection === section.id}
        key={section.id}
        onClick={() => selectSection(section.id)}
      >{section.label}</button>)}
    </div>

    {activeSection === 'general' && <>
      {error && <div className="form-alert error system-alert">{error}</div>}
      {message && <div className="form-alert success system-alert">{message}</div>}

      <article className="feature-setting-card">
        <div className="feature-setting-head">
          <div>
            <span className="setting-category">外部接入</span>
            <h3>Git Webhook API</h3>
            <p>允许外部 Git 平台把代码事件发送给 ZRT，并沿用仓库签名校验、投递去重和任务队列。</p>
          </div>
          <button
            className={`feature-switch${webhookSettings?.enabled ? ' enabled' : ''}`}
            type="button"
            role="switch"
            aria-checked={webhookSettings?.enabled ?? false}
            disabled={loading || Boolean(saving) || !webhookSettings || !canManage}
            onClick={() => void toggleWebhook()}
          >
            <span aria-hidden="true" />
            {saving === 'webhook' ? '保存中…' : webhookSettings?.enabled ? '已开启' : '已关闭'}
          </button>
        </div>

        <div className="feature-setting-details">
          <div><span>请求地址</span><code>{webhookSettings?.path_template ?? '/api/v1/webhooks/git/{repository_id}'}</code></div>
          <div><span>支持平台</span><p>{(webhookSettings?.providers ?? []).map((provider) => providerLabels[provider] || provider).join('、') || '普通 Git、GitHub、GitLab、Gitea、Gitee'}</p></div>
          <div><span>常见事件</span><p>{(webhookSettings?.events ?? []).map((event) => eventLabels[event] || event).join('、') || '分支 Push、Tag Push、PR / MR'}</p></div>
          <div><span>请求上限</span><p>{Math.round((webhookSettings?.max_body_bytes ?? 2 * 1024 * 1024) / 1024 / 1024)} MiB</p></div>
        </div>

        <div className="setting-security-note">
          <strong>安全条件</strong>
          <p>全局开关开启后，还必须在对应代码仓库中启用 Webhook。每次请求仍会按平台校验签名或 Token；关闭全局开关不会删除仓库密钥和历史投递记录。</p>
          <Link to="/repositories">配置代码仓库 →</Link>
        </div>
        {!canManage && <p className="settings-readonly-hint">当前账户只有查看权限，需要“配置管理”权限才能修改开关。</p>}
      </article>

      <article className="feature-setting-card">
        <div className="feature-setting-head">
          <div>
            <span className="setting-category">登录安全</span>
            <h3>登录失败锁定</h3>
            <p>开启后，同一用户名和来源地址在有限时间内连续登录失败会被暂时锁定。新安装和升级后的默认状态均为关闭。</p>
          </div>
          <button
            className={`feature-switch${loginLockoutSettings?.enabled ? ' enabled' : ''}`}
            type="button"
            role="switch"
            aria-checked={loginLockoutSettings?.enabled ?? false}
            disabled={loading || Boolean(saving) || !loginLockoutSettings || !canManage}
            onClick={() => void toggleLoginLockout()}
          >
            <span aria-hidden="true" />
            {saving === 'login-lockout' ? '保存中…' : loginLockoutSettings?.enabled ? '已开启' : '已关闭'}
          </button>
        </div>

        <div className="feature-setting-details">
          <div><span>默认状态</span><p>关闭</p></div>
          <div><span>触发阈值</span><p>{loginLockoutSettings?.max_failures ?? 5} 次失败</p></div>
          <div><span>锁定时间</span><p>{Math.max(1, Math.round((loginLockoutSettings?.window_seconds ?? 900) / 60))} 分钟</p></div>
          <div><span>计数维度</span><p>用户名与来源地址</p></div>
        </div>

        <div className="setting-security-note">
          <strong>行为说明</strong>
          <p>开关变化时会清理已有的 Redis 登录失败计数，避免旧计数影响新策略；不会强制退出已经登录的用户，也不会修改账户密码或启用状态。</p>
        </div>
        {!canManage && <p className="settings-readonly-hint">当前账户只有查看权限，需要“配置管理”权限才能修改开关。</p>}
      </article>
    </>}

    {activeSection === 'login-methods' && canReadIdentity && <IdentityProvidersView embedded />}
    {!activeSection && <div className="empty-state">当前账户没有可查看的系统设置</div>}
  </section>
}
