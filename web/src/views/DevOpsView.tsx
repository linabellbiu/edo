import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'

import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import ResourceSelectField from '@/components/ResourceSelectField'
import { useAuthStore } from '@/stores/auth'

type Section = 'applications' | 'repositories' | 'build-plans' | 'image-registries' | 'deployment-plans' | 'pipelines'

interface Repository {
  id: string; name: string; provider: string; clone_url: string; default_branch: string
  webhook_enabled: boolean; webhook_url?: string; webhook_secret?: string; is_active: boolean
  auth_type: 'none' | 'token' | 'ssh_key'; username?: string; allow_insecure_http: boolean
  has_credential?: boolean; credential_id?: string
}
interface GitCredential {
  id: string; name: string; provider: string; auth_type: 'token' | 'ssh_key'; username?: string; secret_hint: string
}
interface GitRef { name: string; sha: string }
interface RepositoryRefResult { branches: GitRef[]; tags: GitRef[] }
interface RepositoryCheck {
  status: 'checking' | 'success' | 'error'
  message: string
}
interface BuildPlan {
  id: string; name: string; kind: string; description: string; dockerfile_path?: string
  context_path: string; timeout_seconds: number; is_active: boolean
}
interface ImageRegistry {
  id: string; name: string; provider: string; endpoint: string; namespace: string
  has_credential: boolean; is_active: boolean
}
interface DeploymentPlan {
  id: string; name: string; kind: string; description: string; helm_chart?: string
  compose_file?: string; service_name?: string; timeout_seconds: number; is_active: boolean
}
interface DeploymentTarget { id: string; name: string; environment: string; platform: string; is_active: boolean }
interface WorkflowTemplate { id: string; name: string; description: string; revision: number; is_active: boolean }
interface ApplicationEnvironment {
  id?: string; key: 'dev' | 'test' | 'pre' | 'prod'; name: string; branch: string
  poll_enabled: boolean; watch_push: boolean; watch_pull_request: boolean; watch_tags: boolean
  tag_pattern: string; release_plan_id?: string; deployment_target_id?: string; sort_order: number
}
interface Application {
  id: string; name: string; description: string; repository_id: string; branch: string
  poll_enabled: boolean; poll_interval_seconds: number; watch_push: boolean; watch_pull_request: boolean
  watch_tags: boolean; tag_pattern: string; build_plan_id?: string; image_registry_id?: string
  release_plan_id?: string; deployment_target_id?: string; last_observed_ref?: string
  last_observed_commit?: string; sync_status: string; sync_message?: string; last_checked_at?: string
  is_active: boolean; repository?: Repository; build_plan?: BuildPlan; image_registry?: ImageRegistry
  release_plan?: DeploymentPlan; deployment_target?: DeploymentTarget
  release_approval_enabled: boolean; environments?: ApplicationEnvironment[]
  workflow_template_id?: string; workflow_template?: WorkflowTemplate
  workflow?: { id: string; is_active: boolean; revision: number }
}
interface PipelineRun {
  id: string; application_id: string; trigger: string; ref: string; commit_sha: string
  status: string; stage: string; message?: string; created_at: string; application?: Application
  environment?: string; current_node_id?: string; approved_by?: string
}

const sectionCopy: Record<Section, { title: string; description: string }> = {
  applications: { title: '应用', description: '选择代码仓库，配置监听方式，并绑定完整的构建与发布流程。' },
  repositories: { title: '代码仓库', description: '统一管理 Git 仓库地址、认证方式和 Webhook。' },
  'build-plans': { title: '构建方案', description: '保存可复用的打包脚本或 Dockerfile 构建配置。' },
  'image-registries': { title: '镜像仓库', description: '管理 Harbor、Docker Hub 或其他兼容 Registry。' },
  'deployment-plans': { title: '部署方案', description: '定义部署节点如何通过 Helm、Docker Compose、Docker 或受控脚本执行。' },
  pipelines: { title: '流水线', description: '查看代码变更以及应用的流程配置状态。' },
}

const defaultEnvironments: Array<ApplicationEnvironment & { enabled: boolean }> = [
  { key: 'dev', name: '开发环境', branch: 'dev', enabled: true, poll_enabled: true, watch_push: true, watch_pull_request: false, watch_tags: false, tag_pattern: 'v*', release_plan_id: '', deployment_target_id: '', sort_order: 0 },
  { key: 'test', name: '测试环境', branch: 'test', enabled: true, poll_enabled: false, watch_push: true, watch_pull_request: true, watch_tags: false, tag_pattern: 'v*', release_plan_id: '', deployment_target_id: '', sort_order: 1 },
  { key: 'pre', name: '预发布环境', branch: 'main', enabled: true, poll_enabled: false, watch_push: true, watch_pull_request: true, watch_tags: false, tag_pattern: 'v*', release_plan_id: '', deployment_target_id: '', sort_order: 2 },
  { key: 'prod', name: '生产环境', branch: 'release', enabled: true, poll_enabled: false, watch_push: false, watch_pull_request: false, watch_tags: true, tag_pattern: 'v*', release_plan_id: '', deployment_target_id: '', sort_order: 3 },
]

function initialApplicationForm() {
  return {
    name: '', description: '', repository_id: '', branch: 'dev', poll_enabled: true,
    poll_interval_seconds: 3, watch_push: true, watch_pull_request: false,
    watch_tags: false, tag_pattern: 'v*', build_plan_id: '', image_registry_id: '',
    release_plan_id: '', deployment_target_id: '', release_approval_enabled: true,
    workflow_template_id: '',
    environments: defaultEnvironments.map((item) => ({ ...item })),
  }
}

function initialRepositoryForm() {
  return {
    name: '', provider: 'github', clone_url: '', default_branch: 'main', auth_type: 'none', username: '',
    credential_mode: 'saved', credential_id: '', credential_name: '', credential_secret: '',
    webhook_enabled: true, allow_insecure_http: false,
  }
}

function formatTime(value?: string) {
  if (!value) return '尚未检查'
  return new Date(value).toLocaleString('zh-CN', { hour12: false })
}

function shortSHA(value?: string) { return value ? value.slice(0, 8) : '—' }

function kindLabel(value: string) {
  return ({ script: '脚本', dockerfile: 'Dockerfile', helm: 'Helm', compose: 'Docker Compose', docker: 'Docker', generic: '通用', harbor: 'Harbor', docker_hub: 'Docker Hub' } as Record<string, string>)[value] || value
}

