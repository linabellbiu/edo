import { useCallback, useEffect, useState } from 'react'

import client from '@/api/client'
import { apiErrorMessage, getResources, postResource, type ResourceRecord } from '@/api/resources'
import JsonCreatePanel from '@/components/JsonCreatePanel'
import ResourceTable from '@/components/ResourceTable'
import { useAuthStore } from '@/stores/auth'

export default function DeploymentView() {
  const user = useAuthStore((state) => state.user)
  const allowed = (permission: string) => Boolean(user?.is_superuser || user?.permissions.includes(permission))
  const [targets, setTargets] = useState<ResourceRecord[]>([])
  const [deployments, setDeployments] = useState<ResourceRecord[]>([])
  const [targetID, setTargetID] = useState('')
  const [image, setImage] = useState('')
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)

  const refresh = useCallback(async () => {
    setError('')
    try {
      const [nextTargets, nextDeployments] = await Promise.all([
        getResources('/deployment-targets', 'targets'),
        getResources('/deployments', 'deployments'),
      ])
      setTargets(nextTargets)
      setDeployments(nextDeployments)
      setTargetID((current) => current || String(nextTargets[0]?.id ?? ''))
    } catch (loadError) {
      setError(apiErrorMessage(loadError))
    }
  }, [])

  useEffect(() => { void refresh() }, [refresh])

  async function requestDeployment(event: React.FormEvent) {
    event.preventDefault()
    setSubmitting(true)
    setError('')
    try {
      await client.post('/deployments', { target_id: targetID, image })
      setImage('')
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
      await postResource(endpoint)
      await refresh()
    } catch (actionError) {
      setError(apiErrorMessage(actionError))
    }
  }

  return (
    <section className="management-page">
      <div className="page-heading">
        <div><span className="section-label">IMMUTABLE RELEASES</span><h2>应用发布</h2><p>生产镜像强制使用 digest，并由非申请人审批；执行目标使用审批时快照。</p></div>
        <button className="refresh-button" type="button" onClick={() => void refresh()}>刷新</button>
      </div>
      {error && <div className="form-alert error system-alert">{error}</div>}
      {allowed('deployment.run') && (
        <form className="inline-form" onSubmit={(event) => void requestDeployment(event)}>
          <label>发布目标<select value={targetID} onChange={(event) => setTargetID(event.target.value)} required>
            {targets.filter((target) => target.is_active).map((target) => <option key={String(target.id)} value={String(target.id)}>{String(target.name)} · {String(target.environment)}</option>)}
          </select></label>
          <label className="grow">镜像<input value={image} onChange={(event) => setImage(event.target.value)} placeholder="registry.example.com/team/app:version；生产环境请使用 @sha256:…" required /></label>
          <button className="primary-button" disabled={submitting || !targetID} type="submit">{submitting ? '提交中…' : '发起发布'}</button>
        </form>
      )}
      <div className="resource-section">
        <div className="section-heading"><h3>发布记录</h3><span>{deployments.length} 条</span></div>
        <div className="resource-panel"><ResourceTable rows={deployments} columns={[
          { key: 'target_name', label: '目标' }, { key: 'environment', label: '环境' }, { key: 'operation', label: '操作' },
          { key: 'image', label: '镜像' }, { key: 'status', label: '状态' }, { key: 'requested_by', label: '申请人' },
          { key: 'warning_message', label: '告警' }, { key: 'created_at', label: '时间' },
        ]} actions={(row) => <>
          {row.status === 'awaiting_approval' && allowed('deployment.review') && <button type="button" onClick={() => void action(`/deployments/${String(row.id)}/approve`)}>审批</button>}
          {row.status === 'succeeded' && row.previous_image && allowed('deployment.review') && <button type="button" onClick={() => void action(`/deployments/${String(row.id)}/rollback`)}>回滚</button>}
        </>} /></div>
      </div>
      <div className="resource-section">
        <div className="section-heading"><h3>发布目标</h3><span>{targets.length} 个</span></div>
        <div className="resource-panel"><ResourceTable rows={targets} columns={[
          { key: 'name', label: '名称' }, { key: 'platform', label: '平台' }, { key: 'environment', label: '环境' },
          { key: 'namespace', label: '命名空间' }, { key: 'workload_name', label: '工作负载' }, { key: 'container_name', label: '容器' },
          { key: 'rollout_timeout', label: '超时(秒)' }, { key: 'is_active', label: '启用' },
        ]} /></div>
      </div>
      {allowed('deployment.manage') && <JsonCreatePanel title="发布目标" endpoint="/deployment-targets" example={{
        name: 'production-api', platform: 'kubernetes', environment: 'production', runtime_id: '请替换为集群或 Docker 连接 ID',
        namespace: 'default', workload_name: 'api', container_name: 'api', rollout_timeout: 300,
      }} onCreated={() => void refresh()} />}
    </section>
  )
}
