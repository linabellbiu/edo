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
  build_plan_id?: string; release_plan_id?: string; build_plan?: BuildPlan; release_plan?: DeploymentPlan
}
interface GitCredential {
  id: string; name: string; provider: string; auth_type: 'token' | 'ssh_key'; username?: string; secret_hint: string
}
interface GitRef { name: string; sha: string }
interface ManualRunSource { id: string; name: string; environment?: string }
interface ManualRunRepositoryOptions { repository_id: string; name: string; sort_order: number; branches: GitRef[]; tags: GitRef[] }
interface RepositoryRefResult { branches: GitRef[]; tags: GitRef[]; manual_sources?: ManualRunSource[]; repositories?: ManualRunRepositoryOptions[] }
interface CommitOption { ref: string; name: string; sha: string; kind: 'branch' | 'tag' }
interface CommitPickerRepository { repositoryID: string; name: string; options: CommitOption[]; selectedRef: string; sortOrder: number }
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
  tag_pattern: string; release_plan_id?: string; deployment_target_id?: string; sort_order: number
}
interface Application {
  id: string; name: string; description: string; repository_id: string; branch: string
  poll_enabled: boolean; poll_interval_seconds: number; watch_push: boolean; watch_pull_request: boolean
  watch_tags: boolean; tag_pattern: string; build_plan_id?: string; image_registry_id?: string
  release_plan_id?: string; deployment_target_id?: string; last_observed_ref?: string
  last_observed_commit?: string; sync_status: string; sync_message?: string; last_checked_at?: string
  is_active: boolean; repository?: Repository; build_plan?: BuildPlan; image_registry?: ImageRegistry
  release_plan?: DeploymentPlan
  release_approval_enabled: boolean; environments?: ApplicationEnvironment[]
  workflow_template_id?: string; workflow_template?: WorkflowTemplate
  workflow?: { id: string; is_active: boolean; revision: number }
  repository_ordered: boolean; repositories: ApplicationRepository[]
}
interface ApplicationRepository {
  id: string; repository_id: string; sort_order: number; repository: Repository
  last_observed_ref?: string; last_observed_commit?: string; last_checked_at?: string
}
interface PipelineRunRepository {
  id: string; repository_id: string; sort_order: number; ref?: string; commit_sha?: string
  build_plan_id?: string; release_plan_id?: string; status: string; repository: Repository
}
interface PipelineRun {
  id: string; application_id: string; trigger: string; ref: string; commit_sha: string
  status: string; stage: string; message?: string; created_at: string; application?: Application
  environment?: string; current_node_id?: string; approved_by?: string
  workflow_id?: string; workflow_revision?: number; approval_required?: boolean
  repository_ordered: boolean; repositories?: PipelineRunRepository[]
}

const sectionCopy: Record<Section, { title: string; description: string }> = {
  applications: { title: '应用', description: '组合一个或多个代码仓库，并配置并行或顺序发布。' },
  repositories: { title: '代码仓库', description: '统一管理 Git 连接，以及仓库自己的构建和部署方案。' },
  'build-plans': { title: '构建方案', description: '保存可复用的打包脚本或 Dockerfile 构建配置。' },
  'image-registries': { title: '镜像仓库', description: '管理 Harbor、Docker Hub 或其他兼容 Registry。' },
  'deployment-plans': { title: '部署方案', description: '定义部署节点如何通过 Helm、Docker Compose、Docker 或受控脚本执行。' },
  'release-plans': { title: '发布计划', description: '集中查看发布计划和只读发布记录；计划关联应用流水线与审核要求。' },
}

