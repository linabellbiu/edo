import { type FormEvent, useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'

import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import ResourceSelectField from '@/components/ResourceSelectField'
import { useAuthStore } from '@/stores/auth'

type Platform = 'docker' | 'kubernetes'
type EnvironmentLevel = 'development' | 'staging' | 'production'

interface Environment {
  id: string
  name: string
  description: string
  platform: Platform
  environment: EnvironmentLevel
  runtime_id: string
  namespace: string
  workload_name: string
  container_name: string
  rollout_timeout: number
  is_active: boolean
}

interface DockerEndpoint {
  id: string
  name: string
  host: string
  ssh_configured: boolean
  is_active: boolean
}

interface KubernetesCluster {
  id: string
  name: string
  api_server: string
  default_namespace: string
  is_active: boolean
}

const emptyForm = {
  name: '', description: '', platform: 'docker' as Platform,
  environment: 'development' as EnvironmentLevel, runtime_id: '', namespace: 'default',
  workload_name: '', container_name: '', rollout_timeout: 300,
}

const levelLabels: Record<EnvironmentLevel, string> = {
  development: '开发', staging: '测试 / 预发布', production: '生产',
}

export default function EnvironmentView() {
  const [searchParams, setSearchParams] = useSearchParams()
  const user = useAuthStore((state) => state.user)
  const canManage = Boolean(user?.is_superuser || user?.permissions.includes('deployment.manage'))
  const canReadCluster = Boolean(user?.is_superuser || user?.permissions.includes('cluster.read'))
  const [environments, setEnvironments] = useState<Environment[]>([])
  const [dockerEndpoints, setDockerEndpoints] = useState<DockerEndpoint[]>([])
  const [clusters, setClusters] = useState<KubernetesCluster[]>([])
  const [form, setForm] = useState(emptyForm)
  const [formOpen, setFormOpen] = useState(false)
  const [editingID, setEditingID] = useState('')
  const [loading, setLoading] = useState(true)
  const [submitting, setSubmitting] = useState(false)
  const [testing, setTesting] = useState(false)
  const [message, setMessage] = useState('')
  const [error, setError] = useState('')

  const refresh = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const [environmentResult, dockerResult, clusterResult] = await Promise.all([
        client.get<{ environments: Environment[] }>('/environments'),
        canReadCluster ? client.get<{ endpoints: DockerEndpoint[] }>('/docker/endpoints') : Promise.resolve(null),
        canReadCluster ? client.get<{ clusters: KubernetesCluster[] }>('/kubernetes/clusters') : Promise.resolve(null),
      ])
      setEnvironments(environmentResult.data.environments || [])
      setDockerEndpoints(dockerResult?.data.endpoints || [])
      setClusters(clusterResult?.data.clusters || [])
    } catch (loadError) {
      setError(apiErrorMessage(loadError))
    } finally {
      setLoading(false)
    }
  }, [canReadCluster])

  useEffect(() => { void refresh() }, [refresh])

  useEffect(() => {
    if (searchParams.get('create') !== '1' || !canManage) return
    openCreate()
    setSearchParams({}, { replace: true })
  }, [canManage, searchParams, setSearchParams])

  const runtimeSummary = useMemo(() => {
    const result = new Map<string, string>()
    dockerEndpoints.forEach((item) => result.set(item.id, `${item.name} · ${item.host}`))
    clusters.forEach((item) => result.set(item.id, `${item.name} · ${item.api_server || '集群内连接'}`))
    return result
  }, [clusters, dockerEndpoints])

  function openCreate() {
    setEditingID('')
    setForm(emptyForm)
    setMessage('')
    setError('')
    setFormOpen(true)
  }

  function editEnvironment(environment: Environment) {
    setEditingID(environment.id)
    setForm({
      name: environment.name, description: environment.description || '', platform: environment.platform,
      environment: environment.environment, runtime_id: environment.runtime_id,
      namespace: environment.namespace || 'default', workload_name: environment.workload_name,
      container_name: environment.container_name || '', rollout_timeout: environment.rollout_timeout,
    })
    setMessage('')
    setError('')
    setFormOpen(true)
    window.scrollTo({ top: 0, behavior: 'smooth' })
  }

  function closeForm() {
    setFormOpen(false)
    setEditingID('')
    setForm(emptyForm)
    setMessage('')
    setError('')
  }

  async function testConnection() {
    if (!form.runtime_id) {
      setError('请选择发布连接')
      return
    }
    setTesting(true)
    setError('')
    setMessage('')
    try {
      const endpoint = form.platform === 'docker'
        ? `/docker/endpoints/${form.runtime_id}/ping`
        : `/kubernetes/clusters/${form.runtime_id}/ping`
      await client.post(endpoint)
      setMessage('连接测试成功')
    } catch (testError) {
      setError(apiErrorMessage(testError))
    } finally {
      setTesting(false)
    }
  }

  async function submit(event: FormEvent) {
    event.preventDefault()
    setSubmitting(true)
    setError('')
    setMessage('')
    try {
      const payload = {
        ...form,
        namespace: form.platform === 'kubernetes' ? form.namespace : '',
        container_name: form.platform === 'kubernetes' ? form.container_name : '',
      }
      if (editingID) {
        await client.put(`/environments/${editingID}`, payload)
      } else {
        await client.post('/environments', payload)
      }
      closeForm()
      await refresh()
    } catch (submitError) {
      setError(apiErrorMessage(submitError))
    } finally {
      setSubmitting(false)
    }
  }

  async function setActive(environment: Environment) {
    setError('')
    try {
      await client.patch(`/environments/${environment.id}/status`, { active: !environment.is_active })
      await refresh()
    } catch (statusError) {
      setError(apiErrorMessage(statusError))
    }
  }

  return <section className="devops-page page-enter">
    <div className="page-heading">
      <div><span className="section-label">DEPLOYMENT ENVIRONMENTS</span><h2>环境管理</h2><p>维护真实的发布目标。流水线部署节点选择环境后，ZRT 会通过 Kubernetes API、Docker API 或 Docker SSH 连接在目标位置发布。</p></div>
      {canManage && <button className="primary-button" type="button" onClick={formOpen ? closeForm : openCreate}>{formOpen ? '取消创建' : '＋ 创建环境'}</button>}
    </div>

    {error && <div className="form-alert error system-alert" role="alert">{error}</div>}
    {message && <div className="form-alert success system-alert" role="status">{message}</div>}

    {formOpen && <form className="create-sheet modern-card" onSubmit={(event) => void submit(event)}>
      <div className="sheet-header"><div><h3>{editingID ? '编辑环境' : '创建环境'}</h3><p>环境名称可以自定义；生产级环境会继续执行不可变镜像和审批保护。</p></div><button type="button" onClick={closeForm}>×</button></div>
      <div className="form-grid">
        <label>环境名称<input required maxLength={128} value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} placeholder="例如：华东生产、客户演示" /></label>
        <label>环境级别<select value={form.environment} onChange={(event) => setForm({ ...form, environment: event.target.value as EnvironmentLevel })}><option value="development">开发</option><option value="staging">测试 / 预发布</option><option value="production">生产（需要审批）</option></select></label>
        <label className="span-2">说明<input maxLength={500} value={form.description} onChange={(event) => setForm({ ...form, description: event.target.value })} placeholder="说明该环境的位置、用途和维护负责人" /></label>
        <label>发布类型<select value={form.platform} onChange={(event) => setForm({ ...form, platform: event.target.value as Platform, runtime_id: '', namespace: event.target.value === 'kubernetes' ? 'default' : '' })}><option value="docker">Docker</option><option value="kubernetes">Kubernetes</option></select></label>
        <ResourceSelectField id="environment-runtime" label={form.platform === 'docker' ? 'Docker SSH 主机' : 'Kubernetes 集群'} createLabel="容器连接" createTo={form.platform === 'docker' ? '/infrastructure?create=ssh' : '/infrastructure'} required value={form.runtime_id} onChange={(event) => {
          const cluster = clusters.find((item) => item.id === event.target.value)
          setForm({ ...form, runtime_id: event.target.value, namespace: cluster?.default_namespace || form.namespace })
        }} help={form.platform === 'docker' ? <>选择测试成功的 Docker SSH 主机；也可以直接<Link to="/infrastructure?create=ssh">创建 SSH 连接</Link>。</> : '选择已验证的集群连接。'}>
          <option value="">请选择发布连接</option>
          {form.platform === 'docker'
            ? dockerEndpoints.filter((item) => item.is_active && item.host.startsWith('ssh://')).map((item) => <option key={item.id} value={item.id}>{item.name} · SSH</option>)
            : clusters.filter((item) => item.is_active).map((item) => <option key={item.id} value={item.id}>{item.name} · {item.api_server || '集群内连接'}</option>)}
        </ResourceSelectField>
        {form.platform === 'kubernetes' && <><label>命名空间<input required value={form.namespace} onChange={(event) => setForm({ ...form, namespace: event.target.value })} placeholder="default" /></label><label>Deployment 名称<input required value={form.workload_name} onChange={(event) => setForm({ ...form, workload_name: event.target.value })} placeholder="order-api" /></label><label>容器名称<input required value={form.container_name} onChange={(event) => setForm({ ...form, container_name: event.target.value })} placeholder="api" /></label></>}
        {form.platform === 'docker' && <label>容器名称<input required value={form.workload_name} onChange={(event) => setForm({ ...form, workload_name: event.target.value })} placeholder="order-api" /><small className="field-help">发布时在目标地址拉取镜像，并基于现有容器配置安全替换。</small></label>}
        <label>发布超时（秒）<input type="number" min={30} max={3600} required value={form.rollout_timeout} onChange={(event) => setForm({ ...form, rollout_timeout: Number(event.target.value) })} /></label>
      </div>
      <div className="form-actions"><button className="secondary-button" type="button" disabled={testing || submitting} onClick={() => void testConnection()}>{testing ? '测试中…' : '测试连接'}</button><button className="secondary-button" type="button" onClick={closeForm}>取消</button><button className="primary-button" type="submit" disabled={submitting}>{submitting ? '保存中…' : editingID ? '保存' : '创建'}</button></div>
    </form>}

    <div className="resource-card-grid environment-grid">
      {environments.map((environment) => <article className={`resource-card modern-card${environment.is_active ? '' : ' inactive'}`} key={environment.id}>
        <div className={`resource-icon ${environment.platform === 'kubernetes' ? 'build-icon' : 'release-icon'}`}>{environment.platform === 'kubernetes' ? 'K8s' : 'D'}</div>
        <div><div className="card-title-line"><h3>{environment.name}</h3><span>{levelLabels[environment.environment]} · {environment.platform === 'kubernetes' ? 'Kubernetes' : 'Docker'}</span></div><p>{environment.description || runtimeSummary.get(environment.runtime_id) || '未填写说明'}</p><div className="meta-row"><span>{runtimeSummary.get(environment.runtime_id) || '连接不可用'}</span><span>{environment.namespace ? `${environment.namespace} / ` : ''}{environment.workload_name}</span><span>{environment.is_active ? '已启用' : '已停用'}</span></div></div>
        {canManage && <div className="card-actions"><button type="button" onClick={() => editEnvironment(environment)}>编辑</button><button type="button" onClick={() => void setActive(environment)}>{environment.is_active ? '停用' : '启用'}</button></div>}
      </article>)}
      {!loading && environments.length === 0 && <div className="modern-empty"><span className="empty-icon">◎</span><h3>还没有发布环境</h3><p>先创建 Docker 或 Kubernetes 发布目标，再在流水线部署节点中选择。</p>{canManage && <button className="primary-button" type="button" onClick={openCreate}>＋ 创建环境</button>}</div>}
    </div>
  </section>
}
