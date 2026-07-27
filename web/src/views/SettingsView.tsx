import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'

import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'

interface ExternalGitWebhookSettings {
  enabled: boolean
  version: number
  path_template: string
  max_body_bytes: number
  providers: string[]
  events: string[]
}

const providerLabels: Record<string, string> = {
  generic: '普通 Git', github: 'GitHub', gitlab: 'GitLab', gitea: 'Gitea', gitee: 'Gitee',
}

const eventLabels: Record<string, string> = {
  branch_push: '分支 Push', tag_push: 'Tag Push', pull_request: 'PR / MR',
}

export default function SettingsView() {
  const user = useAuthStore((state) => state.user)
  const canManage = Boolean(user?.is_superuser || user?.permissions.includes('config.manage'))
  const [settings, setSettings] = useState<ExternalGitWebhookSettings | null>(null)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const response = await client.get<ExternalGitWebhookSettings>('/settings/external-git-webhook')
      setSettings(response.data)
    } catch (loadError) {
      setError(apiErrorMessage(loadError))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { void load() }, [load])

  async function toggle() {
    if (!settings || !canManage) return
    setSaving(true)
    setError('')
    setMessage('')
    try {
      const response = await client.put<ExternalGitWebhookSettings>('/settings/external-git-webhook', {
        enabled: !settings.enabled,
        expected_version: settings.version,
      })
      setSettings(response.data)
      setMessage(response.data.enabled ? '外部 Git Webhook API 已启用。' : '外部 Git Webhook API 已关闭。')
    } catch (saveError) {
      const saveMessage = apiErrorMessage(saveError)
      await load()
      setError(saveMessage)
    } finally {
      setSaving(false)
    }
  }

  return <section className="settings-page">
    <div className="page-heading">
      <div><span className="section-label">ZRT SETTINGS</span><h2>系统设置</h2><p>集中控制需要显式开放的外部接入能力。</p></div>
      <button className="refresh-button" type="button" disabled={loading || saving} onClick={() => void load()}>{loading ? '加载中…' : '刷新'}</button>
    </div>

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
          className={`feature-switch${settings?.enabled ? ' enabled' : ''}`}
          type="button"
          role="switch"
          aria-checked={settings?.enabled ?? false}
          disabled={loading || saving || !settings || !canManage}
          onClick={() => void toggle()}
        >
          <span aria-hidden="true" />
          {saving ? '保存中…' : settings?.enabled ? '已开启' : '已关闭'}
        </button>
      </div>

      <div className="feature-setting-details">
        <div><span>请求地址</span><code>{settings?.path_template ?? '/api/v1/webhooks/git/{repository_id}'}</code></div>
        <div><span>支持平台</span><p>{(settings?.providers ?? []).map((provider) => providerLabels[provider] || provider).join('、') || '普通 Git、GitHub、GitLab、Gitea、Gitee'}</p></div>
        <div><span>常见事件</span><p>{(settings?.events ?? []).map((event) => eventLabels[event] || event).join('、') || '分支 Push、Tag Push、PR / MR'}</p></div>
        <div><span>请求上限</span><p>{Math.round((settings?.max_body_bytes ?? 2 * 1024 * 1024) / 1024 / 1024)} MiB</p></div>
      </div>

      <div className="setting-security-note">
        <strong>安全条件</strong>
        <p>全局开关开启后，还必须在对应代码仓库中启用 Webhook。每次请求仍会按平台校验签名或 Token；关闭全局开关不会删除仓库密钥和历史投递记录。</p>
        <Link to="/repositories">配置代码仓库 →</Link>
      </div>
      {!canManage && <p className="settings-readonly-hint">当前账户只有查看权限，需要“配置管理”权限才能修改开关。</p>}
    </article>
  </section>
}