function credentialCompatible(item: GitCredential, provider: string, authType: string) {
  return item.auth_type === authType && (item.provider === 'generic' || provider === 'generic' || item.provider === provider)
}

function repositoryCheckMessage(result: RepositoryRefResult, defaultBranch: string) {
  const branches = result.branches ?? []
  const tags = result.tags ?? []
  let message = branches.length === 0 && tags.length === 0
    ? '连接成功，仓库当前没有分支或标签。'
    : `读取到 ${branches.length} 个分支、${tags.length} 个标签。`
  if (defaultBranch && !branches.some((branch) => branch.name === defaultBranch)) {
    message += ` 未找到默认分支 ${defaultBranch}。`
  }
  return message
}

function RepositoryCheckResult({ check }: { check: RepositoryCheck }) {
  return <div className={`repository-check repository-check-${check.status}`} role={check.status === 'error' ? 'alert' : 'status'} aria-live="polite">
    <span className="repository-check-dot" aria-hidden="true" />
    <div><strong>{check.status === 'checking' ? '正在测试仓库' : check.status === 'success' ? '仓库可读取' : '仓库不可读取'}</strong><small>{check.message}</small></div>
  </div>
}

function RegistryCheckResult({ check }: { check: RepositoryCheck }) {
  return <div className={`repository-check repository-check-${check.status}`} role={check.status === 'error' ? 'alert' : 'status'} aria-live="polite">
    <span className="repository-check-dot" aria-hidden="true" />
    <div><strong>{check.status === 'checking' ? '正在登录镜像仓库' : check.status === 'success' ? '登录成功' : '登录失败'}</strong><small>{check.message}</small></div>
  </div>
}

function StatusPill({ value }: { value: string }) {
  const label = ({ idle: '等待检查', checking: '检查中', synced: '已同步', changed: '发现更新', failed: '检查失败', detected: '发现变更', ready: '等待部署', blocked: '配置不完整', awaiting_approval: '等待审核', running: '流程进行中', succeeded: '已完成', canceled: '已取消' } as Record<string, string>)[value] || value
  return <span className={`status-pill status-${value}`}>{label}</span>
}

function EmptyState({ title, description }: { title: string; description: string }) {
  return <div className="empty-state modern-empty"><span className="empty-icon">＋</span><h3>{title}</h3><p>{description}</p></div>
}

function FormActions({ submitting, editing, onCancel }: { submitting: boolean; editing?: boolean; onCancel: () => void }) {
  return <div className="form-actions"><button className="secondary-button" type="button" onClick={onCancel}>取消</button><button className="primary-button" type="submit" disabled={submitting}>{submitting ? '保存中…' : editing ? '保存修改' : '创建'}</button></div>
}

