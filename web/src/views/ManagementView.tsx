import { useCallback, useEffect, useMemo, useState } from 'react'

import { apiErrorMessage, getResources, postResource, type ResourceRecord } from '@/api/resources'
import JsonCreatePanel from '@/components/JsonCreatePanel'
import ResourceTable, { type ResourceColumn } from '@/components/ResourceTable'
import { useAuthStore } from '@/stores/auth'

interface Group {
  id: string
  title: string
  description: string
  endpoint: string
  responseKey: string
  permission: string
  columns: ResourceColumn[]
  create?: { title: string; permission: string; example: object }
}

const repositoryGroups: Group[] = [{
  id: 'repositories', title: '代码仓库', description: '统一接入 Git、GitLab、Gitea、GitHub 与 Gitee。',
  endpoint: '/repositories', responseKey: 'repositories', permission: 'repository.read',
  columns: [
    { key: 'name', label: '名称' }, { key: 'provider', label: '平台' }, { key: 'clone_url', label: 'Clone URL' },
    { key: 'default_branch', label: '默认分支' }, { key: 'webhook_enabled', label: 'Webhook' }, { key: 'is_active', label: '状态' },
  ],
  create: {
    title: '代码仓库', permission: 'repository.manage',
    example: {
      name: 'production-api', provider: 'github', clone_url: 'https://github.com/org/repo.git',
      default_branch: 'main', auth_type: 'none', username: '', credential: null,
      webhook_enabled: true, regenerate_webhook: false, allow_insecure_http: false,
    },
  },
}]

const operationGroups: Group[] = [
  {
    id: 'tasks', title: '任务中心', description: '查看有限重试、失败原因和人工操作。',
    endpoint: '/tasks', responseKey: 'tasks', permission: 'task.read',
    columns: [
      { key: 'kind', label: '类型' }, { key: 'status', label: '状态' }, { key: 'attempt', label: '次数' },
      { key: 'max_attempts', label: '上限' }, { key: 'error_message', label: '提示' }, { key: 'created_at', label: '创建时间' },
    ],
  },
  {
    id: 'monitor', title: 'HTTP 监控', description: '连续失败/恢复达到阈值后触发通知。',
    endpoint: '/monitor-rules', responseKey: 'rules', permission: 'monitor.read',
    columns: [
      { key: 'name', label: '名称' }, { key: 'endpoint', label: '目标' }, { key: 'status', label: '状态' },
      { key: 'interval_seconds', label: '间隔(秒)' }, { key: 'consecutive_failures', label: '连续失败' }, { key: 'is_active', label: '启用' },
    ],
    create: {
      title: '监控规则', permission: 'monitor.manage',
      example: {
        name: 'production-api', endpoint: 'https://service.example.com/health', method: 'GET',
        expected_status_min: 200, expected_status_max: 299, timeout_seconds: 5, interval_seconds: 60,
        failure_threshold: 3, recovery_threshold: 2, notification_channel_id: '', allow_http: false,
      },
    },
  },
  {
    id: 'channels', title: '通知渠道', description: 'Webhook 地址和 Token 始终加密保存。',
    endpoint: '/notification-channels', responseKey: 'channels', permission: 'notification.read',
    columns: [
      { key: 'name', label: '名称' }, { key: 'type', label: '类型' }, { key: 'has_token', label: 'Token' },
      { key: 'is_active', label: '启用' }, { key: 'updated_at', label: '更新时间' },
    ],
    create: {
      title: '通知渠道', permission: 'notification.manage',
      example: { name: 'operations-alerts', type: 'webhook', endpoint: 'https://notify.example.com/zrt', token: null, allow_http: false },
    },
  },
  {
    id: 'notifications', title: '通知记录', description: '查看发送次数、最终状态和安全错误提示。',
    endpoint: '/notifications', responseKey: 'notifications', permission: 'notification.read',
    columns: [
      { key: 'title', label: '标题' }, { key: 'severity', label: '级别' }, { key: 'status', label: '状态' },
      { key: 'attempts', label: '尝试次数' }, { key: 'error_message', label: '提示' }, { key: 'created_at', label: '创建时间' },
    ],
  },
  {
    id: 'schedules', title: '定时任务', description: '标准五段 Cron，只执行白名单内部动作。',
    endpoint: '/schedules', responseKey: 'schedules', permission: 'scheduler.read',
    columns: [
      { key: 'name', label: '名称' }, { key: 'cron_expression', label: 'Cron' }, { key: 'timezone', label: '时区' },
      { key: 'action', label: '动作' }, { key: 'next_run_at', label: '下次运行' }, { key: 'is_active', label: '启用' },
    ],
    create: {
      title: '定时任务', permission: 'scheduler.manage',
      example: {
        name: 'daily-reminder', cron_expression: '0 9 * * *', timezone: 'Asia/Shanghai', action: 'notification',
        payload: { channel_id: '请替换为通知渠道 ID', title: '每日运维提醒', message: '请检查今日发布与告警。', severity: 'info' },
      },
    },
  },
  {
    id: 'configurations', title: '配置中心', description: '支持环境覆盖、密钥加密和乐观版本。',
    endpoint: '/configurations', responseKey: 'configurations', permission: 'config.read',
    columns: [
      { key: 'namespace', label: '命名空间' }, { key: 'environment', label: '环境' }, { key: 'key', label: 'Key' },
      { key: 'value', label: '值' }, { key: 'is_secret', label: '密钥' }, { key: 'version', label: '版本' }, { key: 'is_active', label: '启用' },
    ],
    create: {
      title: '配置项', permission: 'config.manage',
      example: { namespace: 'production-api', environment: 'production', key: 'REQUEST_TIMEOUT', value: '30s', is_secret: false },
    },
  },
]