const defaultEnvironments: Array<ApplicationEnvironment & { enabled: boolean }> = [
  { key: 'dev', name: '开发环境', branch: 'dev', enabled: true, poll_enabled: true, watch_push: true, watch_pull_request: false, watch_tags: false, tag_pattern: 'v*', release_plan_id: '', deployment_target_id: '', sort_order: 0 },
  { key: 'test', name: '测试环境', branch: 'test', enabled: true, poll_enabled: false, watch_push: true, watch_pull_request: true, watch_tags: false, tag_pattern: 'v*', release_plan_id: '', deployment_target_id: '', sort_order: 1 },
  { key: 'pre', name: '预发布环境', branch: 'main', enabled: true, poll_enabled: false, watch_push: true, watch_pull_request: true, watch_tags: false, tag_pattern: 'v*', release_plan_id: '', deployment_target_id: '', sort_order: 2 },
  { key: 'prod', name: '生产环境', branch: 'release', enabled: true, poll_enabled: false, watch_push: false, watch_pull_request: false, watch_tags: true, tag_pattern: 'v*', release_plan_id: '', deployment_target_id: '', sort_order: 3 },
]

function initialApplicationForm() {
  return {
    name: '', description: '', repository_id: '', repository_ids: [] as string[], repository_ordered: false, branch: 'dev', poll_enabled: true,
    poll_interval_seconds: 3, watch_push: true, watch_pull_request: false,
    watch_tags: false, tag_pattern: 'v*', image_registry_id: '',
    release_plan_id: '', deployment_target_id: '', release_approval_enabled: true,
    workflow_template_id: '',
    environments: defaultEnvironments.map((item) => ({ ...item })),
  }
}

