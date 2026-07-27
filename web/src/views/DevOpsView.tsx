import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'

import client from '@/api/client'
import { apiErrorMessage, type ResourceRecord } from '@/api/resources'
import ResourceSelectField from '@/components/ResourceSelectField'
import ResourceTable from '@/components/ResourceTable'
import { useAuthStore } from '@/stores/auth'

type Section = 'applications' | 'repositories' | 'build-plans' | 'image-registries' | 'deployment-plans' | 'release-plans'

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
interface ManualRunSource { id: string; name: string; environment?: string }
interface RepositoryRefResult { branches: GitRef[]; tags: GitRef[]; manual_sources?: ManualRunSource[] }
interface CommitOption { ref: string; name: string; sha: string; kind: 'branch' | 'tag' }
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
interface WorkflowTemplate { id: string; name: string; description: string; revision: number; is_active: boolean }
interface ApplicationEnvironment {
  id?: string; key: 'dev' | 'test' | 'pre' | 'prod'; name: string; branch: string
  poll_enabled: boolean; watch_push: boolean; watch_pull_request: boolean; watch_tags: boolean
  tag_pattern: string; deployment_plan_id?: string; deployment_target_id?: string; sort_order: number
}
interface Application {
  id: string; name: string; description: string; repository_id: string; branch: string
  poll_enabled: boolean; poll_interval_seconds: number; watch_push: boolean; watch_pull_request: boolean
  watch_tags: boolean; tag_pattern: string; build_plan_id?: string; image_registry_id?: string
  deployment_plan_id?: string; deployment_target_id?: string; last_observed_ref?: string
  last_observed_commit?: string; sync_status: string; sync_message?: string; last_checked_at?: string
  is_active: boolean; repository?: Repository; build_plan?: BuildPlan; image_registry?: ImageRegistry
  deployment_plan?: DeploymentPlan
  release_approval_enabled: boolean; environments?: ApplicationEnvironment[]
  workflow_template_id?: string; workflow_template?: WorkflowTemplate
  workflow?: { id: string; is_active: boolean; revision: number }
}
interface PipelineRunRepository {
  id: string; repository_id: string; sort_order: number; ref?: string; commit_sha?: string
  build_plan_id?: string; deployment_plan_id?: string; status: string; repository: Repository
}
interface PipelineRun {
  id: string; application_id: string; trigger: string; ref: string; commit_sha: string
  status: string; stage: string; message?: string; created_at: string; application?: Application
  environment?: string; current_node_id?: string; approved_by?: string
  workflow_id?: string; workflow_revision?: number; approval_required?: boolean
  retry_of_id?: string; execution_job_id?: string; deployment_id?: string; image?: string
  repositories?: PipelineRunRepository[]
}
interface ReleaseGroupApplication { id: string; application_id: string; sort_order: number; application: Application }
interface ReleaseGroupDependency { release_group_id: string; depends_on_group_id: string }
interface ReleaseGroup {
  id: string; release_plan_id: string; name: string; mode: 'parallel' | 'sequential'
  failure_policy: 'stop' | 'continue'; sort_order: number
  applications: ReleaseGroupApplication[]; dependencies: ReleaseGroupDependency[]
}
interface ReleasePlan {
  id: string; name: string; version: string; description: string
  status: 'draft' | 'active' | 'completed' | 'canceled'; created_at: string; groups: ReleaseGroup[]
}

const sectionCopy: Record<Section, { title: string; description: string }> = {
  applications: { title: '应用', description: '一个应用对应一个代码仓库，并维护自己的构建、部署和流水线配置。' },
  repositories: { title: '代码仓库', description: '统一管理 Git 连接、个人令牌和 Webhook。构建与部署方案由应用选择。' },
  'build-plans': { title: '构建方案', description: '保存可复用的打包脚本或 Dockerfile 构建配置。' },
  'image-registries': { title: '镜像仓库', description: '管理 Harbor、Docker Hub 或其他兼容 Registry。' },
  'deployment-plans': { title: '部署方案', description: '定义部署节点如何通过 Helm、Docker Compose、Docker 或受控脚本执行。' },
  'release-plans': { title: '发布计划', description: '按迭代、版本或发布列车组织发布组，并编排多个应用。' },
}

const defaultEnvironments: Array<ApplicationEnvironment & { enabled: boolean }> = [
  { key: 'dev', name: '开发环境', branch: 'dev', enabled: true, poll_enabled: true, watch_push: true, watch_pull_request: false, watch_tags: false, tag_pattern: 'v*', deployment_plan_id: '', deployment_target_id: '', sort_order: 0 },
  { key: 'test', name: '测试环境', branch: 'test', enabled: true, poll_enabled: false, watch_push: true, watch_pull_request: true, watch_tags: false, tag_pattern: 'v*', deployment_plan_id: '', deployment_target_id: '', sort_order: 1 },
  { key: 'pre', name: '预发布环境', branch: 'main', enabled: true, poll_enabled: false, watch_push: true, watch_pull_request: true, watch_tags: false, tag_pattern: 'v*', deployment_plan_id: '', deployment_target_id: '', sort_order: 2 },
  { key: 'prod', name: '生产环境', branch: 'release', enabled: true, poll_enabled: false, watch_push: false, watch_pull_request: false, watch_tags: true, tag_pattern: 'v*', deployment_plan_id: '', deployment_target_id: '', sort_order: 3 },
]

