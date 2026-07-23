import { useCallback, useEffect, useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'

import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'

type Section = 'applications' | 'repositories' | 'build-plans' | 'image-registries' | 'release-plans' | 'pipelines'

interface Repository {
  id: string; name: string; provider: string; clone_url: string; default_branch: string
  webhook_enabled: boolean; webhook_url?: string; is_active: boolean
}
interface BuildPlan {
  id: string; name: string; kind: string; description: string; dockerfile_path?: string
  context_path: string; timeout_seconds: number; is_active: boolean
}
interface ImageRegistry {
  id: string; name: string; provider: string; endpoint: string; namespace: string
  has_credential: boolean; is_active: boolean
}
interface ReleasePlan {
  id: string; name: string; kind: string; description: string; helm_chart?: string
  compose_file?: string; service_name?: string; timeout_seconds: number; is_active: boolean
}
interface DeploymentTarget { id: string; name: string; environment: string; platform: string; is_active: boolean }
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
  release_plan?: ReleasePlan; deployment_target?: DeploymentTarget
  release_approval_enabled: boolean; environments?: ApplicationEnvironment[]
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
  'release-plans': { title: '发布方案', description: '为脚本、Helm、Docker Compose 或 Docker 创建发布模板。' },
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
    poll_interval_seconds: 60, watch_push: true, watch_pull_request: false,
    watch_tags: false, tag_pattern: 'v*', build_plan_id: '', image_registry_id: '',
    release_plan_id: '', deployment_target_id: '', release_approval_enabled: true,
    environments: defaultEnvironments.map((item) => ({ ...item })),
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
  const user = useAuthStore((state) => state.user)
  const canManageDelivery = Boolean(user?.is_superuser || user?.permissions.includes('delivery.manage'))
  const canRun = Boolean(user?.is_superuser || user?.permissions.includes('delivery.run'))
	const canReview = Boolean(user?.is_superuser || user?.permissions.includes('deployment.review'))
  const canManageRepository = Boolean(user?.is_superuser || user?.permissions.includes('repository.manage'))
  const canReadDelivery = Boolean(user?.is_superuser || user?.permissions.includes('delivery.read'))
  const canReadRepository = Boolean(user?.is_superuser || user?.permissions.includes('repository.read'))
  const canReadDeployment = Boolean(user?.is_superuser || user?.permissions.includes('deployment.read'))
  const [applications, setApplications] = useState<Application[]>([])
  const [repositories, setRepositories] = useState<Repository[]>([])
  const [buildPlans, setBuildPlans] = useState<BuildPlan[]>([])
  const [registries, setRegistries] = useState<ImageRegistry[]>([])
  const [releasePlans, setReleasePlans] = useState<ReleasePlan[]>([])
  const [targets, setTargets] = useState<DeploymentTarget[]>([])
  const [runs, setRuns] = useState<PipelineRun[]>([])
  const [formOpen, setFormOpen] = useState(false)
  const [editingID, setEditingID] = useState('')
  const [loading, setLoading] = useState(true)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')
  const [webhookSetup, setWebhookSetup] = useState<{ url: string; secret: string } | null>(null)
  const [applicationForm, setApplicationForm] = useState(initialApplicationForm)
  const [repositoryForm, setRepositoryForm] = useState({ name: '', provider: 'github', clone_url: '', default_branch: 'main', auth_type: 'none', username: '', credential: '', webhook_enabled: true, allow_insecure_http: false })
  const [buildForm, setBuildForm] = useState({ name: '', kind: 'dockerfile', description: '', script: '', dockerfile_path: 'Dockerfile', context_path: '.', artifact_path: '', timeout_seconds: 1800 })
  const [registryForm, setRegistryForm] = useState({ name: '', provider: 'harbor', endpoint: 'https://', namespace: '', username: '', credential: '', allow_insecure_http: false })
  const [releaseForm, setReleaseForm] = useState({ name: '', kind: 'helm', description: '', script: '', helm_chart: '', helm_values: '', compose_file: 'docker-compose.yml', service_name: '', timeout_seconds: 600 })
  const copy = sectionCopy[section]

  const refresh = useCallback(async () => {
    setLoading(true)
      setError('')
    try {
      const repositoryRequest = canReadRepository
        ? client.get<{ repositories: Repository[] }>('/repositories')
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
      const releaseRequest = canReadDelivery
        ? client.get<{ release_plans: ReleasePlan[] }>('/release-plans')
        : Promise.resolve(null)
      const runRequest = canReadDelivery
        ? client.get<{ pipeline_runs: PipelineRun[] }>('/pipeline-runs')
        : Promise.resolve(null)
      const [appResult, repoResult, buildResult, registryResult, releaseResult, targetResult, runResult] = await Promise.all([
        applicationRequest,
        repositoryRequest,
        buildRequest,
        registryRequest,
        releaseRequest,
        targetRequest,
        runRequest,
      ])
      setApplications(appResult?.data.applications || [])
      setRepositories(repoResult?.data.repositories || [])
      setBuildPlans(buildResult?.data.build_plans || [])
      setRegistries(registryResult?.data.image_registries || [])
      setReleasePlans(releaseResult?.data.release_plans || [])
      setTargets(targetResult?.data.targets || [])
      setRuns(runResult?.data.pipeline_runs || [])
    } catch (loadError) {
      setError(apiErrorMessage(loadError))
    } finally {
      setLoading(false)
    }
  }, [canReadDelivery, canReadDeployment, canReadRepository])

  useEffect(() => { void refresh() }, [refresh])

  const counts = useMemo(() => ({
    configured: applications.filter((item) => item.build_plan_id && item.image_registry_id && item.workflow?.is_active).length,
    changed: applications.filter((item) => item.sync_status === 'changed').length,
  }), [applications])

  function closeForm() {
    setFormOpen(false)
    setEditingID('')
    setApplicationForm(initialApplicationForm())
    setError('')
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
	  environments,
    })
    setFormOpen(true)
    window.scrollTo({ top: 0, behavior: 'smooth' })
  }

  function updateEnvironment(key: ApplicationEnvironment['key'], updates: Partial<ApplicationEnvironment & { enabled: boolean }>) {
	setApplicationForm((current) => ({
	  ...current,
	  environments: current.environments.map((environment) => environment.key === key ? { ...environment, ...updates } : environment),
	}))
  }

  async function submitApplication() {
	const enabledEnvironments = applicationForm.environments.filter((item) => item.enabled).map(({ enabled: _enabled, id: _id, ...item }) => item)
	const primary = enabledEnvironments[0]
	await submit(editingID ? `/applications/${editingID}` : '/applications', {
	  ...applicationForm,
	  environments: enabledEnvironments,
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
      const result = await client.post<{ repository: Repository; webhook_secret?: string }>('/repositories', {
        ...repositoryForm, credential: repositoryForm.credential || null, regenerate_webhook: false,
      })
      if (result.data.webhook_secret) {
        setWebhookSetup({ url: result.data.repository.webhook_url || `/api/v1/webhooks/git/${result.data.repository.id}`, secret: result.data.webhook_secret })
      }
      setRepositoryForm({ ...repositoryForm, name: '', clone_url: '', credential: '' })
      closeForm()
      await refresh()
    } catch (submitError) {
      setError(apiErrorMessage(submitError))
    } finally {
      setSubmitting(false)
    }
  }

  const canCreate = section === 'repositories' ? canManageRepository : section !== 'pipelines' && canManageDelivery

  return <section className="devops-page page-enter">
    <div className="page-heading modern-heading">
      <div><span className="section-label">持续交付</span><h2>{copy.title}</h2><p>{copy.description}</p></div>
      <div className="heading-actions">
        <button className="icon-button" type="button" onClick={() => void refresh()} disabled={loading} aria-label="刷新">↻</button>
        {canCreate && <button className="primary-button" type="button" onClick={() => setFormOpen((value) => !value)}>＋ 创建{copy.title.replace('代码', '')}</button>}
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
      <div><strong>Webhook 已创建</strong><p>密钥只显示这一次，请现在复制到代码托管平台。</p></div>
      <dl><div><dt>回调地址</dt><dd>{webhookSetup.url}</dd></div><div><dt>签名密钥</dt><dd>{webhookSetup.secret}</dd></div></dl>
      <button type="button" onClick={() => setWebhookSetup(null)}>我已保存</button>
    </div>}

    {formOpen && section === 'applications' && <form className="create-sheet application-sheet" onSubmit={(event) => { event.preventDefault(); void submitApplication() }}>
      <div className="sheet-header"><div><h3>{editingID ? '配置应用' : '创建应用'}</h3><p>先确定环境和审核规则，创建后再到发布计划画布调整节点和连线。</p></div><button type="button" onClick={closeForm}>×</button></div>
      <div className="form-grid">
        <label>应用名称<input required value={applicationForm.name} onChange={(e) => setApplicationForm({ ...applicationForm, name: e.target.value })} placeholder="例如：订单服务" /></label>
        <label>代码仓库<select required value={applicationForm.repository_id} onChange={(e) => setApplicationForm({ ...applicationForm, repository_id: e.target.value })}><option value="">请选择仓库</option>{repositories.filter((item) => item.is_active).map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
        <label className="span-2">说明<input value={applicationForm.description} onChange={(e) => setApplicationForm({ ...applicationForm, description: e.target.value })} placeholder="这个应用负责什么" /></label>
        <label>构建方案<select value={applicationForm.build_plan_id} onChange={(e) => setApplicationForm({ ...applicationForm, build_plan_id: e.target.value })}><option value="">暂不绑定</option>{buildPlans.filter((item) => item.is_active).map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
        <label>镜像仓库<select value={applicationForm.image_registry_id} onChange={(e) => setApplicationForm({ ...applicationForm, image_registry_id: e.target.value })}><option value="">暂不绑定</option>{registries.filter((item) => item.is_active).map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
        <label>Pull 检查间隔<select value={applicationForm.poll_interval_seconds} onChange={(e) => setApplicationForm({ ...applicationForm, poll_interval_seconds: Number(e.target.value) })}><option value={30}>30 秒</option><option value={60}>1 分钟</option><option value={300}>5 分钟</option><option value={900}>15 分钟</option></select></label>
      </div>
      <div className="form-block"><span className="form-block-title">发布计划审核</span><div className="approval-choice">
        <label className={applicationForm.release_approval_enabled ? 'selected' : ''}><input type="radio" name="release-approval" checked={applicationForm.release_approval_enabled} onChange={() => setApplicationForm({ ...applicationForm, release_approval_enabled: true })} /><span><strong>需要审核</strong><small>生产部署的每条路径都必须经过审核节点，申请人不能审核自己。</small></span></label>
        <label className={!applicationForm.release_approval_enabled ? 'selected' : ''}><input type="radio" name="release-approval" checked={!applicationForm.release_approval_enabled} onChange={() => setApplicationForm({ ...applicationForm, release_approval_enabled: false })} /><span><strong>关闭审核</strong><small>审核节点不再阻塞流程，适合内部开发或已由外部系统审批的应用。</small></span></label>
      </div></div>
      <div className="form-block"><span className="form-block-title">应用环境</span><p className="form-block-help">环境可以少选，例如只启用 test 和 prod。创建后的默认连线可在发布计划里自由调整。</p><div className="environment-config-grid">
        {applicationForm.environments.map((environment) => <article className={`environment-config-card${environment.enabled ? ' enabled' : ''}`} key={environment.key}>
          <div className="environment-card-head"><label><input type="checkbox" checked={environment.enabled} onChange={(e) => updateEnvironment(environment.key, { enabled: e.target.checked })} /><span><strong>{environment.name}</strong><small>{environment.key}</small></span></label><b>{environment.enabled ? '已启用' : '未启用'}</b></div>
          {environment.enabled && <div className="environment-fields">
            <label>监听分支<input required value={environment.branch} onChange={(e) => updateEnvironment(environment.key, { branch: e.target.value })} placeholder="main 或 release/*" /></label>
            <fieldset><legend>触发方式</legend>{[
              ['poll_enabled', 'Pull'], ['watch_push', 'Push'], ['watch_pull_request', 'PR'], ['watch_tags', 'Tag'],
            ].map(([field, label]) => <label key={field}><input type="checkbox" checked={Boolean(environment[field as keyof ApplicationEnvironment])} onChange={(e) => updateEnvironment(environment.key, { [field]: e.target.checked } as Partial<ApplicationEnvironment>)} />{label}</label>)}</fieldset>
            {environment.watch_tags && <label>Tag 规则<input value={environment.tag_pattern} onChange={(e) => updateEnvironment(environment.key, { tag_pattern: e.target.value })} placeholder="v*" /></label>}
            <label>发布方案<select value={environment.release_plan_id || ''} onChange={(e) => updateEnvironment(environment.key, { release_plan_id: e.target.value })}><option value="">稍后配置</option>{releasePlans.filter((item) => item.is_active).map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
            <label>发布目标<select value={environment.deployment_target_id || ''} onChange={(e) => updateEnvironment(environment.key, { deployment_target_id: e.target.value })}><option value="">稍后配置</option>{targets.filter((item) => item.is_active).map((item) => <option key={item.id} value={item.id}>{item.name} · {item.environment}</option>)}</select></label>
          </div>}
        </article>)}
      </div></div><FormActions submitting={submitting} editing={Boolean(editingID)} onCancel={closeForm} />
    </form>}

    {formOpen && section === 'repositories' && <form className="create-sheet" onSubmit={(event) => { event.preventDefault(); void submitRepository() }}>
      <div className="sheet-header"><div><h3>添加代码仓库</h3><p>支持普通 Git、GitHub、GitLab、Gitea 和 Gitee。</p></div><button type="button" onClick={closeForm}>×</button></div>
      <div className="form-grid"><label>名称<input required value={repositoryForm.name} onChange={(e) => setRepositoryForm({ ...repositoryForm, name: e.target.value })} /></label><label>平台<select value={repositoryForm.provider} onChange={(e) => setRepositoryForm({ ...repositoryForm, provider: e.target.value })}><option value="github">GitHub</option><option value="gitlab">GitLab</option><option value="gitea">Gitea</option><option value="gitee">Gitee</option><option value="generic">普通 Git</option></select></label><label className="span-2">Clone 地址<input required value={repositoryForm.clone_url} onChange={(e) => setRepositoryForm({ ...repositoryForm, clone_url: e.target.value })} placeholder="https://git.example.com/team/project.git" /></label><label>默认分支<input value={repositoryForm.default_branch} onChange={(e) => setRepositoryForm({ ...repositoryForm, default_branch: e.target.value })} /></label><label>认证方式<select value={repositoryForm.auth_type} onChange={(e) => setRepositoryForm({ ...repositoryForm, auth_type: e.target.value })}><option value="none">无需认证</option><option value="token">Token</option><option value="ssh_key">SSH 私钥</option></select></label>{repositoryForm.auth_type !== 'none' && <><label>用户名<input value={repositoryForm.username} onChange={(e) => setRepositoryForm({ ...repositoryForm, username: e.target.value })} /></label><label>凭据<input type="password" required value={repositoryForm.credential} onChange={(e) => setRepositoryForm({ ...repositoryForm, credential: e.target.value })} /></label></>}</div><label className="simple-check"><input type="checkbox" checked={repositoryForm.webhook_enabled} onChange={(e) => setRepositoryForm({ ...repositoryForm, webhook_enabled: e.target.checked })} />启用 Webhook</label><FormActions submitting={submitting} onCancel={closeForm} />
    </form>}

    {formOpen && section === 'build-plans' && <form className="create-sheet" onSubmit={(event) => { event.preventDefault(); void submit('/build-plans', buildForm, () => setBuildForm({ ...buildForm, name: '', description: '', script: '' })) }}>
      <div className="sheet-header"><div><h3>创建构建方案</h3><p>Dockerfile 用于镜像构建，脚本用于已有的打包流程。</p></div><button type="button" onClick={closeForm}>×</button></div><div className="form-grid"><label>名称<input required value={buildForm.name} onChange={(e) => setBuildForm({ ...buildForm, name: e.target.value })} /></label><label>类型<select value={buildForm.kind} onChange={(e) => setBuildForm({ ...buildForm, kind: e.target.value })}><option value="dockerfile">Dockerfile</option><option value="script">打包脚本</option></select></label><label className="span-2">说明<input value={buildForm.description} onChange={(e) => setBuildForm({ ...buildForm, description: e.target.value })} /></label>{buildForm.kind === 'dockerfile' ? <><label>Dockerfile 路径<input required value={buildForm.dockerfile_path} onChange={(e) => setBuildForm({ ...buildForm, dockerfile_path: e.target.value })} /></label><label>构建上下文<input required value={buildForm.context_path} onChange={(e) => setBuildForm({ ...buildForm, context_path: e.target.value })} /></label></> : <label className="span-2">打包脚本<textarea required rows={8} value={buildForm.script} onChange={(e) => setBuildForm({ ...buildForm, script: e.target.value })} placeholder={'set -e\nnpm ci\nnpm run build'} /></label>}<label>超时时间（秒）<input type="number" min={30} max={7200} value={buildForm.timeout_seconds} onChange={(e) => setBuildForm({ ...buildForm, timeout_seconds: Number(e.target.value) })} /></label></div><FormActions submitting={submitting} onCancel={closeForm} />
    </form>}

    {formOpen && section === 'image-registries' && <form className="create-sheet" onSubmit={(event) => { event.preventDefault(); void submit('/image-registries', { ...registryForm, credential: registryForm.credential || null }, () => setRegistryForm({ ...registryForm, name: '', credential: '' })) }}>
      <div className="sheet-header"><div><h3>添加镜像仓库</h3><p>凭据会加密保存，接口不会返回原文。</p></div><button type="button" onClick={closeForm}>×</button></div><div className="form-grid"><label>名称<input required value={registryForm.name} onChange={(e) => setRegistryForm({ ...registryForm, name: e.target.value })} /></label><label>类型<select value={registryForm.provider} onChange={(e) => setRegistryForm({ ...registryForm, provider: e.target.value })}><option value="harbor">Harbor</option><option value="docker_hub">Docker Hub</option><option value="generic">通用 Registry</option></select></label><label className="span-2">仓库地址<input required value={registryForm.endpoint} onChange={(e) => setRegistryForm({ ...registryForm, endpoint: e.target.value })} /></label><label>命名空间<input value={registryForm.namespace} onChange={(e) => setRegistryForm({ ...registryForm, namespace: e.target.value })} /></label><label>用户名<input value={registryForm.username} onChange={(e) => setRegistryForm({ ...registryForm, username: e.target.value })} /></label><label className="span-2">密码或 Token<input type="password" value={registryForm.credential} onChange={(e) => setRegistryForm({ ...registryForm, credential: e.target.value })} /></label></div><FormActions submitting={submitting} onCancel={closeForm} />
    </form>}

    {formOpen && section === 'release-plans' && <form className="create-sheet" onSubmit={(event) => { event.preventDefault(); void submit('/release-plans', releaseForm, () => setReleaseForm({ ...releaseForm, name: '', description: '', script: '', helm_chart: '', helm_values: '', service_name: '' })) }}>
      <div className="sheet-header"><div><h3>创建发布方案</h3><p>选择一种清晰、可审计的发布方式。</p></div><button type="button" onClick={closeForm}>×</button></div><div className="form-grid"><label>名称<input required value={releaseForm.name} onChange={(e) => setReleaseForm({ ...releaseForm, name: e.target.value })} /></label><label>发布方式<select value={releaseForm.kind} onChange={(e) => setReleaseForm({ ...releaseForm, kind: e.target.value })}><option value="helm">Helm</option><option value="compose">Docker Compose</option><option value="docker">Docker</option><option value="script">自定义脚本</option></select></label><label className="span-2">说明<input value={releaseForm.description} onChange={(e) => setReleaseForm({ ...releaseForm, description: e.target.value })} /></label>{releaseForm.kind === 'helm' && <><label className="span-2">Chart 路径<input required value={releaseForm.helm_chart} onChange={(e) => setReleaseForm({ ...releaseForm, helm_chart: e.target.value })} placeholder="deploy/chart" /></label><label className="span-2">Values 覆盖<textarea rows={6} value={releaseForm.helm_values} onChange={(e) => setReleaseForm({ ...releaseForm, helm_values: e.target.value })} /></label></>}{releaseForm.kind === 'compose' && <label className="span-2">Compose 文件<input required value={releaseForm.compose_file} onChange={(e) => setReleaseForm({ ...releaseForm, compose_file: e.target.value })} /></label>}{releaseForm.kind === 'docker' && <label className="span-2">容器服务名<input required value={releaseForm.service_name} onChange={(e) => setReleaseForm({ ...releaseForm, service_name: e.target.value })} /></label>}{releaseForm.kind === 'script' && <label className="span-2">发布脚本<textarea required rows={8} value={releaseForm.script} onChange={(e) => setReleaseForm({ ...releaseForm, script: e.target.value })} /></label>}<label>超时时间（秒）<input type="number" min={30} max={3600} value={releaseForm.timeout_seconds} onChange={(e) => setReleaseForm({ ...releaseForm, timeout_seconds: Number(e.target.value) })} /></label></div><FormActions submitting={submitting} onCancel={closeForm} />
    </form>}

    {section === 'applications' && <div className="application-grid">{applications.map((application) => <article className="application-card" key={application.id}>
      <div className="card-top"><div className="app-identity"><span className="app-symbol">{application.name.slice(0, 1)}</span><div><h3>{application.name}</h3><p>{application.repository?.name || '未找到仓库'} · {application.branch}</p></div></div><StatusPill value={application.sync_status} /></div>
      <p className="card-description">{application.description || '暂未填写应用说明'}</p>
      <div className="commit-row"><div><span>当前版本</span><strong>{shortSHA(application.last_observed_commit)}</strong></div><div><span>最后检查</span><strong>{formatTime(application.last_checked_at)}</strong></div></div>
      <div className="application-environments">{(application.environments || []).map((environment) => <span key={environment.key}>{environment.key}<small>{environment.branch}</small></span>)}</div>
      <div className="pipeline-flow compact-flow"><span className={application.repository ? 'complete' : ''}>代码</span><i>›</i><span className={application.build_plan ? 'complete' : ''}>构建</span><i>›</i><span className={application.image_registry ? 'complete' : ''}>镜像</span><i>›</i><span className={application.workflow?.is_active ? 'complete' : ''}>{application.workflow?.is_active ? '计划已启用' : '计划草稿'}</span><span className={application.release_approval_enabled ? 'review-on' : ''}>{application.release_approval_enabled ? '需审核' : '免审核'}</span></div>
      <div className="card-actions"><button type="button" onClick={() => editApplication(application)}>配置</button><button type="button" onClick={() => navigate(`/release-workflows?application=${application.id}`)}>发布计划</button>{canRun && <><button type="button" onClick={() => void action(`/applications/${application.id}/sync`)}>检查更新</button><button className="accent-action" type="button" onClick={() => void action(`/applications/${application.id}/pipeline-runs`)}>启动流程</button></>}</div>
    </article>)}{!loading && applications.length === 0 && <EmptyState title="还没有应用" description="创建第一个应用，把仓库、构建和发布流程连接起来。" />}</div>}

    {section === 'repositories' && <div className="resource-card-grid">{repositories.map((item) => <article className="resource-card modern-card" key={item.id}><div className="resource-icon git-icon">⌁</div><div><div className="card-title-line"><h3>{item.name}</h3><span>{item.provider}</span></div><p className="resource-url">{item.clone_url}</p><div className="meta-row"><span>默认分支 {item.default_branch || '—'}</span><span>{item.webhook_enabled ? 'Webhook 已开启' : '仅 Pull'}</span></div></div></article>)}{!loading && repositories.length === 0 && <EmptyState title="还没有代码仓库" description="添加仓库后才能创建应用。" />}</div>}

    {section === 'build-plans' && <div className="resource-card-grid">{buildPlans.map((item) => <article className="resource-card modern-card" key={item.id}><div className="resource-icon build-icon">◇</div><div><div className="card-title-line"><h3>{item.name}</h3><span>{kindLabel(item.kind)}</span></div><p>{item.description || (item.kind === 'dockerfile' ? item.dockerfile_path : '自定义打包脚本')}</p><div className="meta-row"><span>上下文 {item.context_path}</span><span>超时 {item.timeout_seconds} 秒</span></div></div></article>)}{!loading && buildPlans.length === 0 && <EmptyState title="还没有构建方案" description="创建脚本或 Dockerfile 构建方案。" />}</div>}

    {section === 'image-registries' && <div className="resource-card-grid">{registries.map((item) => <article className="resource-card modern-card" key={item.id}><div className="resource-icon registry-icon">▱</div><div><div className="card-title-line"><h3>{item.name}</h3><span>{kindLabel(item.provider)}</span></div><p className="resource-url">{item.endpoint}</p><div className="meta-row"><span>{item.namespace || '根命名空间'}</span><span>{item.has_credential ? '凭据已配置' : '匿名访问'}</span></div></div></article>)}{!loading && registries.length === 0 && <EmptyState title="还没有镜像仓库" description="添加用于推送构建镜像的 Registry。" />}</div>}

    {section === 'release-plans' && <div className="resource-card-grid">{releasePlans.map((item) => <article className="resource-card modern-card" key={item.id}><div className="resource-icon release-icon">↗</div><div><div className="card-title-line"><h3>{item.name}</h3><span>{kindLabel(item.kind)}</span></div><p>{item.description || item.helm_chart || item.compose_file || item.service_name || '自定义发布脚本'}</p><div className="meta-row"><span>超时 {item.timeout_seconds} 秒</span><span>已启用</span></div></div></article>)}{!loading && releasePlans.length === 0 && <EmptyState title="还没有发布方案" description="创建 Helm、Compose、Docker 或脚本发布模板。" />}</div>}

    {section === 'pipelines' && <div className="pipeline-list">{runs.map((run) => <article className="pipeline-run" key={run.id}><div className="run-status-dot" /><div className="run-main"><div className="card-title-line"><h3>{run.application?.name || run.application_id}</h3><StatusPill value={run.status} /></div><div className="run-details"><span>{run.environment || '未进入环境'}</span><span>{run.trigger}</span><span>{run.ref || '尚无代码引用'}</span><span>{shortSHA(run.commit_sha)}</span></div><p>{run.message || '代码变更已记录'}</p>{run.current_node_id && <div className="run-actions">{run.status === 'awaiting_approval' && canReview && <button type="button" onClick={() => void runAction(`/pipeline-runs/${run.id}/approve`)}>通过审核</button>}{run.status !== 'awaiting_approval' && run.status !== 'succeeded' && canRun && <button type="button" onClick={() => void runAction(`/pipeline-runs/${run.id}/advance`, { target_node_id: '' })}>{run.status === 'ready' ? '完成部署并继续' : '推进下一步'}</button>}</div>}</div><time>{formatTime(run.created_at)}</time></article>)}{!loading && runs.length === 0 && <EmptyState title="还没有流水线记录" description="应用检测到代码变化后会显示在这里。" />}</div>}
  </section>
}
