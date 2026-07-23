import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useSearchParams } from 'react-router-dom'

import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'

type NodeType = 'trigger' | 'manual' | 'approval' | 'deploy'
type EnvironmentKey = 'dev' | 'test' | 'pre' | 'prod'

interface ApplicationEnvironment {
  id: string; key: EnvironmentKey; name: string; branch: string; poll_enabled: boolean
  watch_push: boolean; watch_pull_request: boolean; watch_tags: boolean; tag_pattern: string
  release_plan_id?: string; deployment_target_id?: string; sort_order: number
}
interface Application {
  id: string; name: string; release_approval_enabled: boolean; environments: ApplicationEnvironment[]
}
interface ReleasePlan { id: string; name: string; kind: string; is_active: boolean }
interface DeploymentTarget { id: string; name: string; environment: string; is_active: boolean }
interface WorkflowNode {
  id: string; type: NodeType; name: string; position: { x: number; y: number }
  config: {
    environment?: EnvironmentKey; branch?: string; events?: string[]; tag_pattern?: string
    release_plan_id?: string; deployment_target_id?: string; description?: string
  }
}
interface WorkflowEdge { id: string; source: string; target: string; label?: string }
interface Workflow {
  id: string; application_id: string; name: string; revision: number; is_active: boolean
  nodes: WorkflowNode[]; edges: WorkflowEdge[]; viewport: { x: number; y: number; zoom: number }
}
interface WorkflowIssue { code: string; message: string; node_id?: string; edge_id?: string }
interface WorkflowResponse { workflow: Workflow; valid: boolean; issues: WorkflowIssue[] }

const nodeCopy: Record<NodeType, { label: string; hint: string; icon: string }> = {
  trigger: { label: '代码触发', hint: '监听分支、Push、PR 或 Tag', icon: '⌁' },
  manual: { label: '人工放行', hint: '主动接测后进入下一环境', icon: '✓' },
  approval: { label: '发布审核', hint: '由其他成员确认发布', icon: '◎' },
  deploy: { label: '部署', hint: '执行绑定的发布方案', icon: '↗' },
}

const environmentLabel: Record<EnvironmentKey, string> = {
  dev: '开发环境', test: '测试环境', pre: '预发布环境', prod: '生产环境',
}

function uid(prefix: string) {
  return `${prefix}-${crypto.randomUUID()}`
}

function triggerEvents(environment: ApplicationEnvironment) {
  return [
    environment.poll_enabled && 'pull', environment.watch_push && 'push',
    environment.watch_pull_request && 'pr', environment.watch_tags && 'tag',
  ].filter(Boolean) as string[]
}

function createTemplate(application: Application, compact = false) {
  let environments = [...application.environments].sort((a, b) => a.sort_order - b.sort_order)
  if (compact) {
    const selected = environments.filter((item) => item.key === 'test' || item.key === 'prod')
    if (selected.length > 0) environments = selected
  }
  const nodes: WorkflowNode[] = []
  const edges: WorkflowEdge[] = []
  const deployIDs: string[] = []
  const entryIDs = new Map<EnvironmentKey, string>()
  environments.forEach((environment, index) => {
    const x = 120 + index * 440
    const triggerID = `trigger-${environment.key}`
    const deployID = `deploy-${environment.key}`
    nodes.push(
      { id: triggerID, type: 'trigger', name: `${environment.name}代码`, position: { x, y: 70 }, config: { environment: environment.key, branch: environment.branch, events: triggerEvents(environment), tag_pattern: environment.tag_pattern } },
      { id: deployID, type: 'deploy', name: `部署到${environment.name}`, position: { x, y: 350 }, config: { environment: environment.key, release_plan_id: environment.release_plan_id, deployment_target_id: environment.deployment_target_id } },
    )
    entryIDs.set(environment.key, deployID)
    deployIDs.push(deployID)
  })
  const prodIndex = environments.findIndex((item) => item.key === 'prod')
  if (application.release_approval_enabled && prodIndex >= 0) {
    nodes.push({ id: 'approval-prod', type: 'approval', name: '生产发布审核', position: { x: 120 + prodIndex * 440, y: 215 }, config: { environment: 'prod', description: '发布申请人之外的成员审核' } })
    entryIDs.set('prod', 'approval-prod')
    edges.push({ id: uid('edge'), source: 'approval-prod', target: 'deploy-prod' })
  }
  environments.forEach((environment, index) => {
    edges.push({ id: uid('edge'), source: `trigger-${environment.key}`, target: entryIDs.get(environment.key) || `deploy-${environment.key}` })
    if (index === 0) return
    const gateID = `promote-${environment.key}`
    nodes.push({ id: gateID, type: 'manual', name: `放行到${environment.name}`, position: { x: 120 + index * 440 - 220, y: 350 }, config: { environment: environment.key, description: '主动接测或人工放行后继续' } })
    edges.push(
      { id: uid('edge'), source: deployIDs[index - 1], target: gateID },
      { id: uid('edge'), source: gateID, target: entryIDs.get(environment.key) || deployIDs[index] },
    )
  })
  return { nodes, edges }
}