function initialApplicationForm() {
  return {
    name: '', description: '', repository_id: '', branch: 'dev', poll_enabled: true,
    poll_interval_seconds: 3, watch_push: true, watch_pull_request: false,
    watch_tags: false, tag_pattern: 'v*', build_plan_id: '', image_registry_id: '',
    deployment_plan_id: '', deployment_target_id: '', release_approval_enabled: true,
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
  const label = ({ draft: '草稿', active: '已启用', completed: '已完成', idle: '等待检查', checking: '检查中', synced: '已同步', changed: '发现更新', failed: '失败', detected: '发现变更', ready: '等待部署', blocked: '配置不完整', awaiting_approval: '等待审核', running: '正在执行', succeeded: '发布成功', canceled: '已取消' } as Record<string, string>)[value] || value
  return <span className={`status-pill status-${value}`}>{label}</span>
}

function blockedRunReasons(run: PipelineRun, application?: Application) {
  if (run.status !== 'blocked') return []
  const reasons: string[] = []
  if (!application?.repository_id) reasons.push('未绑定代码仓库。')
  if (!run.commit_sha) reasons.push('尚未选择代码版本：点击“选择版本”，选择仓库 Commit。')
  if (application) {
    if (!application.build_plan_id) reasons.push('应用未绑定构建方案。')
    if (!application.deployment_plan_id) reasons.push('应用未绑定部署方案。')
    if (!application.workflow?.is_active) reasons.push('应用流水线尚未启用，或仍有节点配置问题。')
  }
  if (reasons.length === 0) reasons.push(run.message || '应用的构建或发布配置不完整。')
  return [...new Set(reasons)]
}

function applicationCanExecute(application?: Application) {
	return Boolean(application?.repository_id && application.build_plan_id && application.deployment_plan_id && application.workflow?.is_active)
}

function EmptyState({ title, description }: { title: string; description: string }) {
  return <div className="empty-state modern-empty"><span className="empty-icon">＋</span><h3>{title}</h3><p>{description}</p></div>
}

function FormActions({ submitting, editing, onCancel }: { submitting: boolean; editing?: boolean; onCancel: () => void }) {
  return <div className="form-actions"><button className="secondary-button" type="button" onClick={onCancel}>取消</button><button className="primary-button" type="submit" disabled={submitting}>{submitting ? '保存中…' : editing ? '保存修改' : '创建'}</button></div>
}

const deploymentMethodHelp: Record<string, { title: string; description: string; items: string[] }> = {
  helm: {
    title: 'Helm 部署',
    description: '用于 Kubernetes 应用，通过仓库中的 Chart 描述工作负载、Service 和配置。',
    items: ['Chart 路径填写代码仓库中的相对目录，例如 deploy/chart。', 'Values 覆盖填写需要叠加的 YAML；留空时使用 Chart 默认值。', '不要在 Values 中保存密码、Token 或 kubeconfig。'],
  },
  compose: {
    title: 'Docker Compose 部署',
    description: '用于由 Compose 文件统一管理的一组容器，服务名称从 Compose 文件的 services 段读取。',
    items: ['Compose 文件填写仓库内的相对路径，例如 deploy/compose.prod.yml。', '选择这种方式时不需要再填写 Docker 工作负载名称。', '镜像版本应由流水线运行传入，不建议硬编码固定 Tag。'],
  },
  docker: {
    title: 'Docker 部署',
    description: '用于标识由 Docker 方式管理的单个容器或 Docker Service，不是镜像名称。',
    items: ['填写稳定的容器名称，例如 order-api、zrt-api 或 web-prod。', '不要填写镜像地址、Tag、容器 ID、端口、服务器 IP 或域名。', '如果实际由 docker compose 管理，请改选“Docker Compose”。', '流水线发布时会在所选 SSH 主机拉取不可变镜像，并以受控方式替换这个容器。'],
  },
  script: {
    title: '自定义脚本部署',
    description: '用于无法由 Helm、Compose 或 Docker 标准方式表达的受控部署步骤。',
    items: ['脚本应支持重复执行，并在失败时返回非零退出码。', '不要在脚本中写入明文凭据，也不要依赖交互式输入。', '当前流水线不会在 ZRT 主进程中直接执行任意脚本。'],
  },
}

function DeploymentMethodHelp({ kind }: { kind: string }) {
  const help = deploymentMethodHelp[kind]
  if (!help) return null
  return <aside className="deployment-method-help span-2" aria-live="polite">
    <div><span>填写帮助</span><strong>{help.title}</strong></div>
    <p>{help.description}</p>
    <ul>{help.items.map((item) => <li key={item}>{item}</li>)}</ul>
  </aside>
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
  const [releasePlans, setReleasePlans] = useState<ReleasePlan[]>([])
  const [workflowTemplates, setWorkflowTemplates] = useState<WorkflowTemplate[]>([])
  const [runs, setRuns] = useState<PipelineRun[]>([])
  const [deployments, setDeployments] = useState<ResourceRecord[]>([])
  const [formOpen, setFormOpen] = useState(false)
  const [editingID, setEditingID] = useState('')
  const [loading, setLoading] = useState(true)
  const [submitting, setSubmitting] = useState(false)
  const [retryingRunID, setRetryingRunID] = useState('')
  const [error, setError] = useState('')
  const [webhookSetup, setWebhookSetup] = useState<{ url: string; secret: string } | null>(null)
  const [repositoryChecks, setRepositoryChecks] = useState<Record<string, RepositoryCheck>>({})
  const [repositoryFormCheck, setRepositoryFormCheck] = useState<RepositoryCheck | null>(null)
  const [registryFormCheck, setRegistryFormCheck] = useState<RepositoryCheck | null>(null)
  const [commitPicker, setCommitPicker] = useState<{ run: PipelineRun; repositoryName: string; options: CommitOption[]; selectedRef: string; sources: ManualRunSource[]; selectedSourceID: string; loading: boolean } | null>(null)
  const [commitSubmitting, setCommitSubmitting] = useState(false)
  const registryTestSequence = useRef(0)
  const [applicationForm, setApplicationForm] = useState(initialApplicationForm)
  const [repositoryForm, setRepositoryForm] = useState(initialRepositoryForm)
  const [buildForm, setBuildForm] = useState({ name: '', kind: 'dockerfile', description: '', script: '', dockerfile_path: 'Dockerfile', context_path: '.', artifact_path: '', timeout_seconds: 1800 })
  const [registryForm, setRegistryForm] = useState({ name: '', provider: 'harbor', endpoint: 'https://', namespace: '', username: '', credential: '', allow_insecure_http: false })
  const [deploymentForm, setDeploymentForm] = useState({ name: '', kind: 'helm', description: '', script: '', helm_chart: '', helm_values: '', compose_file: 'docker-compose.yml', service_name: '', timeout_seconds: 600 })
  const [releasePlanForm, setReleasePlanForm] = useState({ name: '', version: '', description: '' })
  const [groupEditor, setGroupEditor] = useState<{ planID: string; groupID: string; name: string; mode: 'parallel' | 'sequential'; failure_policy: 'stop' | 'continue'; application_ids: string[]; depends_on_group_ids: string[] } | null>(null)
  const copy = sectionCopy[section]
  const requestedReleaseView = searchParams.get('view')
  const releaseView = canReadDeployment && (requestedReleaseView === 'records' || !canReadDelivery) ? 'records' : requestedReleaseView === 'runs' ? 'runs' : 'plans'
  const canCreate = section === 'repositories' ? canManageRepository : canManageDelivery

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
      const deploymentRecordRequest = canReadDeployment
        ? client.get<{ deployments: ResourceRecord[] }>('/deployments')
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
      const releasePlanRequest = canReadDelivery
        ? client.get<{ release_plans: ReleasePlan[] }>('/release-plans')
        : Promise.resolve(null)
      const workflowTemplateRequest = canReadDelivery
        ? client.get<{ workflow_templates: WorkflowTemplate[] }>('/workflow-templates')
        : Promise.resolve(null)
      const [appResult, repoResult, credentialResult, buildResult, registryResult, deploymentPlanResult, deploymentRecordResult, runResult, releasePlanResult, workflowTemplateResult] = await Promise.all([
        applicationRequest,
        repositoryRequest,
        credentialRequest,
        buildRequest,
        registryRequest,
        deploymentPlanRequest,
        deploymentRecordRequest,
        runRequest,
        releasePlanRequest,
        workflowTemplateRequest,
      ])
      setApplications(appResult?.data.applications || [])
      setRepositories(repoResult?.data.repositories || [])
      setCredentials(credentialResult?.data.credentials || [])
      setBuildPlans(buildResult?.data.build_plans || [])
      setRegistries(registryResult?.data.image_registries || [])
      setDeploymentPlans(deploymentPlanResult?.data.deployment_plans || [])
      setDeployments(deploymentRecordResult?.data.deployments || [])
      setRuns(runResult?.data.pipeline_runs || [])
      setReleasePlans(releasePlanResult?.data.release_plans || [])
      setWorkflowTemplates(workflowTemplateResult?.data.workflow_templates || [])
    } catch (loadError) {
      setError(apiErrorMessage(loadError))
    } finally {
      setLoading(false)
    }
  }, [canReadCredential, canReadDelivery, canReadDeployment, canReadRepository])

  useEffect(() => { void refresh() }, [refresh])
  useEffect(() => {
    if (section !== 'release-plans' || releaseView !== 'runs' || !runs.some((run) => run.status === 'running')) return
    const timer = window.setInterval(() => {
      void Promise.all([
        client.get<{ pipeline_runs: PipelineRun[] }>('/pipeline-runs'),
        canReadDeployment ? client.get<{ deployments: ResourceRecord[] }>('/deployments') : Promise.resolve(null),
      ]).then(([runResult, deploymentResult]) => {
        setRuns(runResult.data.pipeline_runs || [])
        if (deploymentResult) setDeployments(deploymentResult.data.deployments || [])
      }).catch(() => undefined)
    }, 2000)
    return () => window.clearInterval(timer)
  }, [canReadDeployment, releaseView, runs, section])
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
  useEffect(() => {
    const applicationToEdit = searchParams.get('edit')
    if (section !== 'applications' || !canManageDelivery || !applicationToEdit) return
    const application = applications.find((item) => item.id === applicationToEdit)
    if (!application) return
    editApplication(application)
    const next = new URLSearchParams(searchParams)
    next.delete('edit')
    setSearchParams(next, { replace: true })
  }, [applications, canManageDelivery, searchParams, section, setSearchParams])

  const counts = useMemo(() => ({
    configured: applications.filter((item) => item.repository_id && item.build_plan_id && item.deployment_plan_id && item.workflow?.is_active).length,
    changed: applications.filter((item) => item.sync_status === 'changed').length,
  }), [applications])

  function closeForm() {
	registryTestSequence.current += 1
	setRegistryFormCheck(null)
    setFormOpen(false)
    setEditingID('')
    setApplicationForm(initialApplicationForm())
    setRepositoryForm(initialRepositoryForm())
    setReleasePlanForm({ name: '', version: '', description: '' })
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

  function openApplicationCreateForm() {
    setEditingID('')
    setApplicationForm(initialApplicationForm())
    setError('')
    setFormOpen(true)
    window.scrollTo({ top: 0, behavior: 'smooth' })
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
      tag_pattern: application.tag_pattern || 'v*',
      build_plan_id: application.build_plan_id || '', image_registry_id: application.image_registry_id || '', deployment_plan_id: application.deployment_plan_id || '',
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
	if (!applicationForm.repository_id) {
	  setError('请选择代码仓库')
	  return
	}
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
      const message = apiErrorMessage(actionError)
      await refresh()
      setError(message)
    }
  }

  async function createPipelineRun(applicationID: string) {
	setError('')
	try {
	  await client.post(`/applications/${applicationID}/pipeline-runs`)
	  navigate('/release-plans?view=runs')
	} catch (actionError) {
	  const message = apiErrorMessage(actionError)
	  await refresh()
	  setError(message)
	}
  }

  function openGroupEditor(plan: ReleasePlan, group?: ReleaseGroup) {
    setGroupEditor({
      planID: plan.id,
      groupID: group?.id || '',
      name: group?.name || '',
      mode: group?.mode || 'parallel',
      failure_policy: group?.failure_policy || 'stop',
      application_ids: group?.applications?.map((item) => item.application_id) || [],
      depends_on_group_ids: group?.dependencies?.map((item) => item.depends_on_group_id) || [],
    })
  }

  async function saveReleaseGroup() {
    if (!groupEditor) return
    setSubmitting(true)
    setError('')
    try {
      const endpoint = `/release-plans/${groupEditor.planID}/groups${groupEditor.groupID ? `/${groupEditor.groupID}` : ''}`
      const payload = {
        name: groupEditor.name,
        mode: groupEditor.mode,
        failure_policy: groupEditor.failure_policy,
        application_ids: groupEditor.application_ids,
        depends_on_group_ids: groupEditor.depends_on_group_ids,
      }
      if (groupEditor.groupID) await client.put(endpoint, payload)
      else await client.post(endpoint, payload)
      setGroupEditor(null)
      await refresh()
    } catch (saveError) {
      setError(apiErrorMessage(saveError))
    } finally {
      setSubmitting(false)
    }
  }

  async function deleteReleaseGroup(plan: ReleasePlan, group: ReleaseGroup) {
    if (!window.confirm(`确认删除发布组“${group.name}”？`)) return
    setError('')
    try {
      await client.delete(`/release-plans/${plan.id}/groups/${group.id}`)
      setGroupEditor((current) => current?.groupID === group.id ? null : current)
      await refresh()
    } catch (deleteError) {
      setError(apiErrorMessage(deleteError))
    }
  }

  async function activateReleasePlan(plan: ReleasePlan) {
    setError('')
    try {
      await client.put(`/release-plans/${plan.id}`, { name: plan.name, version: plan.version, description: plan.description, status: 'active' })
      await refresh()
    } catch (updateError) {
      setError(apiErrorMessage(updateError))
    }
  }

  async function deleteReleasePlan(plan: ReleasePlan) {
    if (!window.confirm(`确认删除发布计划“${plan.name}”？`)) return
    setError('')
    try {
      await client.delete(`/release-plans/${plan.id}`)
      setGroupEditor((current) => current?.planID === plan.id ? null : current)
      await refresh()
    } catch (deleteError) {
      setError(apiErrorMessage(deleteError))
    }
  }

  async function runAction(endpoint: string, payload: unknown = undefined) {
	setError('')
	try {
	  await client.post(endpoint, payload)
	  await refresh()
	} catch (actionError) {
	  const message = apiErrorMessage(actionError)
	  await refresh()
	  setError(message)
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

  async function openCommitPicker(run: PipelineRun) {
	setError('')
	const application = applications.find((item) => item.id === run.application_id) || run.application
	setCommitPicker({ run, repositoryName: application?.repository?.name || '代码仓库', options: [], selectedRef: '', sources: [], selectedSourceID: '', loading: true })
	try {
	  const response = await client.get<RepositoryRefResult>(`/applications/${run.application_id}/repository-refs`, { timeout: 35_000 })
	  const options: CommitOption[] = [
		...(response.data.branches || []).map((item) => ({ ref: `refs/heads/${item.name}`, name: item.name, sha: item.sha, kind: 'branch' as const })),
		...(response.data.tags || []).map((item) => ({ ref: `refs/tags/${item.name}`, name: item.name, sha: item.sha, kind: 'tag' as const })),
	  ]
	  const preferredRef = `refs/heads/${application?.repository?.default_branch || application?.branch || 'main'}`
	  const sources = response.data.manual_sources || []
	  setCommitPicker((current) => current?.run.id === run.id ? {
		run, repositoryName: application?.repository?.name || '代码仓库', options,
		selectedRef: options.some((item) => item.ref === preferredRef) ? preferredRef : options[0]?.ref || '',
		sources, selectedSourceID: sources[0]?.id || '', loading: false,
	  } : current)
	} catch (requestError) {
	  setCommitPicker(null)
	  setError(apiErrorMessage(requestError))
	}
  }

  async function executePipelineRun(run: PipelineRun) {
	if (!run.commit_sha) {
	  await openCommitPicker(run)
	  return
	}
	await runAction(`/pipeline-runs/${run.id}/execute`)
  }

  async function retryPipelineRun(run: PipelineRun) {
    setRetryingRunID(run.id)
    setError('')
    try {
      await client.post(`/pipeline-runs/${run.id}/retry`)
      await refresh()
    } catch (requestError) {
      setError(apiErrorMessage(requestError))
    } finally {
      setRetryingRunID('')
    }
  }

  async function confirmCommitExecution() {
	if (!commitPicker) return
	const selected = commitPicker.options.find((item) => item.ref === commitPicker.selectedRef)
	if (!selected) return
	setCommitSubmitting(true)
	setError('')
	try {
	  await client.post(`/pipeline-runs/${commitPicker.run.id}/execute`, { ref: selected.ref, commit_sha: selected.sha, source_node_id: commitPicker.selectedSourceID })
	  setCommitPicker(null)
	  await refresh()
	} catch (requestError) {
	  setError(apiErrorMessage(requestError))
	} finally {
	  setCommitSubmitting(false)
	}
  }

  async function deletePipelineRun(run: PipelineRun) {
	const applicationName = applications.find((item) => item.id === run.application_id)?.name || run.application?.name || run.application_id
	if (!window.confirm(`确认删除“${applicationName}”的这次流水线运行？\n\n运行数据删除后无法恢复；独立的发布记录不会被删除。`)) return
	setError('')
	try {
	  await client.delete(`/pipeline-runs/${run.id}`)
	  await refresh()
	} catch (deleteError) {
	  setError(apiErrorMessage(deleteError))
	  await refresh()
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
        {canCreate && section !== 'applications' && (section !== 'release-plans' || releaseView === 'plans') && <button className="primary-button" type="button" onClick={toggleCreateForm}>＋ 创建{copy.title.replace('代码', '')}</button>}
      </div>
    </div>

    {section === 'applications' && <nav className="application-subnav" aria-label="应用页面">
      <button type="button" className={!formOpen ? 'active' : ''} aria-current={!formOpen ? 'page' : undefined} onClick={closeForm}>应用列表</button>
      {canManageDelivery && <button type="button" className={formOpen && !editingID ? 'active' : ''} aria-current={formOpen && !editingID ? 'page' : undefined} onClick={openApplicationCreateForm}>创建应用</button>}
      {formOpen && editingID && <button type="button" className="active" aria-current="page">编辑应用<span>{applicationForm.name}</span></button>}
    </nav>}

    {section === 'release-plans' && <div className="release-view-tabs" role="tablist" aria-label="发布数据">
      {canReadDelivery && <button type="button" role="tab" aria-selected={releaseView === 'plans'} className={releaseView === 'plans' ? 'active' : ''} onClick={() => { const next = new URLSearchParams(searchParams); next.delete('view'); setSearchParams(next) }}>发布计划</button>}
      {canReadDelivery && <button type="button" role="tab" aria-selected={releaseView === 'runs'} className={releaseView === 'runs' ? 'active' : ''} onClick={() => { const next = new URLSearchParams(searchParams); next.set('view', 'runs'); setSearchParams(next) }}>流水线运行</button>}
      {canReadDeployment && <button type="button" role="tab" aria-selected={releaseView === 'records'} className={releaseView === 'records' ? 'active' : ''} onClick={() => { const next = new URLSearchParams(searchParams); next.set('view', 'records'); setSearchParams(next) }}>发布记录</button>}
    </div>}

    {section === 'applications' && !formOpen && <div className="summary-strip">
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
      <div className="sheet-header"><div><h3>{editingID ? '编辑应用' : '创建应用'}</h3><p>选择流水线方案后，环境、分支和执行路径会复制到应用流水线。</p></div><button type="button" onClick={closeForm}>×</button></div>
      <div className="form-grid">
        <label>应用名称<input required value={applicationForm.name} onChange={(e) => setApplicationForm({ ...applicationForm, name: e.target.value })} placeholder="例如：订单服务" /></label>
        <label>说明<input value={applicationForm.description} onChange={(e) => setApplicationForm({ ...applicationForm, description: e.target.value })} placeholder="这个应用负责什么" /></label>
        <ResourceSelectField id="application-repository" label="代码仓库" createLabel="代码仓库" createTo={canManageRepository ? '/repositories?create=1' : undefined} wrapperClassName="span-2" help="一个应用只对应一个代码仓库；构建方案和部署方案由应用维护。" required value={applicationForm.repository_id} onChange={(event) => setApplicationForm({ ...applicationForm, repository_id: event.target.value })}><option value="">请选择代码仓库</option>{repositories.filter((item) => item.is_active).map((item) => <option key={item.id} value={item.id}>{item.name} · {item.default_branch || '未设置默认分支'}</option>)}</ResourceSelectField>
        <ResourceSelectField id="application-build-plan" label="构建方案" createLabel="构建方案" createTo={canManageDelivery ? '/build-plans?create=1' : undefined} required value={applicationForm.build_plan_id} onChange={(event) => setApplicationForm({ ...applicationForm, build_plan_id: event.target.value })}><option value="">请选择构建方案</option>{buildPlans.filter((item) => item.is_active).map((item) => <option key={item.id} value={item.id}>{item.name} · {kindLabel(item.kind)}</option>)}</ResourceSelectField>
        <ResourceSelectField id="application-deployment-plan" label="部署方案" createLabel="部署方案" createTo={canManageDelivery ? '/deployment-plans?create=1' : undefined} required value={applicationForm.deployment_plan_id} onChange={(event) => setApplicationForm({ ...applicationForm, deployment_plan_id: event.target.value })}><option value="">请选择部署方案</option>{deploymentPlans.filter((item) => item.is_active).map((item) => <option key={item.id} value={item.id}>{item.name} · {kindLabel(item.kind)}</option>)}</ResourceSelectField>
        <ResourceSelectField id="application-image-registry" label="镜像仓库" createLabel="镜像仓库" createTo={canManageDelivery ? '/image-registries?create=1' : undefined} help="可选；镜像统一由 Docker-in-Docker 构建。未绑定时通过 SSH 将 docker save 流加载到 Docker 主机；Kubernetes 发布仍需镜像仓库。" value={applicationForm.image_registry_id} onChange={(event) => setApplicationForm({ ...applicationForm, image_registry_id: event.target.value })}><option value="">不绑定（SSH 传输本地镜像）</option>{registries.filter((item) => item.is_active).map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</ResourceSelectField>
        <ResourceSelectField id="application-workflow-template" label="流水线方案" createLabel="流水线方案" createTo={canManageDelivery ? '/pipeline-plans/editor?create=1' : undefined} wrapperClassName="span-2" help="环境、监听分支、触发事件和部署节点都在无限画布中配置。" required={!editingID} value={applicationForm.workflow_template_id} onChange={(event) => setApplicationForm({ ...applicationForm, workflow_template_id: event.target.value })}><option value="">{editingID ? '保留应用当前自定义流水线' : '请选择流水线方案'}</option>{workflowTemplates.filter((item) => item.is_active).map((item) => <option key={item.id} value={item.id}>{item.name} · 第 {item.revision} 版</option>)}</ResourceSelectField>
        <label>Pull 检查间隔<select value={applicationForm.poll_interval_seconds} onChange={(e) => setApplicationForm({ ...applicationForm, poll_interval_seconds: Number(e.target.value) })}><option value={3}>3 秒</option><option value={5}>5 秒</option><option value={10}>10 秒</option><option value={60}>1 分钟</option></select></label>
      </div>
      <div className="form-block"><span className="form-block-title">发布审核</span><div className="approval-choice">
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
      <div className="sheet-header"><div><h3>创建部署方案</h3><p>选择一种清晰、可审计的部署方式；切换方式后可查看对应填写说明。</p></div><button type="button" onClick={closeForm}>×</button></div><div className="form-grid"><label>名称<input required value={deploymentForm.name} onChange={(e) => setDeploymentForm({ ...deploymentForm, name: e.target.value })} placeholder="例如：生产环境 Docker 部署" /></label><label>部署方式<select value={deploymentForm.kind} onChange={(e) => setDeploymentForm({ ...deploymentForm, kind: e.target.value })}><option value="helm">Helm</option><option value="compose">Docker Compose</option><option value="docker">Docker</option><option value="script">自定义脚本</option></select></label><label className="span-2">说明<input value={deploymentForm.description} onChange={(e) => setDeploymentForm({ ...deploymentForm, description: e.target.value })} placeholder="说明适用的应用、环境和用途" /></label><DeploymentMethodHelp kind={deploymentForm.kind} />{deploymentForm.kind === 'helm' && <><label className="span-2">Chart 路径<input required value={deploymentForm.helm_chart} onChange={(e) => setDeploymentForm({ ...deploymentForm, helm_chart: e.target.value })} placeholder="deploy/chart" /><small className="field-help">代码仓库中的相对目录，不是本机绝对路径或 Chart 仓库 URL。</small></label><label className="span-2">Values 覆盖<textarea rows={6} value={deploymentForm.helm_values} onChange={(e) => setDeploymentForm({ ...deploymentForm, helm_values: e.target.value })} placeholder={'replicaCount: 2\nresources:\n  limits:\n    cpu: 500m'} /><small className="field-help">可选的 YAML 覆盖内容，请勿填写明文凭据。</small></label></>}{deploymentForm.kind === 'compose' && <label className="span-2">Compose 文件<input required value={deploymentForm.compose_file} onChange={(e) => setDeploymentForm({ ...deploymentForm, compose_file: e.target.value })} placeholder="deploy/compose.prod.yml" /><small className="field-help">填写代码仓库内的相对文件路径，服务名称由文件中的 services 配置决定。</small></label>}{deploymentForm.kind === 'docker' && <label className="span-2">Docker 工作负载名称<input required maxLength={255} aria-describedby="docker-workload-help" value={deploymentForm.service_name} onChange={(e) => setDeploymentForm({ ...deploymentForm, service_name: e.target.value })} placeholder="例如：order-api" /><small className="field-help" id="docker-workload-help">填写容器或 Docker Service 的稳定逻辑名称；不要填写镜像地址、容器 ID、端口或服务器地址。</small></label>}{deploymentForm.kind === 'script' && <label className="span-2">部署脚本<textarea required rows={8} value={deploymentForm.script} onChange={(e) => setDeploymentForm({ ...deploymentForm, script: e.target.value })} placeholder={'set -e\n# 在受控执行器中执行部署步骤'} /><small className="field-help">脚本应可重复执行、无需交互，并通过退出码明确表示成功或失败。</small></label>}<label>超时时间（秒）<input type="number" min={30} max={3600} value={deploymentForm.timeout_seconds} onChange={(e) => setDeploymentForm({ ...deploymentForm, timeout_seconds: Number(e.target.value) })} /><small className="field-help">允许范围 30–3600 秒，超时后任务会停止并记录失败。</small></label></div><FormActions submitting={submitting} onCancel={closeForm} />
    </form>}

    {formOpen && section === 'release-plans' && <form className="create-sheet" onSubmit={(event) => { event.preventDefault(); void submit('/release-plans', releasePlanForm, () => setReleasePlanForm({ name: '', version: '', description: '' })) }}>
      <div className="sheet-header"><div><h3>创建发布计划</h3><p>发布计划表示一次迭代、版本或发布列车，创建后在计划内部添加发布组。</p></div><button type="button" onClick={closeForm}>×</button></div>
      <div className="form-grid">
        <label>计划名称<input required maxLength={128} value={releasePlanForm.name} onChange={(event) => setReleasePlanForm({ ...releasePlanForm, name: event.target.value })} placeholder="例如：八月版本发布" /></label>
        <label>版本<input required maxLength={64} pattern="[A-Za-z0-9][A-Za-z0-9._/-]*" value={releasePlanForm.version} onChange={(event) => setReleasePlanForm({ ...releasePlanForm, version: event.target.value })} placeholder="2026.08" /></label>
        <label className="span-2">说明<input maxLength={500} value={releasePlanForm.description} onChange={(event) => setReleasePlanForm({ ...releasePlanForm, description: event.target.value })} placeholder="本次迭代包含的范围和发布目标" /></label>
      </div>
      <FormActions submitting={submitting} onCancel={closeForm} />
    </form>}

    {section === 'applications' && !formOpen && <div className="application-grid">{applications.map((application) => <article className="application-card" key={application.id}>
      <div className="card-top"><div className="app-identity"><span className="app-symbol">{application.name.slice(0, 1)}</span><div><h3>{application.name}</h3><p>{application.repository?.name || '未找到仓库'}</p></div></div><StatusPill value={application.sync_status} /></div>
      <p className="card-description">{application.description || '暂未填写应用说明'}</p>
      <div className="commit-row"><div><span>当前版本</span><strong>{shortSHA(application.last_observed_commit)}</strong></div><div><span>最后检查</span><strong>{formatTime(application.last_checked_at)}</strong></div></div>
      <div className="application-environments">{(application.environments || []).map((environment) => <span key={environment.key}>{environment.key}<small>{environment.branch}</small></span>)}</div>
      <div className="pipeline-flow compact-flow"><span className={application.repository_id ? 'complete' : ''}>代码仓库</span><i>›</i><span className={application.build_plan_id && application.deployment_plan_id ? 'complete' : ''}>应用方案</span><i>›</i><span className="complete">{application.image_registry ? '构建并推送' : 'DinD 构建并 SSH 传输'}</span><i>›</i><span className={application.workflow?.is_active ? 'complete' : ''}>{application.workflow_template?.name || (application.workflow?.is_active ? '应用流水线' : '流水线草稿')}</span><span className={application.release_approval_enabled ? 'review-on' : ''}>{application.release_approval_enabled ? '需审核' : '免审核'}</span></div>
      <div className="card-actions"><button type="button" onClick={() => editApplication(application)}>配置</button><button type="button" onClick={() => navigate(`/pipeline-plans/editor?application=${application.id}`)}>应用流水线</button>{canRun && <><button type="button" onClick={() => void action(`/applications/${application.id}/sync`)}>检查更新</button><button className="accent-action" type="button" onClick={() => void createPipelineRun(application.id)}>手动执行</button></>}</div>
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

    {section === 'release-plans' && releaseView === 'plans' && <div className="release-plan-grid">{releasePlans.map((plan) => <article className="release-plan-card" key={plan.id}>
      <header>
        <div><span className="release-plan-version">{plan.version}</span><h3>{plan.name}</h3><p>{plan.description || '暂未填写本次迭代的发布范围。'}</p></div>
        <StatusPill value={plan.status} />
      </header>
      <div className="release-group-list">{(plan.groups || []).map((group) => {
        const dependencyNames = (group.dependencies || []).map((dependency) => plan.groups.find((item) => item.id === dependency.depends_on_group_id)?.name).filter(Boolean)
        return <section className="release-group-card" key={group.id}>
          <div className="release-group-heading"><div><strong>{group.name}</strong><span>{group.mode === 'parallel' ? '组内并行' : '组内串行'} · {group.failure_policy === 'stop' ? '失败即停止' : '失败后继续'}</span></div>{canManageDelivery && <div><button type="button" onClick={() => openGroupEditor(plan, group)}>编辑</button><button className="danger-action" type="button" onClick={() => void deleteReleaseGroup(plan, group)}>删除</button></div>}</div>
          <div className="release-group-applications">{(group.applications || []).map((item, index) => <span key={item.id}>{group.mode === 'sequential' ? `${index + 1}. ` : ''}{item.application?.name || item.application_id}</span>)}</div>
          {dependencyNames.length > 0 && <small>依赖发布组：{dependencyNames.join('、')}</small>}
        </section>
      })}{(plan.groups || []).length === 0 && <p className="release-group-empty">还没有发布组，请添加需要一起发布的应用。</p>}</div>
      {groupEditor?.planID === plan.id && <div className="release-group-editor">
        <div className="release-group-editor-heading"><strong>{groupEditor.groupID ? '编辑发布组' : '添加发布组'}</strong><button type="button" onClick={() => setGroupEditor(null)} aria-label="关闭发布组编辑">×</button></div>
        <div className="form-grid">
          <label>发布组名称<input required maxLength={128} value={groupEditor.name} onChange={(event) => setGroupEditor({ ...groupEditor, name: event.target.value })} placeholder="例如：核心服务" /></label>
          <label>组内执行方式<select value={groupEditor.mode} onChange={(event) => setGroupEditor({ ...groupEditor, mode: event.target.value as 'parallel' | 'sequential' })}><option value="parallel">并行</option><option value="sequential">串行</option></select></label>
          <label>失败策略<select value={groupEditor.failure_policy} onChange={(event) => setGroupEditor({ ...groupEditor, failure_policy: event.target.value as 'stop' | 'continue' })}><option value="stop">停止本计划后续执行</option><option value="continue">记录失败并继续</option></select></label>
          <fieldset className="span-2 release-group-options"><legend>应用</legend>{applications.filter((application) => application.is_active).map((application) => <label key={application.id}><input type="checkbox" checked={groupEditor.application_ids.includes(application.id)} onChange={(event) => setGroupEditor({ ...groupEditor, application_ids: event.target.checked ? [...groupEditor.application_ids, application.id] : groupEditor.application_ids.filter((id) => id !== application.id) })} />{application.name}</label>)}</fieldset>
          {(plan.groups || []).some((group) => group.id !== groupEditor.groupID) && <fieldset className="span-2 release-group-options"><legend>前置发布组</legend>{plan.groups.filter((group) => group.id !== groupEditor.groupID).map((group) => <label key={group.id}><input type="checkbox" checked={groupEditor.depends_on_group_ids.includes(group.id)} onChange={(event) => setGroupEditor({ ...groupEditor, depends_on_group_ids: event.target.checked ? [...groupEditor.depends_on_group_ids, group.id] : groupEditor.depends_on_group_ids.filter((id) => id !== group.id) })} />{group.name}</label>)}</fieldset>}
        </div>
        <div className="form-actions"><button className="secondary-button" type="button" onClick={() => setGroupEditor(null)}>取消</button><button className="primary-button" type="button" disabled={submitting || !groupEditor.name.trim() || groupEditor.application_ids.length === 0} onClick={() => void saveReleaseGroup()}>{submitting ? '保存中…' : '保存发布组'}</button></div>
      </div>}
      <footer><span>创建于 {formatTime(plan.created_at)}</span>{canManageDelivery && <div><button type="button" onClick={() => openGroupEditor(plan)}>＋ 添加发布组</button>{plan.status === 'draft' && <button className="accent-action" type="button" disabled={(plan.groups || []).length === 0} onClick={() => void activateReleasePlan(plan)}>启用计划</button>}{plan.status === 'draft' && <button className="danger-action" type="button" onClick={() => void deleteReleasePlan(plan)}>删除</button>}</div>}</footer>
    </article>)}{!loading && releasePlans.length === 0 && <EmptyState title="还没有发布计划" description="创建一次迭代、版本或发布列车，再在计划中添加发布组。" />}</div>}

    {section === 'release-plans' && releaseView === 'runs' && <div className="pipeline-list">{runs.map((run) => {
      const application = applications.find((item) => item.id === run.application_id) || run.application
      const blockers = blockedRunReasons(run, application)
      return <article className={`pipeline-run${run.status === 'blocked' || run.status === 'failed' ? ' pipeline-run-blocked' : ''}`} key={run.id}>
        <div className="run-status-dot" />
        <div className="run-main">
          <div className="card-title-line"><h3>{application?.name || run.application_id}</h3><StatusPill value={run.status} /></div>
          <div className="run-details"><span>{run.environment || '未进入环境'}</span><span>{run.trigger === 'manual' ? '手动创建' : run.trigger === 'retry' ? '重新执行' : run.trigger}</span><span>{run.ref || '尚无代码引用'}</span><span>{shortSHA(run.commit_sha)}</span>{run.retry_of_id && <span>来源运行：{shortSHA(run.retry_of_id)}</span>}</div>
          {(run.repositories || []).length > 0 && <div className="run-repository-list">{(run.repositories || []).map((item) => <div key={item.id}><strong>{item.repository?.name || item.repository_id}</strong><small>{item.ref || '待选择版本'}{item.commit_sha ? ` · ${shortSHA(item.commit_sha)}` : ''}</small></div>)}</div>}
          {blockers.length > 0 ? <div className="run-blockers" role="alert"><strong>这次流水线运行不能继续，还缺少：</strong><ul>{blockers.map((reason) => <li key={reason}>{reason}</li>)}</ul></div> : <p>{run.message || '流水线运行已创建'}</p>}
          {run.image && <div className="run-details"><span>镜像：{run.image}</span>{run.deployment_id && <span>发布记录：{run.deployment_id}</span>}</div>}
          <div className="run-details"><span>关联流水线：{application?.workflow_template?.name || '应用流水线'}{run.workflow_revision ? ` · 第 ${run.workflow_revision} 版` : ''}</span><span>{run.approval_required ? '需要审核' : '无需审核'}</span>{run.current_node_id && <span>当前节点：{run.current_node_id}</span>}</div>
          <div className="run-actions">
            {run.status === 'blocked' && !run.commit_sha && application?.repository_id && canRun && <button className="accent-action" type="button" onClick={() => void openCommitPicker(run)}>选择版本</button>}
            {run.status === 'blocked' && Boolean(run.commit_sha) && applicationCanExecute(application) && canRun && <button className="accent-action" type="button" onClick={() => void executePipelineRun(run)}>执行</button>}
            {run.status === 'failed' && run.commit_sha && canRun && <button className="accent-action" type="button" disabled={retryingRunID === run.id} onClick={() => void retryPipelineRun(run)}>{retryingRunID === run.id ? '正在重新执行…' : '重新执行'}</button>}
            {(run.status === 'blocked' || run.status === 'failed') && application && canManageDelivery && <button type="button" onClick={() => navigate(`/applications?edit=${run.application_id}`)}>配置应用</button>}
            <button type="button" onClick={() => navigate(`/pipeline-plans/editor?application=${run.application_id}`)}>查看流水线</button>
            {run.current_node_id && run.status === 'awaiting_approval' && canReview && <button type="button" onClick={() => void runAction(`/pipeline-runs/${run.id}/approve`)}>通过审核</button>}
            {run.current_node_id && (run.status === 'detected' || run.status === 'ready') && canRun && <button className="accent-action" type="button" onClick={() => void executePipelineRun(run)}>{run.status === 'detected' ? '执行' : run.stage === 'deploy_succeeded' ? '进入下一节点' : '执行部署'}</button>}
            {canManageDelivery && <button className="danger-action" type="button" onClick={() => void deletePipelineRun(run)}>删除</button>}
          </div>
        </div>
        <time>{formatTime(run.created_at)}</time>
      </article>
    })}{!loading && runs.length === 0 && <EmptyState title="还没有流水线运行" description="可在应用页面手动执行，代码事件触发的运行也会显示在这里。" />}</div>}

    {section === 'release-plans' && releaseView === 'records' && canReadDeployment && <div className="resource-section release-records-readonly"><div className="section-heading"><h3>发布记录</h3><span>{deployments.length} 条 · 只读</span></div><div className="resource-panel"><ResourceTable rows={deployments} emptyText="还没有发布记录" columns={[
      { key: 'environment', label: '环境' }, { key: 'operation', label: '操作' }, { key: 'image', label: '镜像' },
      { key: 'status', label: '状态' }, { key: 'requested_by', label: '申请人' }, { key: 'approved_by', label: '审核人' },
      { key: 'error_message', label: '失败原因' }, { key: 'warning_message', label: '告警' }, { key: 'created_at', label: '时间' },
    ]} /></div></div>}

    {commitPicker && <div className="modal-backdrop" role="presentation" onMouseDown={(event) => { if (event.target === event.currentTarget && !commitSubmitting) setCommitPicker(null) }}>
      <div className="commit-picker-modal" role="dialog" aria-modal="true" aria-labelledby="commit-picker-title">
        <header><div><span>手动执行</span><h3 id="commit-picker-title">选择发布的 Commit</h3><p>{commitPicker.run.application?.name || applications.find((item) => item.id === commitPicker.run.application_id)?.name || '当前应用'}</p></div><button type="button" disabled={commitSubmitting} onClick={() => setCommitPicker(null)} aria-label="关闭">×</button></header>
        <div className="commit-picker-body">
          {commitPicker.loading ? <div className="commit-picker-empty">正在读取远程分支和 Tag…</div> : commitPicker.options.length === 0 ? <div className="commit-picker-empty">代码仓库没有可选择的分支或 Tag。</div> : <>
			{commitPicker.sources.length > 0 && <label>发布入口<select value={commitPicker.selectedSourceID} onChange={(event) => setCommitPicker({ ...commitPicker, selectedSourceID: event.target.value })}>{commitPicker.sources.map((source) => <option key={source.id} value={source.id}>{source.name}{source.environment ? ` · ${source.environment.toUpperCase()}` : ''}</option>)}</select></label>}
            <div className="commit-repository-list">{(() => {
              const selected = commitPicker.options.find((item) => item.ref === commitPicker.selectedRef)
              return <section className="commit-repository-item"><div><strong>{commitPicker.repositoryName}</strong></div><label>代码版本<select autoFocus value={commitPicker.selectedRef} onChange={(event) => setCommitPicker({ ...commitPicker, selectedRef: event.target.value })}><optgroup label="分支">{commitPicker.options.filter((item) => item.kind === 'branch').map((item) => <option key={item.ref} value={item.ref}>{item.name} · {shortSHA(item.sha)}</option>)}</optgroup><optgroup label="Tag">{commitPicker.options.filter((item) => item.kind === 'tag').map((item) => <option key={item.ref} value={item.ref}>{item.name} · {shortSHA(item.sha)}</option>)}</optgroup></select></label>{selected && <div className="commit-picker-sha"><span>Commit SHA</span><code>{selected.sha}</code></div>}</section>
            })()}</div>
          </>}
        </div>
        <footer><button className="secondary-button" type="button" disabled={commitSubmitting} onClick={() => setCommitPicker(null)}>取消</button><button className="primary-button" type="button" disabled={commitPicker.loading || !commitPicker.selectedRef || commitSubmitting} onClick={() => void confirmCommitExecution()}>{commitSubmitting ? '执行中…' : '确认并执行'}</button></footer>
      </div>
    </div>}
  </section>
}