const accessGroups: Group[] = [
  {
    id: 'users', title: '用户', description: '管理账户状态与角色归属。', endpoint: '/users', responseKey: 'users', permission: 'user.read',
    columns: [
      { key: 'username', label: '用户名' }, { key: 'nickname', label: '昵称' }, { key: 'is_superuser', label: '超级管理员' },
      { key: 'is_active', label: '启用' }, { key: 'role_ids', label: '角色' }, { key: 'last_login_at', label: '最后登录' },
    ],
    create: {
      title: '用户', permission: 'user.manage',
      example: { username: 'operator', nickname: '运维人员', password: '请设置至少 12 位密码', role_ids: [] },
    },
  },
  {
    id: 'roles', title: '角色', description: '以权限代码组合最小权限角色。', endpoint: '/roles', responseKey: 'roles', permission: 'role.read',
    columns: [
      { key: 'name', label: '标识' }, { key: 'display_name', label: '名称' }, { key: 'description', label: '说明' },
      { key: 'permissions', label: '权限' }, { key: 'updated_at', label: '更新时间' },
    ],
    create: {
      title: '角色', permission: 'role.manage',
      example: { name: 'release_operator', display_name: '发布操作员', description: '允许查看并发起非生产发布', permissions: ['deployment.read', 'deployment.run'] },
    },
  },
  {
    id: 'audit', title: '审计日志', description: '记录身份、资源、结果与请求上下文，不记录密钥。',
    endpoint: '/audit-logs', responseKey: 'audit_logs', permission: 'audit.read',
    columns: [
      { key: 'action', label: '动作' }, { key: 'resource_type', label: '资源' }, { key: 'resource_id', label: '资源 ID' },
      { key: 'result', label: '结果' }, { key: 'client_ip', label: '来源 IP' }, { key: 'created_at', label: '时间' },
    ],
  },
]

export type ManagementSection = 'repositories' | 'operations' | 'access'

export default function ManagementView({ section }: { section: ManagementSection }) {
  const user = useAuthStore((state) => state.user)
  const allGroups = section === 'repositories' ? repositoryGroups : section === 'operations' ? operationGroups : accessGroups
  const allowed = useCallback((permission: string) => Boolean(user?.is_superuser || user?.permissions.includes(permission)), [user])
  const groups = useMemo(() => allGroups.filter((group) => allowed(group.permission)), [allGroups, allowed])
  const [activeID, setActiveID] = useState(groups[0]?.id ?? '')
  const [rows, setRows] = useState<ResourceRecord[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const active = groups.find((group) => group.id === activeID) ?? groups[0]

  useEffect(() => {
    if (!groups.some((group) => group.id === activeID)) setActiveID(groups[0]?.id ?? '')
  }, [activeID, groups])

  const refresh = useCallback(async () => {
    if (!active) return
    setLoading(true)
    setError('')
    try {
      setRows(await getResources(active.endpoint, active.responseKey))
    } catch (loadError) {
      setError(apiErrorMessage(loadError))
    } finally {
      setLoading(false)
    }
  }, [active])

  useEffect(() => { void refresh() }, [refresh])

  async function rowAction(endpoint: string) {
    setError('')
    try {
      await postResource(endpoint)
      await refresh()
    } catch (actionError) {
      setError(apiErrorMessage(actionError))
    }
  }

  function actions(row: ResourceRecord) {
    const id = String(row.id ?? '')
    if (active?.id === 'tasks' && allowed('task.manage')) {
      return <>
        {row.status === 'pending' && <button type="button" onClick={() => void rowAction(`/tasks/${id}/cancel`)}>取消</button>}
        {row.status === 'failed' && row.is_idempotent === true && <button type="button" onClick={() => void rowAction(`/tasks/${id}/retry`)}>重试</button>}
      </>
    }
    if (active?.id === 'repositories' && allowed('repository.manage')) {
      return <button type="button" onClick={() => void rowAction(`/repositories/${id}/test`)}>测试连接</button>
    }
    if (active?.id === 'channels' && allowed('notification.manage')) {
      return <button type="button" onClick={() => void rowAction(`/notification-channels/${id}/test`)}>发送测试</button>
    }
    return null
  }

  if (!active) return <section className="management-page"><div className="empty-state">当前账户没有此模块的查看权限</div></section>
  return (
    <section className="management-page">
      <div className="page-heading">
        <div><span className="section-label">ZRT MANAGEMENT</span><h2>{active.title}</h2><p>{active.description}</p></div>
        <button className="refresh-button" type="button" disabled={loading} onClick={() => void refresh()}>{loading ? '加载中…' : '刷新'}</button>
      </div>
      {groups.length > 1 && <div className="tab-bar">{groups.map((group) => (
        <button type="button" className={group.id === active.id ? 'active' : ''} key={group.id} onClick={() => setActiveID(group.id)}>{group.title}</button>
      ))}</div>}
      {error && <div className="form-alert error system-alert">{error}</div>}
      <div className="resource-panel">
        <ResourceTable rows={rows} columns={active.columns} actions={actions} />
      </div>
      {active.create && allowed(active.create.permission) && (
        <JsonCreatePanel title={active.create.title} endpoint={active.endpoint} example={active.create.example} onCreated={() => void refresh()} />
      )}
    </section>
  )
}