function nodeColor(type: NodeType) {
  return ({ trigger: '#4776e6', manual: '#a66bd8', approval: '#d88732', deploy: '#20a57a' } as Record<NodeType, string>)[type]
}

export default function ReleaseWorkflowView() {
  const user = useAuthStore((state) => state.user)
  const canManage = Boolean(user?.is_superuser || user?.permissions.includes('delivery.manage'))
  const [searchParams, setSearchParams] = useSearchParams()
  const [applications, setApplications] = useState<Application[]>([])
  const [releasePlans, setReleasePlans] = useState<ReleasePlan[]>([])
  const [targets, setTargets] = useState<DeploymentTarget[]>([])
  const [applicationID, setApplicationID] = useState(searchParams.get('application') || '')
  const [workflow, setWorkflow] = useState<Workflow | null>(null)
  const [nodes, setNodes] = useState<WorkflowNode[]>([])
  const [edges, setEdges] = useState<WorkflowEdge[]>([])
  const [viewport, setViewport] = useState({ x: 60, y: 40, zoom: 0.85 })
  const [selectedNodeID, setSelectedNodeID] = useState('')
  const [connectingFrom, setConnectingFrom] = useState('')
  const [issues, setIssues] = useState<WorkflowIssue[]>([])
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [dirty, setDirty] = useState(false)
  const [message, setMessage] = useState('')
  const [error, setError] = useState('')
  const canvasRef = useRef<HTMLDivElement>(null)
  const panRef = useRef<{ pointerID: number; startX: number; startY: number; originX: number; originY: number } | null>(null)
  const dragRef = useRef<{ pointerID: number; nodeID: string; startX: number; startY: number; originX: number; originY: number } | null>(null)

  const application = useMemo(() => applications.find((item) => item.id === applicationID), [applicationID, applications])
  const selectedNode = useMemo(() => nodes.find((item) => item.id === selectedNodeID), [nodes, selectedNodeID])

  const loadResources = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const [applicationResult, planResult, targetResult] = await Promise.all([
        client.get<{ applications: Application[] }>('/applications'),
        client.get<{ release_plans: ReleasePlan[] }>('/release-plans'),
        client.get<{ targets: DeploymentTarget[] }>('/deployment-targets'),
      ])
      setApplications(applicationResult.data.applications)
      setReleasePlans(planResult.data.release_plans)
      setTargets(targetResult.data.targets)
      if (!applicationID && applicationResult.data.applications.length > 0) {
        const id = applicationResult.data.applications[0].id
        setApplicationID(id)
        setSearchParams({ application: id }, { replace: true })
      }
    } catch (loadError) {
      setError(apiErrorMessage(loadError))
    } finally {
      setLoading(false)
    }
  }, [applicationID, setSearchParams])

  const loadWorkflow = useCallback(async (id: string) => {
    setLoading(true)
    setError('')
    setMessage('')
    try {
      const result = await client.get<WorkflowResponse>(`/applications/${id}/workflow`)
      setWorkflow(result.data.workflow)
      setNodes(result.data.workflow.nodes || [])
      setEdges(result.data.workflow.edges || [])
      setViewport(result.data.workflow.viewport || { x: 60, y: 40, zoom: 0.85 })
      setIssues(result.data.issues || [])
      setSelectedNodeID('')
      setDirty(false)
    } catch (loadError) {
      setError(apiErrorMessage(loadError))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { void loadResources() }, [loadResources])
  useEffect(() => { if (applicationID) void loadWorkflow(applicationID) }, [applicationID, loadWorkflow])

  function chooseApplication(id: string) {
    setApplicationID(id)
    setSearchParams({ application: id }, { replace: true })
  }

  function updateNode(id: string, update: (node: WorkflowNode) => WorkflowNode) {
    setNodes((current) => current.map((node) => node.id === id ? update(node) : node))
    setDirty(true)
  }

  function addNode(type: NodeType) {
    if (!application || !canvasRef.current) return
    const rect = canvasRef.current.getBoundingClientRect()
    const environment = application.environments[0]
    const id = uid(type)
    const position = {
      x: (rect.width / 2 - viewport.x) / viewport.zoom - 110,
      y: (rect.height / 2 - viewport.y) / viewport.zoom - 60,
    }
    const node: WorkflowNode = {
      id, type, name: nodeCopy[type].label, position,
      config: {
        environment: environment?.key, branch: type === 'trigger' ? environment?.branch : undefined,
        events: type === 'trigger' ? ['push'] : undefined,
        release_plan_id: type === 'deploy' ? environment?.release_plan_id : undefined,
        deployment_target_id: type === 'deploy' ? environment?.deployment_target_id : undefined,
      },
    }
    setNodes((current) => [...current, node])
    setSelectedNodeID(id)
    setDirty(true)
  }

  function removeNode(id: string) {
    setNodes((current) => current.filter((node) => node.id !== id))
    setEdges((current) => current.filter((edge) => edge.source !== id && edge.target !== id))
    setSelectedNodeID('')
    setConnectingFrom('')
    setDirty(true)
  }

  function connectTo(target: string) {
    if (!connectingFrom || connectingFrom === target) return
    if (edges.some((edge) => edge.source === connectingFrom && edge.target === target)) {
      setConnectingFrom('')
      return
    }
    setEdges((current) => [...current, { id: uid('edge'), source: connectingFrom, target }])
    setConnectingFrom('')
    setDirty(true)
  }

  function removeEdge(id: string) {
    setEdges((current) => current.filter((edge) => edge.id !== id))
    setDirty(true)
  }

  function applyTemplate(compact: boolean) {
    if (!application) return
    const template = createTemplate(application, compact)
    setNodes(template.nodes)
    setEdges(template.edges)
    setViewport({ x: 60, y: 45, zoom: compact ? 1 : 0.72 })
    setSelectedNodeID('')
    setIssues([])
    setDirty(true)
  }

  async function validate() {
    if (!applicationID || !workflow) return
    setError('')
    setMessage('')
    try {
      const result = await client.post<WorkflowResponse>(`/applications/${applicationID}/workflow/validate`, {
        name: workflow.name, revision: workflow.revision, activate: false, nodes, edges, viewport,
      })
      setIssues(result.data.issues || [])
      setMessage(result.data.valid ? '检查通过，可以启用这份发布计划。' : `发现 ${result.data.issues.length} 个问题。`)
    } catch (validateError) {
      setError(apiErrorMessage(validateError))
    }
  }

  async function save(activate: boolean) {
    if (!applicationID || !workflow) return
    setSaving(true)
    setError('')
    setMessage('')
    try {
      const result = await client.put<WorkflowResponse>(`/applications/${applicationID}/workflow`, {
        name: workflow.name, revision: workflow.revision, activate, nodes, edges, viewport,
      })
      setWorkflow(result.data.workflow)
      setNodes(result.data.workflow.nodes)
      setEdges(result.data.workflow.edges)
      setIssues(result.data.issues || [])
      setDirty(false)
      setMessage(activate ? '发布计划已启用，新的代码事件会按这张图进入流程。' : '草稿已保存，当前不会触发新的发布流程。')
    } catch (saveError) {
      const response = (saveError as { response?: { data?: { issues?: WorkflowIssue[] } } }).response?.data
      if (response?.issues) setIssues(response.issues)
      setError(apiErrorMessage(saveError))
    } finally {
      setSaving(false)
    }
  }

  function fitCanvas() {
    if (!canvasRef.current || nodes.length === 0) return
    const rect = canvasRef.current.getBoundingClientRect()
    const minX = Math.min(...nodes.map((node) => node.position.x))
    const minY = Math.min(...nodes.map((node) => node.position.y))
    const maxX = Math.max(...nodes.map((node) => node.position.x + 220))
    const maxY = Math.max(...nodes.map((node) => node.position.y + 132))
    const zoom = Math.max(0.25, Math.min(1.15, Math.min((rect.width - 100) / (maxX - minX), (rect.height - 90) / (maxY - minY))))
    setViewport({ x: (rect.width - (maxX - minX) * zoom) / 2 - minX * zoom, y: (rect.height - (maxY - minY) * zoom) / 2 - minY * zoom, zoom })
    setDirty(true)
  }

  function handleWheel(event: React.WheelEvent<HTMLDivElement>) {
    event.preventDefault()
    const rect = event.currentTarget.getBoundingClientRect()
    const pointerX = event.clientX - rect.left
    const pointerY = event.clientY - rect.top
    const worldX = (pointerX - viewport.x) / viewport.zoom
    const worldY = (pointerY - viewport.y) / viewport.zoom
    const zoom = Math.max(0.2, Math.min(2, viewport.zoom * (event.deltaY > 0 ? 0.9 : 1.1)))
    setViewport({ x: pointerX - worldX * zoom, y: pointerY - worldY * zoom, zoom })
    setDirty(true)
  }

  function startPan(event: React.PointerEvent<HTMLDivElement>) {
    if ((event.target as HTMLElement).closest('.workflow-node')) return
    event.currentTarget.setPointerCapture(event.pointerId)
    panRef.current = { pointerID: event.pointerId, startX: event.clientX, startY: event.clientY, originX: viewport.x, originY: viewport.y }
  }

  function movePan(event: React.PointerEvent<HTMLDivElement>) {
    const pan = panRef.current
    if (!pan || pan.pointerID !== event.pointerId) return
    setViewport((current) => ({ ...current, x: pan.originX + event.clientX - pan.startX, y: pan.originY + event.clientY - pan.startY }))
  }

  function endPan(event: React.PointerEvent<HTMLDivElement>) {
    if (panRef.current?.pointerID === event.pointerId) {
      panRef.current = null
      setDirty(true)
    }
  }

  function startNodeDrag(event: React.PointerEvent<HTMLElement>, node: WorkflowNode) {
    if ((event.target as HTMLElement).closest('.node-port, button, input, select')) return
    event.stopPropagation()
    event.currentTarget.setPointerCapture(event.pointerId)
    dragRef.current = { pointerID: event.pointerId, nodeID: node.id, startX: event.clientX, startY: event.clientY, originX: node.position.x, originY: node.position.y }
    setSelectedNodeID(node.id)
  }

  function moveNode(event: React.PointerEvent<HTMLElement>) {
    const drag = dragRef.current
    if (!drag || drag.pointerID !== event.pointerId) return
    const x = drag.originX + (event.clientX - drag.startX) / viewport.zoom
    const y = drag.originY + (event.clientY - drag.startY) / viewport.zoom
    updateNode(drag.nodeID, (node) => ({ ...node, position: { x, y } }))
  }

  function endNodeDrag(event: React.PointerEvent<HTMLElement>) {
    if (dragRef.current?.pointerID === event.pointerId) dragRef.current = null
  }

  if (loading && applications.length === 0) return <div className="loading-panel">正在准备发布计划…</div>

  return <section className="workflow-page page-enter">
    <div className="workflow-heading">
      <div><span className="section-label">持续交付</span><h2>发布计划</h2><p>用节点和连线定义代码从哪个环境进入、何时接测、是否审核，以及最后部署到哪里。</p></div>
      <div className="workflow-heading-actions">
        <label>应用<select value={applicationID} onChange={(event) => chooseApplication(event.target.value)}>{applications.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
        {workflow && <span className={`workflow-state ${workflow.is_active ? 'active' : ''}`}>{workflow.is_active ? '已启用' : '草稿'}</span>}
        {dirty && <span className="unsaved-state">未保存</span>}
      </div>
    </div>

    {error && <div className="form-alert error system-alert" role="alert">{error}</div>}
    {message && <div className="form-alert system-alert" role="status">{message}</div>}

    {!application || !workflow ? <div className="modern-empty workflow-empty"><h3>先创建应用</h3><p>应用创建后会自动生成一份可编辑的发布计划。</p></div> : <div className="workflow-studio">
      <aside className="workflow-palette">
        <div><strong>节点</strong><span>点击添加到画布</span></div>
        {(Object.keys(nodeCopy) as NodeType[]).map((type) => <button key={type} type="button" onClick={() => addNode(type)} disabled={!canManage}>
          <i style={{ background: nodeColor(type) }}>{nodeCopy[type].icon}</i><span><strong>{nodeCopy[type].label}</strong><small>{nodeCopy[type].hint}</small></span>
        </button>)}
        <div className="palette-divider" />
        <div><strong>常用模板</strong><span>会替换当前画布</span></div>
        <button className="template-button" type="button" onClick={() => applyTemplate(false)} disabled={!canManage}><span><strong>dev → test → pre → prod</strong><small>标准四环境晋级流程</small></span></button>
        <button className="template-button" type="button" onClick={() => applyTemplate(true)} disabled={!canManage}><span><strong>test → prod</strong><small>测试通过后直接生产</small></span></button>
      </aside>

      <div className="workflow-canvas-shell">
        <div className="canvas-toolbar">
          <span>{connectingFrom ? '请选择下一个节点的左侧入口' : '拖动画布移动，滚轮缩放，节点从右侧连到左侧'}</span>
          <div><button type="button" onClick={() => setViewport((current) => ({ ...current, zoom: Math.max(.2, current.zoom - .1) }))}>−</button><b>{Math.round(viewport.zoom * 100)}%</b><button type="button" onClick={() => setViewport((current) => ({ ...current, zoom: Math.min(2, current.zoom + .1) }))}>＋</button><button type="button" onClick={fitCanvas}>适合画布</button></div>
        </div>
        <div ref={canvasRef} className={`workflow-canvas${connectingFrom ? ' connecting' : ''}`} style={{ backgroundPosition: `${viewport.x}px ${viewport.y}px`, backgroundSize: `${24 * viewport.zoom}px ${24 * viewport.zoom}px` }} onWheel={handleWheel} onPointerDown={startPan} onPointerMove={movePan} onPointerUp={endPan} onPointerCancel={endPan}>
          <div className="workflow-world" style={{ transform: `translate(${viewport.x}px, ${viewport.y}px) scale(${viewport.zoom})` }}>
            <svg className="workflow-edges" aria-hidden="true">
              {edges.map((edge) => {
                const source = nodes.find((node) => node.id === edge.source)
                const target = nodes.find((node) => node.id === edge.target)
                if (!source || !target) return null
                const sx = source.position.x + 220, sy = source.position.y + 66
                const tx = target.position.x, ty = target.position.y + 66
                const bend = Math.max(70, Math.abs(tx - sx) * .45)
                return <path key={edge.id} d={`M ${sx} ${sy} C ${sx + bend} ${sy}, ${tx - bend} ${ty}, ${tx} ${ty}`} />
              })}
            </svg>
            {nodes.map((node) => <article key={node.id} className={`workflow-node node-${node.type}${selectedNodeID === node.id ? ' selected' : ''}`} style={{ left: node.position.x, top: node.position.y, '--node-color': nodeColor(node.type) } as React.CSSProperties} onPointerDown={(event) => startNodeDrag(event, node)} onPointerMove={moveNode} onPointerUp={endNodeDrag} onPointerCancel={endNodeDrag} onClick={(event) => { event.stopPropagation(); setSelectedNodeID(node.id) }}>
              <button className="node-port input-port" type="button" aria-label="连接到这个节点" onClick={(event) => { event.stopPropagation(); connectTo(node.id) }} />
              <div className="workflow-node-head"><i>{nodeCopy[node.type].icon}</i><span>{nodeCopy[node.type].label}</span><b>{node.config.environment || '通用'}</b></div>
              <h3>{node.name}</h3>
              <p>{node.type === 'trigger' ? `${node.config.branch || '未配置分支'} · ${(node.config.events || []).join(' / ') || '未选择事件'}` : node.type === 'deploy' ? `${releasePlans.find((item) => item.id === node.config.release_plan_id)?.name || '未绑定发布方案'} · ${targets.find((item) => item.id === node.config.deployment_target_id)?.name || '未绑定目标'}` : node.config.description || nodeCopy[node.type].hint}</p>
              <button className={`node-port output-port${connectingFrom === node.id ? ' active' : ''}`} type="button" aria-label="从这个节点开始连接" onClick={(event) => { event.stopPropagation(); setConnectingFrom((current) => current === node.id ? '' : node.id) }} />
            </article>)}
          </div>
        </div>
        <div className="canvas-actions">
          <div>{issues.length > 0 ? <button className="issue-count" type="button" onClick={() => setSelectedNodeID(issues.find((item) => item.node_id)?.node_id || '')}>{issues.length} 个问题</button> : <span className="valid-state">结构检查通过</span>}</div>
          <div><button type="button" onClick={() => void validate()}>检查计划</button>{canManage && <><button type="button" disabled={saving} onClick={() => void save(false)}>保存草稿</button><button className="primary-button" type="button" disabled={saving} onClick={() => void save(true)}>{saving ? '保存中…' : '启用计划'}</button></>}</div>
        </div>
      </div>

      <aside className="workflow-inspector">
        {!selectedNode ? <div className="inspector-empty"><span>⌘</span><h3>选择一个节点</h3><p>在这里配置环境、分支、触发事件和发布目标。</p>{issues.length > 0 && <ul>{issues.slice(0, 6).map((issue, index) => <li key={`${issue.code}-${index}`}>{issue.message}</li>)}</ul>}</div> : <>
          <div className="inspector-title"><div><span>{nodeCopy[selectedNode.type].label}</span><h3>{selectedNode.name}</h3></div>{canManage && <button type="button" onClick={() => removeNode(selectedNode.id)}>删除</button>}</div>
          <div className="inspector-fields">
            <label>节点名称<input value={selectedNode.name} disabled={!canManage} onChange={(event) => updateNode(selectedNode.id, (node) => ({ ...node, name: event.target.value }))} /></label>
            <label>所属环境<select value={selectedNode.config.environment || ''} disabled={!canManage} onChange={(event) => updateNode(selectedNode.id, (node) => ({ ...node, config: { ...node.config, environment: event.target.value as EnvironmentKey } }))}><option value="">通用节点</option>{application.environments.map((item) => <option key={item.key} value={item.key}>{item.name}</option>)}</select></label>
            {selectedNode.type === 'trigger' && <>
              <label>监听分支<input value={selectedNode.config.branch || ''} disabled={!canManage} onChange={(event) => updateNode(selectedNode.id, (node) => ({ ...node, config: { ...node.config, branch: event.target.value } }))} placeholder="main 或 release/*" /></label>
              <fieldset><legend>触发事件</legend>{['pull', 'push', 'pr', 'tag'].map((eventName) => <label key={eventName}><input type="checkbox" disabled={!canManage} checked={(selectedNode.config.events || []).includes(eventName)} onChange={(event) => updateNode(selectedNode.id, (node) => ({ ...node, config: { ...node.config, events: event.target.checked ? [...(node.config.events || []), eventName] : (node.config.events || []).filter((item) => item !== eventName) } }))} />{eventName === 'pull' ? 'Pull 定时检查' : eventName.toUpperCase()}</label>)}</fieldset>
              {(selectedNode.config.events || []).includes('tag') && <label>Tag 规则<input value={selectedNode.config.tag_pattern || ''} disabled={!canManage} onChange={(event) => updateNode(selectedNode.id, (node) => ({ ...node, config: { ...node.config, tag_pattern: event.target.value } }))} placeholder="v*" /></label>}
            </>}
            {selectedNode.type === 'deploy' && <>
              <label>发布方案<select value={selectedNode.config.release_plan_id || ''} disabled={!canManage} onChange={(event) => updateNode(selectedNode.id, (node) => ({ ...node, config: { ...node.config, release_plan_id: event.target.value } }))}><option value="">请选择</option>{releasePlans.filter((item) => item.is_active).map((item) => <option key={item.id} value={item.id}>{item.name} · {item.kind}</option>)}</select></label>
              <label>发布目标<select value={selectedNode.config.deployment_target_id || ''} disabled={!canManage} onChange={(event) => updateNode(selectedNode.id, (node) => ({ ...node, config: { ...node.config, deployment_target_id: event.target.value } }))}><option value="">请选择</option>{targets.filter((item) => item.is_active).map((item) => <option key={item.id} value={item.id}>{item.name} · {item.environment}</option>)}</select></label>
            </>}
            {(selectedNode.type === 'manual' || selectedNode.type === 'approval') && <label>说明<textarea rows={4} value={selectedNode.config.description || ''} disabled={!canManage} onChange={(event) => updateNode(selectedNode.id, (node) => ({ ...node, config: { ...node.config, description: event.target.value } }))} /></label>}
          </div>
          <div className="connection-list"><strong>连线</strong>{edges.filter((edge) => edge.source === selectedNode.id || edge.target === selectedNode.id).map((edge) => {
            const otherID = edge.source === selectedNode.id ? edge.target : edge.source
            const other = nodes.find((node) => node.id === otherID)
            return <div key={edge.id}><span>{edge.source === selectedNode.id ? '到' : '从'} {other?.name || otherID}</span>{canManage && <button type="button" onClick={() => removeEdge(edge.id)}>×</button>}</div>
          })}</div>
          {issues.filter((issue) => issue.node_id === selectedNode.id).map((issue, index) => <div className="node-issue" key={`${issue.code}-${index}`}>{issue.message}</div>)}
        </>}
      </aside>
    </div>}
  </section>
}
