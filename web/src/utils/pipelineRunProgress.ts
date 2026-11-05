import type { PipelineExecutionGraph, PipelineStageTiming } from '@/types/pipeline'

export interface PipelineRunProgressRecord {
  id: string
  status: string
  stage?: string
  current_node_id?: string
  execution_graph?: PipelineExecutionGraph
  timing?: PipelineStageTiming
}

export function pipelineRunProgress(run: PipelineRunProgressRecord) {
  const stages = run.execution_graph?.stages || []
  const current = stages.find(stage => (stage.tasks || []).some(task => task.id === run.current_node_id))
  const failed = stages.find(stage => stage.timing?.state === 'failed')
  const active = stages.filter(stage => stage.timing?.running)
  const executed = stages.filter(stage => stage.timing && !['pending', 'not_executed', 'unavailable', 'skipped'].includes(stage.timing.state))
  const source = run.current_node_id && run.current_node_id === run.execution_graph?.source.id ? run.execution_graph.source.name : ''
  const phase = ({ checkout: '拉取代码', build: '构建', deploy: '部署', approval: '审核', manual: '人工放行' } as Record<string, string>)[run.stage || '']
  let label = '等待执行', state = 'pending', breathing = false
  if (run.status === 'succeeded') { label = '成功'; state = 'succeeded' }
  else if (run.status === 'failed') { label = '失败'; state = 'failed' }
  else if (run.status === 'canceled') { label = '已停止'; state = 'canceled' }
  else if (run.status === 'skipped') { label = '已跳过'; state = 'skipped' }
  else if (run.stage === 'stopping') { label = '停止中'; state = 'waiting' }
  else if (run.status === 'awaiting_approval') { label = '等待审核'; state = 'waiting' }
  else if (run.status === 'blocked' || run.stage === 'manual') { label = '等待放行'; state = 'waiting' }
  else if (run.status === 'running' && run.stage === 'queued') { label = '排队中'; state = 'waiting' }
  else if (run.status === 'running') { label = '运行中'; state = 'running'; breathing = true }

  const terminal = ['succeeded', 'failed', 'canceled', 'skipped'].includes(run.status)
  const stageName = (run.status === 'failed' ? failed?.name : '') ||
    (!terminal ? active.map(stage => stage.name).join('、') : '') || current?.name || source ||
    (terminal ? executed.at(-1)?.name : '') || phase || ''
  return { state, label, breathing, stageName }
}