export default function DevOpsView({ section }: { section: Section }) {
	const navigate = useNavigate()
  const [searchParams, setSearchParams] = useSearchParams()
  const user = useAuthStore((state) => state.user)
  const canManageDelivery = Boolean(user?.is_superuser || user?.permissions.includes('delivery.manage'))
  const canRun = Boolean(user?.is_superuser || user?.permissions.includes('delivery.run'))
	const canReview = Boolean(user?.is_superuser || user?.permissions.includes('deployment.review'))
  const canManageRepository = Boolean(user?.is_superuser || user?.permissions.includes('repository.manage'))
  const canReadRepositorySecret = Boolean(user?.is_superuser || user?.permissions.includes('repository.secret.read'))
  const canReadCredential = Boolean(user?.is_superuser || user?.permissions.includes('credential.read'))
  const canManageCredential = Boolean(user?.is_superuser || user?.permissions.includes('credential.manage'))
  const canReadDelivery = Boolean(user?.is_superuser || user?.permissions.includes('delivery.read'))
  const canReadRepository = Boolean(user?.is_superuser || user?.permissions.includes('repository.read'))
  const canReadDeployment = Boolean(user?.is_superuser || user?.permissions.includes('deployment.read'))
  const [applications, setApplications] = useState<Application[]>([])
  const [repositories, setRepositories] = useState<Repository[]>([])
  const [credentials, setCredentials] = useState<GitCredential[]>([])
  const [buildPlans, setBuildPlans] = useState<BuildPlan[]>([])
  const [registries, setRegistries] = useState<ImageRegistry[]>([])
  const [deploymentPlans, setDeploymentPlans] = useState<DeploymentPlan[]>([])
  const [targets, setTargets] = useState<DeploymentTarget[]>([])
  const [workflowTemplates, setWorkflowTemplates] = useState<WorkflowTemplate[]>([])
  const [runs, setRuns] = useState<PipelineRun[]>([])
  const [formOpen, setFormOpen] = useState(false)
  const [editingID, setEditingID] = useState('')
  const [loading, setLoading] = useState(true)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')
  const [webhookSetup, setWebhookSetup] = useState<{ url: string; secret: string } | null>(null)
  const [repositoryChecks, setRepositoryChecks] = useState<Record<string, RepositoryCheck>>({})
  const [repositoryFormCheck, setRepositoryFormCheck] = useState<RepositoryCheck | null>(null)
  const [registryFormCheck, setRegistryFormCheck] = useState<RepositoryCheck | null>(null)
  const registryTestSequence = useRef(0)
  const [applicationForm, setApplicationForm] = useState(initialApplicationForm)
  const [repositoryForm, setRepositoryForm] = useState(initialRepositoryForm)
  const [buildForm, setBuildForm] = useState({ name: '', kind: 'dockerfile', description: '', script: '', dockerfile_path: 'Dockerfile', context_path: '.', artifact_path: '', timeout_seconds: 1800 })
  const [registryForm, setRegistryForm] = useState({ name: '', provider: 'harbor', endpoint: 'https://', namespace: '', username: '', credential: '', allow_insecure_http: false })
  const [deploymentForm, setDeploymentForm] = useState({ name: '', kind: 'helm', description: '', script: '', helm_chart: '', helm_values: '', compose_file: 'docker-compose.yml', service_name: '', timeout_seconds: 600 })
  const copy = sectionCopy[section]
  const canCreate = section === 'repositories' ? canManageRepository : section !== 'pipelines' && canManageDelivery

  const refresh = useCallback(async () => {
    setLoading(true)
      setError('')
    try {
      const repositoryRequest = canReadRepository
        ? client.get<{ repositories: Repository[] }>('/repositories')
        : Promise.resolve(null)
      const credentialRequest = canReadCredential
        ? client.get<{ credentials: GitCredential[] }>('/git-credentials')
        : Promise.resolve(null)
      const targetRequest = canReadDeployment
        ? client.get<{ targets: DeploymentTarget[] }>('/deployment-targets')
        : Promise.resolve(null)
      const applicationRequest = canReadDelivery
        ? client.get<{ applications: Application[] }>('/applications')
        : Promise.resolve(null)
      const buildRequest = canReadDelivery
        ? client.get<{ build_plans: BuildPlan[] }>('/build-plans')
        : Promise.resolve(null)
      const registryRequest = canReadDelivery
        ? client.get<{ image_registries: ImageRegistry[] }>('/image-registries')
        : Promise.resolve(null)
      const deploymentPlanRequest = canReadDelivery
        ? client.get<{ deployment_plans: DeploymentPlan[] }>('/deployment-plans')
        : Promise.resolve(null)
      const runRequest = canReadDelivery
        ? client.get<{ pipeline_runs: PipelineRun[] }>('/pipeline-runs')
        : Promise.resolve(null)
      const workflowTemplateRequest = canReadDelivery
        ? client.get<{ workflow_templates: WorkflowTemplate[] }>('/workflow-templates')
        : Promise.resolve(null)
      const [appResult, repoResult, credentialResult, buildResult, registryResult, deploymentPlanResult, targetResult, runResult, workflowTemplateResult] = await Promise.all([
        applicationRequest,
        repositoryRequest,
        credentialRequest,
        buildRequest,
        registryRequest,
        deploymentPlanRequest,
        targetRequest,
        runRequest,
        workflowTemplateRequest,
      ])
      setApplications(appResult?.data.applications || [])
      setRepositories(repoResult?.data.repositories || [])
      setCredentials(credentialResult?.data.credentials || [])
      setBuildPlans(buildResult?.data.build_plans || [])
      setRegistries(registryResult?.data.image_registries || [])
      setDeploymentPlans(deploymentPlanResult?.data.deployment_plans || [])
      setTargets(targetResult?.data.targets || [])
      setRuns(runResult?.data.pipeline_runs || [])
      setWorkflowTemplates(workflowTemplateResult?.data.workflow_templates || [])
    } catch (loadError) {
      setError(apiErrorMessage(loadError))
    } finally {
      setLoading(false)
    }
  }, [canReadCredential, canReadDelivery, canReadDeployment, canReadRepository])

  useEffect(() => { void refresh() }, [refresh])
  useEffect(() => { setRepositoryFormCheck(null) }, [repositoryForm])
  useEffect(() => {
    registryTestSequence.current += 1
    setRegistryFormCheck(null)
  }, [registryForm])
  useEffect(() => {
    if (searchParams.get('create') !== '1' || !canCreate) return
	setEditingID('')
	if (section === 'repositories') setRepositoryForm(initialRepositoryForm())
    setFormOpen(true)
    const next = new URLSearchParams(searchParams)
    next.delete('create')
    setSearchParams(next, { replace: true })
    window.scrollTo({ top: 0, behavior: 'smooth' })
  }, [canCreate, searchParams, section, setSearchParams])

  const counts = useMemo(() => ({
    configured: applications.filter((item) => item.build_plan_id && item.image_registry_id && item.workflow?.is_active).length,
    changed: applications.filter((item) => item.sync_status === 'changed').length,
  }), [applications])

  function closeForm() {
	registryTestSequence.current += 1
	setRegistryFormCheck(null)
    setFormOpen(false)
    setEditingID('')
    setApplicationForm(initialApplicationForm())
    setRepositoryForm(initialRepositoryForm())
    setError('')
  }

  function toggleCreateForm() {
    if (formOpen) {
      closeForm()
      return
    }
    setEditingID('')
    if (section === 'repositories') setRepositoryForm(initialRepositoryForm())
    setFormOpen(true)
  }

  function editApplication(application: Application) {
	const environments = defaultEnvironments.map((fallback) => {
	  const stored = application.environments?.find((item) => item.key === fallback.key)
	  return stored ? { ...fallback, ...stored, enabled: true } : { ...fallback, enabled: false }
	})
    setEditingID(application.id)
    setApplicationForm({
      name: application.name, description: application.description || '', repository_id: application.repository_id,
      branch: application.branch, poll_enabled: application.poll_enabled,
      poll_interval_seconds: application.poll_interval_seconds, watch_push: application.watch_push,
      watch_pull_request: application.watch_pull_request, watch_tags: application.watch_tags,
      tag_pattern: application.tag_pattern || 'v*', build_plan_id: application.build_plan_id || '',
      image_registry_id: application.image_registry_id || '', release_plan_id: application.release_plan_id || '',
      deployment_target_id: application.deployment_target_id || '',
	  release_approval_enabled: application.release_approval_enabled,
	  workflow_template_id: application.workflow_template_id || '',
	  environments,
    })
    setFormOpen(true)
    window.scrollTo({ top: 0, behavior: 'smooth' })
  }

  function editRepository(repository: Repository) {
    setEditingID(repository.id)
    setRepositoryForm({
      ...initialRepositoryForm(),
      name: repository.name,
      provider: repository.provider,
      clone_url: repository.clone_url,
      default_branch: repository.default_branch,
      auth_type: repository.auth_type,
      username: repository.username || '',
      credential_mode: repository.credential_id ? 'saved' : repository.has_credential ? 'existing' : 'saved',
      credential_id: repository.credential_id || '',
      webhook_enabled: repository.webhook_enabled,
      allow_insecure_http: repository.allow_insecure_http,
    })
    setRepositoryFormCheck(null)
    setFormOpen(true)
    window.scrollTo({ top: 0, behavior: 'smooth' })
  }

  async function submitApplication() {
	const enabledEnvironments = applicationForm.environments.filter((item) => item.enabled).map(({ enabled: _enabled, id: _id, ...item }) => item)
	const primary = enabledEnvironments[0]
	await submit(editingID ? `/applications/${editingID}` : '/applications', {
	  ...applicationForm,
	  environments: applicationForm.workflow_template_id ? [] : enabledEnvironments,
	  branch: primary?.branch || applicationForm.branch,
	  poll_enabled: primary?.poll_enabled || false,
	  watch_push: primary?.watch_push || false,
	  watch_pull_request: primary?.watch_pull_request || false,
	  watch_tags: primary?.watch_tags || false,
	  tag_pattern: primary?.tag_pattern || '',
	}, () => undefined, editingID ? 'put' : 'post')
  }

  async function submit(endpoint: string, payload: unknown, after: () => void, method: 'post' | 'put' = 'post') {
    setSubmitting(true)
    setError('')
    try {
      await client[method](endpoint, payload)
      after()
      closeForm()
      await refresh()
    } catch (submitError) {
      setError(apiErrorMessage(submitError))
    } finally {
      setSubmitting(false)
    }
  }

  async function action(endpoint: string) {
    setError('')
    try {
      await client.post(endpoint, undefined, { timeout: 35_000 })
      await refresh()
    } catch (actionError) {
      setError(apiErrorMessage(actionError))
      await refresh()
    }
  }

  async function runAction(endpoint: string, payload: unknown = undefined) {
	setError('')
	try {
	  await client.post(endpoint, payload)
	  await refresh()
	} catch (actionError) {
	  setError(apiErrorMessage(actionError))
	  await refresh()
	}
  }

  async function submitRepository() {
    setSubmitting(true)
    setError('')
    try {
      let credentialID = repositoryForm.auth_type === 'none' ? '' : repositoryForm.credential_id
      if (repositoryForm.auth_type !== 'none' && repositoryForm.credential_mode === 'new') {
        const saved = await client.post<{ credential: GitCredential }>('/git-credentials', {
          name: repositoryForm.credential_name,
          provider: repositoryForm.provider,
          auth_type: repositoryForm.auth_type,
          username: repositoryForm.username,
          secret: repositoryForm.credential_secret,
        })
        credentialID = saved.data.credential.id
      }
      const payload = {
        name: repositoryForm.name, provider: repositoryForm.provider, clone_url: repositoryForm.clone_url,
        default_branch: repositoryForm.default_branch, auth_type: repositoryForm.auth_type,
        username: repositoryForm.username, credential_id: credentialID || null,
        webhook_enabled: repositoryForm.webhook_enabled,
        allow_insecure_http: repositoryForm.allow_insecure_http, regenerate_webhook: false,
      }
      const result = editingID
        ? await client.put<{ repository: Repository; webhook_secret?: string }>(`/repositories/${editingID}`, payload)
        : await client.post<{ repository: Repository; webhook_secret?: string }>('/repositories', payload)
      if (result.data.webhook_secret) {
        setWebhookSetup({ url: result.data.repository.webhook_url || `/api/v1/webhooks/git/${result.data.repository.id}`, secret: result.data.webhook_secret })
      }
      closeForm()
      await refresh()
    } catch (submitError) {
      setError(apiErrorMessage(submitError))
    } finally {
      setSubmitting(false)
    }
  }

  async function showWebhook(repository: Repository) {
    setError('')
    try {
      const response = await client.get<{ webhook_url: string; webhook_secret: string }>(`/repositories/${repository.id}/webhook`)
      setWebhookSetup({ url: response.data.webhook_url, secret: response.data.webhook_secret })
      window.scrollTo({ top: 0, behavior: 'smooth' })
    } catch (requestError) {
      setError(apiErrorMessage(requestError))
    }
  }

  async function deleteRepository(repository: Repository) {
    if (!window.confirm(`确认删除代码仓库“${repository.name}”？\n\n该仓库的 Webhook 投递记录会一并清理；正在被应用使用的仓库不会被删除。`)) return
    setError('')
    try {
      await client.delete(`/repositories/${repository.id}`)
      setRepositoryChecks((checks) => {
        const next = { ...checks }
        delete next[repository.id]
        return next
      })
      setWebhookSetup((setup) => setup?.url.endsWith(`/${repository.id}`) ? null : setup)
      await refresh()
    } catch (deleteError) {
      setError(apiErrorMessage(deleteError))
    }
  }

  async function testRepository(repository: Repository) {
    setRepositoryChecks((checks) => ({
      ...checks,
      [repository.id]: { status: 'checking', message: '正在读取远端分支和标签…' },
    }))
    try {
      const result = await client.post<RepositoryRefResult>(`/repositories/${repository.id}/test`, undefined, { timeout: 35_000 })
      setRepositoryChecks((checks) => ({
        ...checks,
        [repository.id]: { status: 'success', message: repositoryCheckMessage(result.data, repository.default_branch) },
      }))
    } catch (testError) {
      setRepositoryChecks((checks) => ({
        ...checks,
        [repository.id]: { status: 'error', message: apiErrorMessage(testError) },
      }))
    }
  }

  async function testRepositoryForm() {
    setRepositoryFormCheck({ status: 'checking', message: '正在读取远端分支和标签…' })
    try {
      if (editingID && repositoryForm.credential_mode === 'existing') {
        const result = await client.post<RepositoryRefResult>(`/repositories/${editingID}/test`, undefined, { timeout: 35_000 })
        setRepositoryFormCheck({
          status: 'success',
          message: `当前保存的连接可用。${repositoryCheckMessage(result.data, repositoryForm.default_branch)}`,
        })
        return
      }
      const result = await client.post<RepositoryRefResult>('/repositories/test', {
        name: repositoryForm.name, provider: repositoryForm.provider, clone_url: repositoryForm.clone_url,
        default_branch: repositoryForm.default_branch, auth_type: repositoryForm.auth_type,
        username: repositoryForm.username,
        credential_id: repositoryForm.credential_mode === 'saved' ? repositoryForm.credential_id || null : null,
        credential: repositoryForm.credential_mode === 'new' ? repositoryForm.credential_secret || null : null,
        webhook_enabled: repositoryForm.webhook_enabled,
        allow_insecure_http: repositoryForm.allow_insecure_http, regenerate_webhook: false,
      }, { timeout: 35_000 })
      setRepositoryFormCheck({
        status: 'success',
        message: repositoryCheckMessage(result.data, repositoryForm.default_branch),
      })
    } catch (testError) {
      setRepositoryFormCheck({ status: 'error', message: apiErrorMessage(testError) })
    }
  }

  async function testRegistryForm() {
    const testSequence = ++registryTestSequence.current
    setError('')
    setRegistryFormCheck({ status: 'checking', message: '正在验证 Registry 地址和登录凭据…' })
    try {
      const result = await client.post<{ message: string }>('/image-registries/test', {
        ...registryForm,
        credential: registryForm.credential || null,
      }, { timeout: 20_000 })
      if (testSequence !== registryTestSequence.current) return
      setRegistryFormCheck({ status: 'success', message: `${result.data.message}，当前配置尚未保存。` })
    } catch (testError) {
      if (testSequence !== registryTestSequence.current) return
      setRegistryFormCheck({ status: 'error', message: apiErrorMessage(testError) })
    }
  }

  return <section className="devops-page page-enter">
    <div className="page-heading modern-heading">
      <div><span className="section-label">持续交付</span><h2>{copy.title}</h2><p>{copy.description}</p></div>
      <div className="heading-actions">
        <button className="icon-button" type="button" onClick={() => void refresh()} disabled={loading} aria-label="刷新">↻</button>
        {canCreate && <button className="primary-button" type="button" onClick={toggleCreateForm}>＋ 创建{copy.title.replace('代码', '')}</button>}
      </div>
    </div>

    {section === 'applications' && <div className="summary-strip">
      <div><strong>{applications.length}</strong><span>应用总数</span></div>
      <div><strong>{counts.configured}</strong><span>流程已绑定</span></div>
      <div><strong>{counts.changed}</strong><span>发现代码更新</span></div>
      <div><strong>{applications.filter((item) => item.poll_enabled).length}</strong><span>Pull 监听中</span></div>
    </div>}

    {error && <div className="form-alert error system-alert" role="alert">{error}</div>}

    {section === 'repositories' && webhookSetup && <div className="webhook-setup modern-card" role="status">
      <div><strong>Webhook 配置</strong><p>可随时从仓库卡片重新查看；请只提供给对应代码托管平台。</p></div>
      <dl><div><dt>回调地址</dt><dd>{webhookSetup.url}</dd></div><div><dt>签名密钥</dt><dd>{webhookSetup.secret}</dd></div></dl>
      <button type="button" onClick={() => setWebhookSetup(null)}>关闭</button>
    </div>}

    {formOpen && section === 'applications' && <form className="create-sheet application-sheet" onSubmit={(event) => { event.preventDefault(); void submitApplication() }}>
      <div className="sheet-header"><div><h3>{editingID ? '配置应用' : '创建应用'}</h3><p>选择公共发布计划后，环境、分支和发布路径会从画布复制到应用。</p></div><button type="button" onClick={closeForm}>×</button></div>
      <div className="form-grid">
        <label>应用名称<input required value={applicationForm.name} onChange={(e) => setApplicationForm({ ...applicationForm, name: e.target.value })} placeholder="例如：订单服务" /></label>
        <ResourceSelectField id="application-repository" label="代码仓库" createLabel="代码仓库" createTo={canManageRepository ? '/repositories?create=1' : undefined} required value={applicationForm.repository_id} onChange={(event) => setApplicationForm({ ...applicationForm, repository_id: event.target.value })}><option value="">请选择仓库</option>{repositories.filter((item) => item.is_active).map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</ResourceSelectField>
        <label className="span-2">说明<input value={applicationForm.description} onChange={(e) => setApplicationForm({ ...applicationForm, description: e.target.value })} placeholder="这个应用负责什么" /></label>
        <ResourceSelectField id="application-build-plan" label="构建方案" createLabel="构建方案" createTo={canManageDelivery ? '/build-plans?create=1' : undefined} value={applicationForm.build_plan_id} onChange={(event) => setApplicationForm({ ...applicationForm, build_plan_id: event.target.value })}><option value="">暂不绑定</option>{buildPlans.filter((item) => item.is_active).map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</ResourceSelectField>
        <ResourceSelectField id="application-image-registry" label="镜像仓库" createLabel="镜像仓库" createTo={canManageDelivery ? '/image-registries?create=1' : undefined} value={applicationForm.image_registry_id} onChange={(event) => setApplicationForm({ ...applicationForm, image_registry_id: event.target.value })}><option value="">暂不绑定</option>{registries.filter((item) => item.is_active).map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</ResourceSelectField>
        <ResourceSelectField id="application-workflow-template" label="公共发布计划" createLabel="公共发布计划" createTo={canManageDelivery ? '/release-workflows/editor?create=1' : undefined} wrapperClassName="span-2" help="环境、监听分支、触发事件和部署节点都在发布计划画布中配置。" required={!editingID} value={applicationForm.workflow_template_id} onChange={(event) => setApplicationForm({ ...applicationForm, workflow_template_id: event.target.value })}><option value="">{editingID ? '保留应用当前自定义计划' : '请选择发布计划'}</option>{workflowTemplates.filter((item) => item.is_active).map((item) => <option key={item.id} value={item.id}>{item.name} · 第 {item.revision} 版</option>)}</ResourceSelectField>
        <label>Pull 检查间隔<select value={applicationForm.poll_interval_seconds} onChange={(e) => setApplicationForm({ ...applicationForm, poll_interval_seconds: Number(e.target.value) })}><option value={3}>3 秒</option><option value={5}>5 秒</option><option value={10}>10 秒</option><option value={60}>1 分钟</option></select></label>
      </div>
      <div className="form-block"><span className="form-block-title">发布计划审核</span><div className="approval-choice">
        <label className={applicationForm.release_approval_enabled ? 'selected' : ''}><input type="radio" name="release-approval" checked={applicationForm.release_approval_enabled} onChange={() => setApplicationForm({ ...applicationForm, release_approval_enabled: true })} /><span><strong>需要审核</strong><small>生产部署的每条路径都必须经过审核节点，申请人不能审核自己。</small></span></label>
        <label className={!applicationForm.release_approval_enabled ? 'selected' : ''}><input type="radio" name="release-approval" checked={!applicationForm.release_approval_enabled} onChange={() => setApplicationForm({ ...applicationForm, release_approval_enabled: false })} /><span><strong>关闭审核</strong><small>审核节点不再阻塞流程，适合内部开发或已由外部系统审批的应用。</small></span></label>
      </div></div>
      <FormActions submitting={submitting} editing={Boolean(editingID)} onCancel={closeForm} />
    </form>}

    {formOpen && section === 'repositories' && <form className="create-sheet" onSubmit={(event) => { event.preventDefault(); void submitRepository() }}>
      <div className="sheet-header"><div><h3>{editingID ? '修改代码仓库' : '添加代码仓库'}</h3><p>支持普通 Git、GitHub、GitLab、Gitea 和 Gitee。</p></div><button type="button" onClick={closeForm}>×</button></div>
      <div className="form-grid">
        <label>名称<input required maxLength={128} value={repositoryForm.name} onChange={(event) => setRepositoryForm({ ...repositoryForm, name: event.target.value })} /><small className="field-help">支持中文、英文、数字、空格、点、下划线和短横线。</small></label>
        <label>平台<select value={repositoryForm.provider} onChange={(event) => setRepositoryForm({ ...repositoryForm, provider: event.target.value, credential_mode: 'saved', credential_id: '' })}><option value="github">GitHub</option><option value="gitlab">GitLab</option><option value="gitea">Gitea</option><option value="gitee">Gitee</option><option value="generic">普通 Git</option></select></label>
        <label className="span-2">Clone 地址<input required value={repositoryForm.clone_url} onChange={(event) => setRepositoryForm({ ...repositoryForm, clone_url: event.target.value })} placeholder="https://git.example.com/team/project.git" /></label>
        <label>默认分支<input value={repositoryForm.default_branch} onChange={(event) => setRepositoryForm({ ...repositoryForm, default_branch: event.target.value })} /></label>
        <label>认证方式<select value={repositoryForm.auth_type} onChange={(event) => setRepositoryForm({ ...repositoryForm, auth_type: event.target.value, credential_mode: 'saved', credential_id: '', credential_secret: '' })}><option value="none">无需认证</option><option value="token">访问令牌</option><option value="ssh_key">SSH 私钥</option></select></label>
        {repositoryForm.auth_type !== 'none' && <div className="span-2 repository-credential-block">
          <div className="credential-mode-tabs">
            {repositoryForm.credential_mode === 'existing' && <button type="button" className="active">保留现有凭据</button>}
            <button type="button" className={repositoryForm.credential_mode === 'saved' ? 'active' : ''} onClick={() => setRepositoryForm({ ...repositoryForm, credential_mode: 'saved' })}>选择我的令牌</button>
            {canManageCredential && <button type="button" className={repositoryForm.credential_mode === 'new' ? 'active' : ''} onClick={() => setRepositoryForm({ ...repositoryForm, credential_mode: 'new' })}>新建并保存</button>}
          </div>
          {repositoryForm.credential_mode === 'existing' ? <p className="credential-permission-hint">当前凭据不会显示，保存时继续使用。要更换凭据，请选择自己的令牌或新建令牌。</p> : repositoryForm.credential_mode === 'saved' ? canReadCredential ? <div className="form-grid compact-grid">
            <label className="span-2">已保存令牌<select required value={repositoryForm.credential_id} onChange={(event) => { const selected = credentials.find((item) => item.id === event.target.value); setRepositoryForm({ ...repositoryForm, credential_id: event.target.value, username: selected?.username || repositoryForm.username }) }}><option value="">请选择</option>{credentials.filter((item) => credentialCompatible(item, repositoryForm.provider, repositoryForm.auth_type)).map((item) => <option key={item.id} value={item.id}>{item.name} · {item.secret_hint}</option>)}</select></label>
          </div> : <p className="credential-permission-hint">当前角色没有查看个人令牌的权限，请联系管理员授权。</p> : <div className="form-grid compact-grid">
            <label>令牌名称<input required value={repositoryForm.credential_name} onChange={(event) => setRepositoryForm({ ...repositoryForm, credential_name: event.target.value })} placeholder="例如：GitHub 生产账号" /></label>
            <label>用户名<input value={repositoryForm.username} onChange={(event) => setRepositoryForm({ ...repositoryForm, username: event.target.value })} /></label>
            <label className="span-2">{repositoryForm.auth_type === 'token' ? '令牌' : 'SSH 私钥'}<textarea required rows={repositoryForm.auth_type === 'ssh_key' ? 8 : 3} value={repositoryForm.credential_secret} onChange={(event) => setRepositoryForm({ ...repositoryForm, credential_secret: event.target.value })} /></label>
          </div>}
        </div>}
      </div>
      <label className="simple-check"><input type="checkbox" checked={repositoryForm.webhook_enabled} onChange={(event) => setRepositoryForm({ ...repositoryForm, webhook_enabled: event.target.checked })} />启用 Webhook</label>
      {repositoryFormCheck && <RepositoryCheckResult check={repositoryFormCheck} />}
      <div className="form-actions"><button className="secondary-button" type="button" onClick={closeForm}>取消</button><button className="secondary-button" type="button" disabled={submitting || repositoryFormCheck?.status === 'checking'} onClick={(event) => { if (event.currentTarget.form?.reportValidity()) void testRepositoryForm() }}>{repositoryFormCheck?.status === 'checking' ? '测试中…' : repositoryForm.credential_mode === 'existing' ? '测试现有连接' : '测试'}</button><button className="primary-button" type="submit" disabled={submitting || repositoryFormCheck?.status === 'checking'}>{submitting ? '保存中…' : editingID ? '保存修改' : '创建'}</button></div>
    </form>}

    {formOpen && section === 'build-plans' && <form className="create-sheet" onSubmit={(event) => { event.preventDefault(); void submit('/build-plans', buildForm, () => setBuildForm({ ...buildForm, name: '', description: '', script: '' })) }}>
      <div className="sheet-header"><div><h3>创建构建方案</h3><p>Dockerfile 用于镜像构建，脚本用于已有的打包流程。</p></div><button type="button" onClick={closeForm}>×</button></div><div className="form-grid"><label>名称<input required value={buildForm.name} onChange={(e) => setBuildForm({ ...buildForm, name: e.target.value })} /></label><label>类型<select value={buildForm.kind} onChange={(e) => setBuildForm({ ...buildForm, kind: e.target.value })}><option value="dockerfile">Dockerfile</option><option value="script">打包脚本</option></select></label><label className="span-2">说明<input value={buildForm.description} onChange={(e) => setBuildForm({ ...buildForm, description: e.target.value })} /></label>{buildForm.kind === 'dockerfile' ? <><label>Dockerfile 路径<input required value={buildForm.dockerfile_path} onChange={(e) => setBuildForm({ ...buildForm, dockerfile_path: e.target.value })} /></label><label>构建上下文<input required value={buildForm.context_path} onChange={(e) => setBuildForm({ ...buildForm, context_path: e.target.value })} /></label></> : <label className="span-2">打包脚本<textarea required rows={8} value={buildForm.script} onChange={(e) => setBuildForm({ ...buildForm, script: e.target.value })} placeholder={'set -e\nnpm ci\nnpm run build'} /></label>}<label>超时时间（秒）<input type="number" min={30} max={7200} value={buildForm.timeout_seconds} onChange={(e) => setBuildForm({ ...buildForm, timeout_seconds: Number(e.target.value) })} /></label></div><FormActions submitting={submitting} onCancel={closeForm} />
    </form>}

    {formOpen && section === 'image-registries' && <form className="create-sheet" onSubmit={(event) => { event.preventDefault(); void submit('/image-registries', { ...registryForm, credential: registryForm.credential || null }, () => setRegistryForm({ ...registryForm, name: '', credential: '' })) }}>
      <div className="sheet-header"><div><h3>添加镜像仓库</h3><p>凭据会加密保存，接口不会返回原文。</p></div><button type="button" onClick={closeForm}>×</button></div>
      <div className="form-grid">
        <label>名称
          <input required maxLength={128} value={registryForm.name} onChange={(e) => setRegistryForm({ ...registryForm, name: e.target.value })} placeholder="例如：UCloud 生产仓库" />
          <small className="field-help">仅用于 ZRT 中识别，也可以填写 host/namespace。</small>
        </label>
        <label>类型<select value={registryForm.provider} onChange={(e) => setRegistryForm({ ...registryForm, provider: e.target.value })}><option value="harbor">Harbor</option><option value="docker_hub">Docker Hub</option><option value="generic">通用 Registry</option></select></label>
        <label className="span-2">Registry 地址
          <input required type="url" maxLength={1024} value={registryForm.endpoint} onChange={(e) => setRegistryForm({ ...registryForm, endpoint: e.target.value })} placeholder="https://uhub.service.ucloud.cn" />
          <small className="field-help">填写协议和 Registry 主机，不要包含用户名、密码、查询参数或镜像 Tag。</small>
        </label>
        <label>命名空间
          <input maxLength={255} value={registryForm.namespace} onChange={(e) => setRegistryForm({ ...registryForm, namespace: e.target.value })} placeholder="例如：zrt-application 或 team/project" />
        </label>
        <label>用户名<input maxLength={255} value={registryForm.username} onChange={(e) => setRegistryForm({ ...registryForm, username: e.target.value })} autoComplete="username" /></label>
        <label className="span-2">密码或 Token<input type="password" value={registryForm.credential} onChange={(e) => setRegistryForm({ ...registryForm, credential: e.target.value })} autoComplete="new-password" /></label>
        <label className="span-2 registry-insecure-check"><input type="checkbox" checked={registryForm.allow_insecure_http} onChange={(e) => setRegistryForm({ ...registryForm, allow_insecure_http: e.target.checked })} />允许 HTTP（仅用于可信内网测试环境）</label>
      </div>
      {registryFormCheck && <RegistryCheckResult check={registryFormCheck} />}
      <div className="form-actions"><button className="secondary-button" type="button" onClick={closeForm}>取消</button><button className="secondary-button" type="button" disabled={submitting || registryFormCheck?.status === 'checking'} onClick={(event) => { if (event.currentTarget.form?.reportValidity()) void testRegistryForm() }}>{registryFormCheck?.status === 'checking' ? '测试中…' : '测试'}</button><button className="primary-button" type="submit" disabled={submitting || registryFormCheck?.status === 'checking'}>{submitting ? '保存中…' : '创建'}</button></div>
    </form>}

    {formOpen && section === 'deployment-plans' && <form className="create-sheet" onSubmit={(event) => { event.preventDefault(); void submit('/deployment-plans', deploymentForm, () => setDeploymentForm({ ...deploymentForm, name: '', description: '', script: '', helm_chart: '', helm_values: '', service_name: '' })) }}>
      <div className="sheet-header"><div><h3>创建部署方案</h3><p>选择一种清晰、可审计的部署方式。</p></div><button type="button" onClick={closeForm}>×</button></div><div className="form-grid"><label>名称<input required value={deploymentForm.name} onChange={(e) => setDeploymentForm({ ...deploymentForm, name: e.target.value })} /></label><label>部署方式<select value={deploymentForm.kind} onChange={(e) => setDeploymentForm({ ...deploymentForm, kind: e.target.value })}><option value="helm">Helm</option><option value="compose">Docker Compose</option><option value="docker">Docker</option><option value="script">自定义脚本</option></select></label><label className="span-2">说明<input value={deploymentForm.description} onChange={(e) => setDeploymentForm({ ...deploymentForm, description: e.target.value })} /></label>{deploymentForm.kind === 'helm' && <><label className="span-2">Chart 路径<input required value={deploymentForm.helm_chart} onChange={(e) => setDeploymentForm({ ...deploymentForm, helm_chart: e.target.value })} placeholder="deploy/chart" /></label><label className="span-2">Values 覆盖<textarea rows={6} value={deploymentForm.helm_values} onChange={(e) => setDeploymentForm({ ...deploymentForm, helm_values: e.target.value })} /></label></>}{deploymentForm.kind === 'compose' && <label className="span-2">Compose 文件<input required value={deploymentForm.compose_file} onChange={(e) => setDeploymentForm({ ...deploymentForm, compose_file: e.target.value })} /></label>}{deploymentForm.kind === 'docker' && <label className="span-2">容器服务名<input required value={deploymentForm.service_name} onChange={(e) => setDeploymentForm({ ...deploymentForm, service_name: e.target.value })} /></label>}{deploymentForm.kind === 'script' && <label className="span-2">部署脚本<textarea required rows={8} value={deploymentForm.script} onChange={(e) => setDeploymentForm({ ...deploymentForm, script: e.target.value })} /></label>}<label>超时时间（秒）<input type="number" min={30} max={3600} value={deploymentForm.timeout_seconds} onChange={(e) => setDeploymentForm({ ...deploymentForm, timeout_seconds: Number(e.target.value) })} /></label></div><FormActions submitting={submitting} onCancel={closeForm} />
    </form>}

    {section === 'applications' && <div className="application-grid">{applications.map((application) => <article className="application-card" key={application.id}>
      <div className="card-top"><div className="app-identity"><span className="app-symbol">{application.name.slice(0, 1)}</span><div><h3>{application.name}</h3><p>{application.repository?.name || '未找到仓库'} · {application.branch}</p></div></div><StatusPill value={application.sync_status} /></div>
      <p className="card-description">{application.description || '暂未填写应用说明'}</p>
      <div className="commit-row"><div><span>当前版本</span><strong>{shortSHA(application.last_observed_commit)}</strong></div><div><span>最后检查</span><strong>{formatTime(application.last_checked_at)}</strong></div></div>
      <div className="application-environments">{(application.environments || []).map((environment) => <span key={environment.key}>{environment.key}<small>{environment.branch}</small></span>)}</div>
      <div className="pipeline-flow compact-flow"><span className={application.repository ? 'complete' : ''}>代码</span><i>›</i><span className={application.build_plan ? 'complete' : ''}>构建</span><i>›</i><span className={application.image_registry ? 'complete' : ''}>镜像</span><i>›</i><span className={application.workflow?.is_active ? 'complete' : ''}>{application.workflow_template?.name || (application.workflow?.is_active ? '应用计划' : '计划草稿')}</span><span className={application.release_approval_enabled ? 'review-on' : ''}>{application.release_approval_enabled ? '需审核' : '免审核'}</span></div>
      <div className="card-actions"><button type="button" onClick={() => editApplication(application)}>配置</button><button type="button" onClick={() => navigate(`/release-workflows/editor?application=${application.id}`)}>发布计划</button>{canRun && <><button type="button" onClick={() => void action(`/applications/${application.id}/sync`)}>检查更新</button><button className="accent-action" type="button" onClick={() => void action(`/applications/${application.id}/pipeline-runs`)}>启动流程</button></>}</div>
    </article>)}{!loading && applications.length === 0 && <EmptyState title="还没有应用" description="创建第一个应用，把仓库、构建和发布流程连接起来。" />}</div>}

    {section === 'repositories' && <div className="resource-card-grid">{repositories.map((item) => {
      const check = repositoryChecks[item.id]
      return <article className="resource-card repository-card modern-card" key={item.id}>
        <div className="resource-icon git-icon">⌁</div>
        <div className="repository-card-main">
          <div className="card-title-line"><h3>{item.name}</h3><span>{item.provider}</span></div>
          <p className="resource-url" title={item.clone_url}>{item.clone_url}</p>
          <div className="meta-row"><span>默认分支 {item.default_branch || '—'}</span><span>{item.webhook_enabled ? 'Webhook 已开启' : '仅 Pull'}</span></div>
          {check && <RepositoryCheckResult check={check} />}
          {(canManageRepository || (item.webhook_enabled && canReadRepositorySecret)) && <div className="card-actions repository-actions">
            {canManageRepository && <button type="button" onClick={() => editRepository(item)}>修改</button>}
            {canManageRepository && <button type="button" disabled={!item.is_active || check?.status === 'checking'} title={item.is_active ? '使用当前凭据读取远端分支和标签' : '仓库已停用'} onClick={() => void testRepository(item)}>
              {check?.status === 'checking' ? '测试中…' : '测试'}
            </button>}
            {item.webhook_enabled && canReadRepositorySecret && <button type="button" onClick={() => void showWebhook(item)}>查看 Webhook</button>}
            {canManageRepository && <button className="danger-action" type="button" onClick={() => void deleteRepository(item)}>删除</button>}
          </div>}
        </div>
      </article>
    })}{!loading && repositories.length === 0 && <EmptyState title="还没有代码仓库" description="添加仓库后才能创建应用。" />}</div>}

    {section === 'build-plans' && <div className="resource-card-grid">{buildPlans.map((item) => <article className="resource-card modern-card" key={item.id}><div className="resource-icon build-icon">◇</div><div><div className="card-title-line"><h3>{item.name}</h3><span>{kindLabel(item.kind)}</span></div><p>{item.description || (item.kind === 'dockerfile' ? item.dockerfile_path : '自定义打包脚本')}</p><div className="meta-row"><span>上下文 {item.context_path}</span><span>超时 {item.timeout_seconds} 秒</span></div></div></article>)}{!loading && buildPlans.length === 0 && <EmptyState title="还没有构建方案" description="创建脚本或 Dockerfile 构建方案。" />}</div>}

    {section === 'image-registries' && <div className="resource-card-grid">{registries.map((item) => <article className="resource-card modern-card" key={item.id}><div className="resource-icon registry-icon">▱</div><div><div className="card-title-line"><h3>{item.name}</h3><span>{kindLabel(item.provider)}</span></div><p className="resource-url">{item.endpoint}</p><div className="meta-row"><span>{item.namespace || '根命名空间'}</span><span>{item.has_credential ? '凭据已配置' : '匿名访问'}</span></div></div></article>)}{!loading && registries.length === 0 && <EmptyState title="还没有镜像仓库" description="添加用于推送构建镜像的 Registry。" />}</div>}

    {section === 'deployment-plans' && <div className="resource-card-grid">{deploymentPlans.map((item) => <article className="resource-card modern-card" key={item.id}><div className="resource-icon release-icon">↗</div><div><div className="card-title-line"><h3>{item.name}</h3><span>{kindLabel(item.kind)}</span></div><p>{item.description || item.helm_chart || item.compose_file || item.service_name || '自定义部署脚本'}</p><div className="meta-row"><span>超时 {item.timeout_seconds} 秒</span><span>已启用</span></div></div></article>)}{!loading && deploymentPlans.length === 0 && <EmptyState title="还没有部署方案" description="创建 Helm、Compose、Docker 或脚本部署模板。" />}</div>}

    {section === 'pipelines' && <div className="pipeline-list">{runs.map((run) => <article className="pipeline-run" key={run.id}><div className="run-status-dot" /><div className="run-main"><div className="card-title-line"><h3>{run.application?.name || run.application_id}</h3><StatusPill value={run.status} /></div><div className="run-details"><span>{run.environment || '未进入环境'}</span><span>{run.trigger}</span><span>{run.ref || '尚无代码引用'}</span><span>{shortSHA(run.commit_sha)}</span></div><p>{run.message || '代码变更已记录'}</p>{run.current_node_id && <div className="run-actions">{run.status === 'awaiting_approval' && canReview && <button type="button" onClick={() => void runAction(`/pipeline-runs/${run.id}/approve`)}>通过审核</button>}{run.status !== 'awaiting_approval' && run.status !== 'succeeded' && canRun && <button type="button" onClick={() => void runAction(`/pipeline-runs/${run.id}/advance`, { target_node_id: '' })}>{run.status === 'ready' ? '完成部署并继续' : '推进下一步'}</button>}</div>}</div><time>{formatTime(run.created_at)}</time></article>)}{!loading && runs.length === 0 && <EmptyState title="还没有流水线记录" description="应用检测到代码变化后会显示在这里。" />}</div>}
  </section>
}
