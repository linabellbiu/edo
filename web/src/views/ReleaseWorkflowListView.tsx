import { useCallback, useEffect, useMemo, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'

import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'

interface WorkflowTemplate {
  id: string
  name: string
  description: string
  revision: number
  is_active: boolean
  nodes: Array<Record<string, unknown>>
  edges: Array<Record<string, unknown>>
  viewport: { x: number; y: number; zoom: number }
  updated_at: string
}

interface ApplicationReference {
  id: string
  workflow_template_id?: string
}

function formatTime(value: string) {
  return new Date(value).toLocaleString('zh-CN', { hour12: false })
}

function copyName(name: string) {
  const now = new Date()
  const suffix = `${String(now.getMonth() + 1).padStart(2, '0')}-${String(now.getDate()).padStart(2, '0')} ${String(now.getHours()).padStart(2, '0')}:${String(now.getMinutes()).padStart(2, '0')}`
  return `${name} 副本 ${suffix}`
}

export default function ReleaseWorkflowListView() {
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const user = useAuthStore((state) => state.user)
  const canManage = Boolean(user?.is_superuser || user?.permissions.includes('delivery.manage'))
  const [templates, setTemplates] = useState<WorkflowTemplate[]>([])
  const [applications, setApplications] = useState<ApplicationReference[]>([])
  const [loading, setLoading] = useState(true)
  const [busyID, setBusyID] = useState('')
  const [error, setError] = useState('')

  const legacyEditorQuery = searchParams.has('application') || searchParams.has('template') || searchParams.get('create') === '1'

  const refresh = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const [templateResult, applicationResult] = await Promise.all([
        client.get<{ workflow_templates: WorkflowTemplate[] }>('/workflow-templates'),
        client.get<{ applications: ApplicationReference[] }>('/applications'),
      ])
      setTemplates(templateResult.data.workflow_templates || [])
      setApplications(applicationResult.data.applications || [])
    } catch (loadError) {
      setError(apiErrorMessage(loadError))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    if (legacyEditorQuery) {
      navigate(`/pipeline-plans/editor?${searchParams.toString()}`, { replace: true })
      return
    }
    void refresh()
  }, [legacyEditorQuery, navigate, refresh, searchParams])

  const usageCount = useMemo(() => {
    const result = new Map<string, number>()
    applications.forEach((application) => {
      if (!application.workflow_template_id) return
      result.set(application.workflow_template_id, (result.get(application.workflow_template_id) || 0) + 1)
    })
    return result
  }, [applications])

  async function setActive(template: WorkflowTemplate, active: boolean) {
    setBusyID(template.id)
    setError('')
    try {
      await client.put(`/workflow-templates/${template.id}`, {
        name: template.name,
        description: template.description,
        revision: template.revision,
        activate: active,
        nodes: template.nodes,
        edges: template.edges,
        viewport: template.viewport,
      })
      await refresh()
    } catch (updateError) {
      setError(apiErrorMessage(updateError))
    } finally {
      setBusyID('')
    }
  }

  async function duplicate(template: WorkflowTemplate) {
    setBusyID(template.id)
    setError('')
    try {
      const result = await client.post<{ workflow_template: WorkflowTemplate }>('/workflow-templates', {
        name: copyName(template.name),
        description: template.description,
        revision: 0,
        activate: false,
        nodes: template.nodes,
        edges: template.edges,
        viewport: template.viewport,
      })
      navigate(`/pipeline-plans/editor?template=${result.data.workflow_template.id}`)
    } catch (copyError) {
      setError(apiErrorMessage(copyError))
    } finally {
      setBusyID('')
    }
  }

  async function remove(template: WorkflowTemplate) {
	const applicationsUsingTemplate = usageCount.get(template.id) || 0
	if (applicationsUsingTemplate > 0) {
	  setError(`“${template.name}”仍被 ${applicationsUsingTemplate} 个应用使用，请先为这些应用更换流水线方案。`)
	  return
	}
	if (!window.confirm(`确认删除流水线方案“${template.name}”？\n\n删除后无法恢复。`)) return
	setBusyID(template.id)
	setError('')
	try {
	  await client.delete(`/workflow-templates/${template.id}`)
	  await refresh()
	} catch (deleteError) {
	  setError(apiErrorMessage(deleteError))
	} finally {
	  setBusyID('')
	}
  }

  if (legacyEditorQuery) return <div className="loading-panel">正在打开流水线方案…</div>
  if (loading && templates.length === 0) return <div className="loading-panel">正在读取流水线方案…</div>

  return <section className="workflow-list-page page-enter">
    <div className="workflow-list-toolbar">
      <div><strong>{templates.length}</strong><span>份流水线方案</span><small>应用选择方案时会复制当时的完整版本。</small></div>
      {canManage && <button className="primary-button" type="button" onClick={() => navigate('/pipeline-plans/editor?create=1')}>＋ 新建流水线方案</button>}
    </div>

    {error && <div className="form-alert error" role="alert">{error}</div>}

    {templates.length > 0 ? <div className="workflow-plan-table">
      <div className="workflow-plan-table-head" aria-hidden="true"><span>流水线方案</span><span>状态</span><span>版本</span><span>流程规模</span><span>使用应用</span><span>更新时间</span><span>操作</span></div>
      {templates.map((template) => <article className="workflow-plan-row" key={template.id}>
        <div className="workflow-plan-name"><span className="workflow-plan-icon">⌘</span><div><h3>{template.name}</h3><p>{template.description || '暂未填写方案说明'}</p></div></div>
        <div data-label="状态"><span className={`workflow-plan-state${template.is_active ? ' active' : ''}`}>{template.is_active ? '已启用' : '草稿'}</span></div>
        <div data-label="版本">第 {template.revision} 版</div>
        <div data-label="流程规模">{template.nodes.length} 个节点 · {template.edges.length} 条连线</div>
        <div data-label="使用应用">{usageCount.get(template.id) || 0} 个</div>
        <time data-label="更新时间">{formatTime(template.updated_at)}</time>
        <div className="workflow-plan-actions">
          <button type="button" onClick={() => navigate(`/pipeline-plans/editor?template=${template.id}`)}>编辑</button>
          {canManage && <button type="button" disabled={busyID === template.id} onClick={() => void duplicate(template)}>复制</button>}
          {canManage && <button type="button" disabled={busyID === template.id} onClick={() => void setActive(template, !template.is_active)}>{template.is_active ? '停用' : '启用'}</button>}
          {canManage && <button className="danger-action" type="button" disabled={busyID === template.id} onClick={() => void remove(template)}>删除</button>}
        </div>
      </article>)}
    </div> : <div className="workflow-list-empty">
      <span>⌘</span><h2>还没有流水线方案</h2><p>创建第一份方案，再到无限画布中配置环境流转、审核和部署节点。</p>
      {canManage && <button className="primary-button" type="button" onClick={() => navigate('/pipeline-plans/editor?create=1')}>＋ 新建流水线方案</button>}
    </div>}
  </section>
}