function initialRepositoryForm() {
  return {
    name: '', provider: 'github', clone_url: '', default_branch: 'main', auth_type: 'none', username: '',
    credential_mode: 'saved', credential_id: '', credential_name: '', credential_secret: '',
    webhook_enabled: true, allow_insecure_http: false, build_plan_id: '', release_plan_id: '',
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

function blockedRunReasons(run: PipelineRun, application?: Application) {
  if (run.status !== 'blocked') return []
  const reasons: string[] = []
  if (!application?.repositories?.length && !application?.repository_id) reasons.push('未绑定代码仓库。')
  if (!run.commit_sha) reasons.push('尚未选择代码版本：点击“执行”，为每个仓库选择 Commit。')
  if (application && !application.workflow?.is_active) {
    const incomplete = (application.repositories || []).filter((item) => !item.repository.build_plan_id || !item.repository.release_plan_id)
    if (incomplete.length > 0) reasons.push(`仓库缺少构建或部署方案：${incomplete.map((item) => item.repository.name).join('、')}。`)
    if (!application.image_registry_id) reasons.push('未绑定镜像仓库。')
    reasons.push('应用流水线尚未启用，或仍有节点配置问题。')
  }
  if (reasons.length === 0) reasons.push(run.message || '应用的构建或发布配置不完整。')
  return [...new Set(reasons)]
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
    items: ['Compose 文件填写仓库内的相对路径，例如 deploy/compose.prod.yml。', '选择这种方式时不需要再填写 Docker 工作负载名称。', '镜像版本应由发布计划传入，不建议硬编码固定 Tag。'],
  },
  docker: {
    title: 'Docker 部署',
    description: '用于标识由 Docker 方式管理的单个容器或 Docker Service，不是镜像名称。',
    items: ['填写稳定的逻辑名称，例如 order-api、zrt-api 或 web-prod。', '不要填写镜像地址、Tag、容器 ID、端口、服务器 IP 或域名。', '如果实际由 docker compose 管理，请改选“Docker Compose”。', '当前版本只保存此标识；流水线到达部署节点后仍等待受控部署执行器，不会直接修改容器。'],
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
  const [workflowTemplates, setWorkflowTemplates] = useState<WorkflowTemplate[]>([])
  const [runs, setRuns] = useState<PipelineRun[]>([])
  const [deployments, setDeployments] = useState<ResourceRecord[]>([])
  const [formOpen, setFormOpen] = useState(false)
  const [editingID, setEditingID] = useState('')
  const [loading, setLoading] = useState(true)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')
  const [webhookSetup, setWebhookSetup] = useState<{ url: string; secret: string } | null>(null)
  const [repositoryChecks, setRepositoryChecks] = useState<Record<string, RepositoryCheck>>({})
  const [repositoryFormCheck, setRepositoryFormCheck] = useState<RepositoryCheck | null>(null)
  const [registryFormCheck, setRegistryFormCheck] = useState<RepositoryCheck | null>(null)
  const [commitPicker, setCommitPicker] = useState<{ run: PipelineRun; repositories: CommitPickerRepository[]; sources: ManualRunSource[]; selectedSourceID: string; loading: boolean } | null>(null)
  const [commitSubmitting, setCommitSubmitting] = useState(false)
  const registryTestSequence = useRef(0)
  const [applicationForm, setApplicationForm] = useState(initialApplicationForm)
  const [repositoryForm, setRepositoryForm] = useState(initialRepositoryForm)
  const [buildForm, setBuildForm] = useState({ name: '', kind: 'dockerfile', description: '', script: '', dockerfile_path: 'Dockerfile', context_path: '.', artifact_path: '', timeout_seconds: 1800 })
  const [registryForm, setRegistryForm] = useState({ name: '', provider: 'harbor', endpoint: 'https://', namespace: '', username: '', credential: '', allow_insecure_http: false })
  const [deploymentForm, setDeploymentForm] = useState({ name: '', kind: 'helm', description: '', script: '', helm_chart: '', helm_values: '', compose_file: 'docker-compose.yml', service_name: '', timeout_seconds: 600 })
  const copy = sectionCopy[section]
  const releaseView = canReadDeployment && (searchParams.get('view') === 'records' || !canReadDelivery) ? 'records' : 'plans'
  const canCreate = section === 'repositories' ? canManageRepository : section !== 'release-plans' && canManageDelivery

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
      const workflowTemplateRequest = canReadDelivery
        ? client.get<{ workflow_templates: WorkflowTemplate[] }>('/workflow-templates')
        : Promise.resolve(null)
      const [appResult, repoResult, credentialResult, buildResult, registryResult, deploymentPlanResult, deploymentRecordResult, runResult, workflowTemplateResult] = await Promise.all([
        applicationRequest,
        repositoryRequest,
        credentialRequest,
        buildRequest,
        registryRequest,
        deploymentPlanRequest,
        deploymentRecordRequest,
        runRequest,
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
    configured: applications.filter((item) => (item.repositories || []).length > 0 && item.repositories.every((link) => link.repository.build_plan_id && link.repository.release_plan_id) && item.image_registry_id && item.workflow?.is_active).length,
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
    const repositoryIDs = (application.repositories || []).map((item) => item.repository_id)
    setEditingID(application.id)
    setApplicationForm({
      name: application.name, description: application.description || '', repository_id: application.repository_id,
      repository_ids: repositoryIDs.length > 0 ? repositoryIDs : [application.repository_id].filter(Boolean),
      repository_ordered: application.repository_ordered || false,
      branch: application.branch, poll_enabled: application.poll_enabled,
      poll_interval_seconds: application.poll_interval_seconds, watch_push: application.watch_push,
      watch_pull_request: application.watch_pull_request, watch_tags: application.watch_tags,
      tag_pattern: application.tag_pattern || 'v*',
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
      build_plan_id: repository.build_plan_id || '',
      release_plan_id: repository.release_plan_id || '',
    })
    setRepositoryFormCheck(null)
    setFormOpen(true)
    window.scrollTo({ top: 0, behavior: 'smooth' })
  }

  function addApplicationRepository(repositoryID: string) {
    if (!repositoryID || applicationForm.repository_ids.includes(repositoryID)) return
    setApplicationForm({ ...applicationForm, repository_ids: [...applicationForm.repository_ids, repositoryID] })
  }

  function removeApplicationRepository(repositoryID: string) {
    setApplicationForm({ ...applicationForm, repository_ids: applicationForm.repository_ids.filter((id) => id !== repositoryID) })
  }

  function moveApplicationRepository(index: number, direction: -1 | 1) {
    const target = index + direction
    if (target < 0 || target >= applicationForm.repository_ids.length) return
    const next = [...applicationForm.repository_ids]
    const current = next[index]
    next[index] = next[target]
    next[target] = current
    setApplicationForm({ ...applicationForm, repository_ids: next })
  }

  async function submitApplication() {
	if (applicationForm.repository_ids.length === 0) {
	  setError('请至少选择一个代码仓库')
	  return
	}
	const enabledEnvironments = applicationForm.environments.filter((item) => item.enabled).map(({ enabled: _enabled, id: _id, ...item }) => item)
	const primary = enabledEnvironments[0]
	await submit(editingID ? `/applications/${editingID}` : '/applications', {
	  ...applicationForm,
	  repository_id: applicationForm.repository_ids[0] || '',
	  repositories: applicationForm.repository_ids.map((repositoryID, index) => ({ repository_id: repositoryID, sort_order: index })),
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

  async function createReleasePlan(applicationID: string) {
	setError('')
	try {
	  await client.post(`/applications/${applicationID}/pipeline-runs`)
	  navigate('/release-plans')
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
        build_plan_id: repositoryForm.build_plan_id,
        release_plan_id: repositoryForm.release_plan_id,
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
	setCommitPicker({ run, repositories: [], sources: [], selectedSourceID: '', loading: true })
	try {
	  const response = await client.get<RepositoryRefResult>(`/applications/${run.application_id}/repository-refs`, { timeout: 35_000 })
	  const application = applications.find((item) => item.id === run.application_id) || run.application
	  const remoteRepositories = response.data.repositories?.length ? response.data.repositories : [{
		repository_id: application?.repository_id || '', name: application?.repository?.name || '代码仓库', sort_order: 0,
		branches: response.data.branches || [], tags: response.data.tags || [],
	  }]
	  const pickerRepositories = remoteRepositories.map((remote) => {
		const options: CommitOption[] = [
		  ...(remote.branches || []).map((item) => ({ ref: `refs/heads/${item.name}`, name: item.name, sha: item.sha, kind: 'branch' as const })),
		  ...(remote.tags || []).map((item) => ({ ref: `refs/tags/${item.name}`, name: item.name, sha: item.sha, kind: 'tag' as const })),
		]
		const repository = repositories.find((item) => item.id === remote.repository_id)
		const preferredRef = `refs/heads/${repository?.default_branch || application?.branch || 'main'}`
		return { repositoryID: remote.repository_id, name: remote.name, options, selectedRef: options.some((item) => item.ref === preferredRef) ? preferredRef : options[0]?.ref || '', sortOrder: remote.sort_order }
	  })
	  const sources = response.data.manual_sources || []
	  setCommitPicker((current) => current?.run.id === run.id ? { run, repositories: pickerRepositories, sources, selectedSourceID: sources[0]?.id || '', loading: false } : current)
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

  async function confirmCommitExecution() {
	if (!commitPicker) return
	const selections = commitPicker.repositories.map((repository) => {
	  const selected = repository.options.find((item) => item.ref === repository.selectedRef)
	  return selected ? { repository_id: repository.repositoryID, ref: selected.ref, commit_sha: selected.sha } : null
	})
	if (selections.some((item) => item === null)) return
	setCommitSubmitting(true)
	setError('')
	try {
	  await client.post(`/pipeline-runs/${commitPicker.run.id}/execute`, { repositories: selections, source_node_id: commitPicker.selectedSourceID })
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
	if (!window.confirm(`确认删除“${applicationName}”的这条发布计划？\n\n发布计划删除后无法恢复；独立的发布记录不会被删除。`)) return
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
        {canCreate && section !== 'applications' && <button className="primary-button" type="button" onClick={toggleCreateForm}>＋ 创建{copy.title.replace('代码', '')}</button>}
        {section === 'release-plans' && releaseView === 'plans' && canRun && <button className="primary-button" type="button" onClick={() => navigate('/applications')}>＋ 从应用创建</button>}
      </div>
    </div>

    {section === 'applications' && <nav className="application-subnav" aria-label="应用页面">
      <button type="button" className={!formOpen ? 'active' : ''} aria-current={!formOpen ? 'page' : undefined} onClick={closeForm}>应用列表</button>
      {canManageDelivery && <button type="button" className={formOpen && !editingID ? 'active' : ''} aria-current={formOpen && !editingID ? 'page' : undefined} onClick={openApplicationCreateForm}>创建应用</button>}
      {formOpen && editingID && <button type="button" className="active" aria-current="page">编辑应用<span>{applicationForm.name}</span></button>}
    </nav>}

    {section === 'release-plans' && <div className="release-view-tabs" role="tablist" aria-label="发布数据">
      {canReadDelivery && <button type="button" role="tab" aria-selected={releaseView === 'plans'} className={releaseView === 'plans' ? 'active' : ''} onClick={() => { const next = new URLSearchParams(searchParams); next.delete('view'); setSearchParams(next) }}>发布计划</button>}
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
        <div className="span-2 application-repository-picker">
          <div className="repository-picker-title"><div><strong>代码仓库</strong><span>构建和部署方案由仓库自身提供</span></div>{canManageRepository && <button type="button" onClick={() => navigate('/repositories?create=1')} aria-label="创建代码仓库">＋</button>}</div>
          <div className="repository-add-row"><select value="" onChange={(event) => addApplicationRepository(event.target.value)}><option value="">＋ 添加仓库</option>{repositories.filter((item) => item.is_active && !applicationForm.repository_ids.includes(item.id)).map((item) => <option key={item.id} value={item.id}>{item.name}{!item.build_plan_id || !item.release_plan_id ? ' · 配置不完整' : ''}</option>)}</select></div>
          {applicationForm.repository_ids.length === 0 ? <p className="repository-picker-empty">至少添加一个仓库。</p> : <div className="selected-repositories">{applicationForm.repository_ids.map((repositoryID, index) => {
            const repository = repositories.find((item) => item.id === repositoryID)
            if (!repository) return null
            return <div key={repositoryID} className={!repository.build_plan_id || !repository.release_plan_id ? 'incomplete' : ''}>
              <span className="repository-sequence">{applicationForm.repository_ordered ? index + 1 : '•'}</span>
              <div><strong>{repository.name}</strong><small>构建：{repository.build_plan?.name || '未配置'} · 部署：{repository.release_plan?.name || '未配置'}</small></div>
              <div className="repository-order-actions">{applicationForm.repository_ordered && <><button type="button" disabled={index === 0} onClick={() => moveApplicationRepository(index, -1)} aria-label={`上移 ${repository.name}`}>↑</button><button type="button" disabled={index === applicationForm.repository_ids.length - 1} onClick={() => moveApplicationRepository(index, 1)} aria-label={`下移 ${repository.name}`}>↓</button></>}<button type="button" onClick={() => removeApplicationRepository(repositoryID)} aria-label={`移除 ${repository.name}`}>×</button></div>
            </div>
          })}</div>}
          <div className="repository-order-mode"><label className={!applicationForm.repository_ordered ? 'selected' : ''}><input type="radio" name="repository-order" checked={!applicationForm.repository_ordered} onChange={() => setApplicationForm({ ...applicationForm, repository_ordered: false })} /><span><strong>无序发布</strong><small>默认，各仓库互不等待</small></span></label><label className={applicationForm.repository_ordered ? 'selected' : ''}><input type="radio" name="repository-order" checked={applicationForm.repository_ordered} onChange={() => setApplicationForm({ ...applicationForm, repository_ordered: true })} /><span><strong>顺序发布</strong><small>按上面的 1、2、3 依次执行</small></span></label></div>
        </div>
        <ResourceSelectField id="application-image-registry" label="镜像仓库" createLabel="镜像仓库" createTo={canManageDelivery ? '/image-registries?create=1' : undefined} value={applicationForm.image_registry_id} onChange={(event) => setApplicationForm({ ...applicationForm, image_registry_id: event.target.value })}><option value="">暂不绑定</option>{registries.filter((item) => item.is_active).map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</ResourceSelectField>
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
        <ResourceSelectField id="repository-build-plan" label="构建方案" createLabel="构建方案" createTo={canManageDelivery ? '/build-plans?create=1' : undefined} required value={repositoryForm.build_plan_id} onChange={(event) => setRepositoryForm({ ...repositoryForm, build_plan_id: event.target.value })}><option value="">请选择构建方案</option>{buildPlans.filter((item) => item.is_active).map((item) => <option key={item.id} value={item.id}>{item.name} · {kindLabel(item.kind)}</option>)}</ResourceSelectField>
        <ResourceSelectField id="repository-release-plan" label="部署方案" createLabel="部署方案" createTo={canManageDelivery ? '/deployment-plans?create=1' : undefined} required value={repositoryForm.release_plan_id} onChange={(event) => setRepositoryForm({ ...repositoryForm, release_plan_id: event.target.value })}><option value="">请选择部署方案</option>{deploymentPlans.filter((item) => item.is_active).map((item) => <option key={item.id} value={item.id}>{item.name} · {kindLabel(item.kind)}</option>)}</ResourceSelectField>
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

    {section === 'applications' && !formOpen && <div className="application-grid">{applications.map((application) => <article className="application-card" key={application.id}>
      <div className="card-top"><div className="app-identity"><span className="app-symbol">{application.name.slice(0, 1)}</span><div><h3>{application.name}</h3><p>{(application.repositories || []).map((item) => item.repository.name).join('、') || application.repository?.name || '未找到仓库'} · {application.repository_ordered ? '顺序发布' : '无序发布'}</p></div></div><StatusPill value={application.sync_status} /></div>
      <p className="card-description">{application.description || '暂未填写应用说明'}</p>
      <div className="commit-row"><div><span>当前版本</span><strong>{shortSHA(application.last_observed_commit)}</strong></div><div><span>最后检查</span><strong>{formatTime(application.last_checked_at)}</strong></div></div>
      <div className="application-environments">{(application.environments || []).map((environment) => <span key={environment.key}>{environment.key}<small>{environment.branch}</small></span>)}</div>
      <div className="pipeline-flow compact-flow"><span className={(application.repositories || []).length > 0 ? 'complete' : ''}>{(application.repositories || []).length || 1} 个仓库</span><i>›</i><span className={(application.repositories || []).length > 0 && (application.repositories || []).every((item) => item.repository.build_plan_id && item.repository.release_plan_id) ? 'complete' : ''}>仓库方案</span><i>›</i><span className={application.image_registry ? 'complete' : ''}>镜像</span><i>›</i><span className={application.workflow?.is_active ? 'complete' : ''}>{application.workflow_template?.name || (application.workflow?.is_active ? '应用流水线' : '流水线草稿')}</span><span className={application.release_approval_enabled ? 'review-on' : ''}>{application.release_approval_enabled ? '需审核' : '免审核'}</span></div>
      <div className="card-actions"><button type="button" onClick={() => editApplication(application)}>配置</button><button type="button" onClick={() => navigate(`/pipeline-plans/editor?application=${application.id}`)}>应用流水线</button>{canRun && <><button type="button" onClick={() => void action(`/applications/${application.id}/sync`)}>检查更新</button><button className="accent-action" type="button" onClick={() => void createReleasePlan(application.id)}>创建发布计划</button></>}</div>
    </article>)}{!loading && applications.length === 0 && <EmptyState title="还没有应用" description="创建第一个应用，把仓库、构建和发布流程连接起来。" />}</div>}

    {section === 'repositories' && <div className="resource-card-grid">{repositories.map((item) => {
      const check = repositoryChecks[item.id]
      return <article className="resource-card repository-card modern-card" key={item.id}>
        <div className="resource-icon git-icon">⌁</div>
        <div className="repository-card-main">
          <div className="card-title-line"><h3>{item.name}</h3><span>{item.provider}</span></div>
          <p className="resource-url" title={item.clone_url}>{item.clone_url}</p>
          <div className="meta-row"><span>默认分支 {item.default_branch || '—'}</span><span>{item.webhook_enabled ? 'Webhook 已开启' : '仅 Pull'}</span></div>
          <div className="repository-plan-row"><span>构建：{item.build_plan?.name || '未配置'}</span><span>部署：{item.release_plan?.name || '未配置'}</span></div>
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

    {section === 'release-plans' && releaseView === 'plans' && <div className="pipeline-list">{runs.map((run) => {
      const application = applications.find((item) => item.id === run.application_id) || run.application
      const blockers = blockedRunReasons(run, application)
      return <article className={`pipeline-run${run.status === 'blocked' ? ' pipeline-run-blocked' : ''}`} key={run.id}>
        <div className="run-status-dot" />
        <div className="run-main">
          <div className="card-title-line"><h3>{application?.name || run.application_id}</h3><StatusPill value={run.status} /></div>
          <div className="run-details"><span>{run.environment || '未进入环境'}</span><span>{run.trigger === 'manual' ? '手动创建' : run.trigger}</span><span>{run.ref || '尚无代码引用'}</span><span>{shortSHA(run.commit_sha)}</span></div>
          {(run.repositories || []).length > 0 && <div className="run-repository-list">{(run.repositories || []).map((item, index) => <div key={item.id}><span>{run.repository_ordered ? index + 1 : '•'}</span><strong>{item.repository?.name || item.repository_id}</strong><small>{item.ref || '待选择版本'}{item.commit_sha ? ` · ${shortSHA(item.commit_sha)}` : ''}</small></div>)}</div>}
          {blockers.length > 0 ? <div className="run-blockers" role="alert"><strong>这条发布计划不能继续，还缺少：</strong><ul>{blockers.map((reason) => <li key={reason}>{reason}</li>)}</ul></div> : <p>{run.message || '发布计划已创建'}</p>}
          <div className="run-details"><span>关联流水线：{application?.workflow_template?.name || '应用流水线'}{run.workflow_revision ? ` · 第 ${run.workflow_revision} 版` : ''}</span><span>{run.approval_required ? '需要审核' : '无需审核'}</span>{run.current_node_id && <span>当前节点：{run.current_node_id}</span>}</div>
          <div className="run-actions">
            {run.status === 'blocked' && !run.commit_sha && canRun && <button className="accent-action" type="button" onClick={() => void openCommitPicker(run)}>执行</button>}
            {run.status === 'blocked' && application && canManageDelivery && <button type="button" onClick={() => navigate(`/applications?edit=${run.application_id}`)}>配置应用</button>}
            <button type="button" onClick={() => navigate(`/pipeline-plans/editor?application=${run.application_id}`)}>查看流水线</button>
            {run.current_node_id && run.status === 'awaiting_approval' && canReview && <button type="button" onClick={() => void runAction(`/pipeline-runs/${run.id}/approve`)}>通过审核</button>}
            {run.current_node_id && run.status !== 'blocked' && run.status !== 'awaiting_approval' && run.status !== 'succeeded' && canRun && <button className="accent-action" type="button" onClick={() => void executePipelineRun(run)}>{run.status === 'detected' ? '执行' : run.status === 'ready' ? '执行部署' : '继续执行'}</button>}
            {canManageDelivery && <button className="danger-action" type="button" onClick={() => void deletePipelineRun(run)}>删除</button>}
          </div>
        </div>
        <time>{formatTime(run.created_at)}</time>
      </article>
    })}{!loading && runs.length === 0 && <EmptyState title="还没有发布计划" description="请从应用页面创建；代码事件触发的计划也会显示在这里。" />}</div>}

    {section === 'release-plans' && releaseView === 'records' && canReadDeployment && <div className="resource-section release-records-readonly"><div className="section-heading"><h3>发布记录</h3><span>{deployments.length} 条 · 只读</span></div><div className="resource-panel"><ResourceTable rows={deployments} emptyText="还没有发布记录" columns={[
      { key: 'environment', label: '环境' }, { key: 'operation', label: '操作' }, { key: 'image', label: '镜像' },
      { key: 'status', label: '状态' }, { key: 'requested_by', label: '申请人' }, { key: 'approved_by', label: '审核人' },
      { key: 'error_message', label: '失败原因' }, { key: 'warning_message', label: '告警' }, { key: 'created_at', label: '时间' },
    ]} /></div></div>}

    {commitPicker && <div className="modal-backdrop" role="presentation" onMouseDown={(event) => { if (event.target === event.currentTarget && !commitSubmitting) setCommitPicker(null) }}>
      <div className="commit-picker-modal" role="dialog" aria-modal="true" aria-labelledby="commit-picker-title">
        <header><div><span>手动执行</span><h3 id="commit-picker-title">选择发布的 Commit</h3><p>{commitPicker.run.application?.name || applications.find((item) => item.id === commitPicker.run.application_id)?.name || '当前应用'}</p></div><button type="button" disabled={commitSubmitting} onClick={() => setCommitPicker(null)} aria-label="关闭">×</button></header>
        <div className="commit-picker-body">
          {commitPicker.loading ? <div className="commit-picker-empty">正在读取各仓库的远程分支和 Tag…</div> : commitPicker.repositories.length === 0 ? <div className="commit-picker-empty">应用没有可发布的代码仓库。</div> : <>
			{commitPicker.sources.length > 0 && <label>发布入口<select value={commitPicker.selectedSourceID} onChange={(event) => setCommitPicker({ ...commitPicker, selectedSourceID: event.target.value })}>{commitPicker.sources.map((source) => <option key={source.id} value={source.id}>{source.name}{source.environment ? ` · ${source.environment.toUpperCase()}` : ''}</option>)}</select></label>}
            <div className="commit-repository-list">{commitPicker.repositories.map((repository, index) => {
              const selected = repository.options.find((item) => item.ref === repository.selectedRef)
              return <section key={repository.repositoryID} className="commit-repository-item"><div><span>{commitPicker.run.repository_ordered ? index + 1 : '•'}</span><strong>{repository.name}</strong><small>{commitPicker.run.repository_ordered ? '按顺序发布' : '无序发布'}</small></div>{repository.options.length === 0 ? <p>仓库没有可选择的分支或 Tag。</p> : <><label>代码版本<select autoFocus={index === 0} value={repository.selectedRef} onChange={(event) => setCommitPicker({ ...commitPicker, repositories: commitPicker.repositories.map((item) => item.repositoryID === repository.repositoryID ? { ...item, selectedRef: event.target.value } : item) })}><optgroup label="分支">{repository.options.filter((item) => item.kind === 'branch').map((item) => <option key={item.ref} value={item.ref}>{item.name} · {shortSHA(item.sha)}</option>)}</optgroup><optgroup label="Tag">{repository.options.filter((item) => item.kind === 'tag').map((item) => <option key={item.ref} value={item.ref}>{item.name} · {shortSHA(item.sha)}</option>)}</optgroup></select></label>{selected && <div className="commit-picker-sha"><span>Commit SHA</span><code>{selected.sha}</code></div>}</>}</section>
            })}</div>
          </>}
        </div>
        <footer><button className="secondary-button" type="button" disabled={commitSubmitting} onClick={() => setCommitPicker(null)}>取消</button><button className="primary-button" type="button" disabled={commitPicker.loading || commitPicker.repositories.some((item) => !item.selectedRef) || commitSubmitting} onClick={() => void confirmCommitExecution()}>{commitSubmitting ? '执行中…' : '确认并执行'}</button></footer>
      </div>
    </div>}
  </section>
}
