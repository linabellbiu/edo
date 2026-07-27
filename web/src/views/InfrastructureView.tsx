import { lazy, Suspense, useCallback, useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'

import client from '@/api/client'
import { apiErrorMessage, getResources, type ResourceRecord } from '@/api/resources'
import DockerSSHForm from '@/components/DockerSSHForm'
import JsonCreatePanel from '@/components/JsonCreatePanel'
import ResourceTable from '@/components/ResourceTable'
import { useAuthStore } from '@/stores/auth'

const TerminalModal = lazy(() => import('@/components/TerminalModal'))

interface TerminalTarget { title: string; path: string }

export default function InfrastructureView() {
  const [searchParams] = useSearchParams()
  const user = useAuthStore((state) => state.user)
  const allowed = (permission: string) => Boolean(user?.is_superuser || user?.permissions.includes(permission))
  const [dockerEndpoints, setDockerEndpoints] = useState<ResourceRecord[]>([])
  const [clusters, setClusters] = useState<ResourceRecord[]>([])
  const [containers, setContainers] = useState<ResourceRecord[]>([])
  const [pods, setPods] = useState<ResourceRecord[]>([])
  const [dockerID, setDockerID] = useState('')
  const [clusterID, setClusterID] = useState('')
  const [namespace, setNamespace] = useState('default')
  const [terminal, setTerminal] = useState<TerminalTarget | null>(null)
  const [error, setError] = useState('')

  const refresh = useCallback(async () => {
    setError('')
    try {
      const [endpoints, nextClusters] = await Promise.all([
        getResources('/docker/endpoints', 'endpoints'), getResources('/kubernetes/clusters', 'clusters'),
      ])
      setDockerEndpoints(endpoints)
      setClusters(nextClusters)
      setDockerID((current) => current || String(endpoints[0]?.id ?? ''))
      setClusterID((current) => current || String(nextClusters[0]?.id ?? ''))
    } catch (loadError) {
      setError(apiErrorMessage(loadError))
    }
  }, [])

  useEffect(() => { void refresh() }, [refresh])

  async function loadContainers() {
    if (!dockerID) return
    setError('')
    try { setContainers(await getResources(`/docker/endpoints/${dockerID}/containers?all=true`, 'containers')) }
    catch (loadError) { setError(apiErrorMessage(loadError)) }
  }

  async function loadPods() {
    if (!clusterID || !namespace) return
    setError('')
    try { setPods(await getResources(`/kubernetes/clusters/${clusterID}/pods?namespace=${encodeURIComponent(namespace)}`, 'pods')) }
    catch (loadError) { setError(apiErrorMessage(loadError)) }
  }

  async function ping(endpoint: string) {
    setError('')
    try { await client.post(endpoint) }
    catch (pingError) { setError(apiErrorMessage(pingError)) }
  }

  return (
    <section className="management-page">
      <div className="page-heading"><div><span className="section-label">CONTAINER RUNTIMES</span><h2>容器与集群</h2><p>Docker 主机通过 SSH 地址、端口和密码或私钥连接；发布时在目标主机执行受控的 docker pull，不提供宿主机交互终端。</p></div><button className="refresh-button" type="button" onClick={() => void refresh()}>刷新</button></div>
      {error && <div className="form-alert error system-alert">{error}</div>}
      <div className="resource-section"><div className="section-heading"><h3>Docker 连接</h3><span>{dockerEndpoints.length} 个</span></div>
        <div className="resource-panel"><ResourceTable rows={dockerEndpoints} columns={[
          { key: 'name', label: '名称' }, { key: 'host', label: 'SSH 地址' }, { key: 'ssh_configured', label: '凭据' }, { key: 'is_active', label: '启用' },
        ]} actions={(row) => allowed('cluster.manage') && <button type="button" onClick={() => void ping(`/docker/endpoints/${String(row.id)}/ping`)}>检查</button>} /></div>
        {allowed('cluster.manage') && <DockerSSHForm initialOpen={searchParams.get('create') === 'docker' || searchParams.get('create') === 'ssh'} onCreated={() => void refresh()} />}
      </div>
      <div className="resource-section"><div className="section-heading"><h3>Kubernetes 集群</h3><span>{clusters.length} 个</span></div>
        <div className="resource-panel"><ResourceTable rows={clusters} columns={[
          { key: 'name', label: '名称' }, { key: 'mode', label: '模式' }, { key: 'api_server', label: 'API Server' }, { key: 'default_namespace', label: '默认命名空间' }, { key: 'is_active', label: '启用' },
        ]} actions={(row) => allowed('cluster.manage') && <button type="button" onClick={() => void ping(`/kubernetes/clusters/${String(row.id)}/ping`)}>检查</button>} /></div>
        {allowed('cluster.manage') && <JsonCreatePanel title="Kubernetes 集群" endpoint="/kubernetes/clusters" example={{ name: 'production-k8s', mode: 'kubeconfig', default_namespace: 'default', kubeconfig: '请粘贴不包含 exec 插件和外部文件引用的 kubeconfig' }} onCreated={() => void refresh()} />}
      </div>
      {allowed('terminal.open') && <div className="runtime-browser">
        <div className="resource-section"><div className="section-heading"><h3>Docker 容器</h3></div><div className="inline-form compact"><select value={dockerID} onChange={(event) => setDockerID(event.target.value)}>{dockerEndpoints.map((item) => <option key={String(item.id)} value={String(item.id)}>{String(item.name)}</option>)}</select><button type="button" onClick={() => void loadContainers()}>加载容器</button></div>
          <div className="resource-panel"><ResourceTable rows={containers} columns={[{ key: 'names', label: '名称' }, { key: 'image', label: '镜像' }, { key: 'state', label: '状态' }, { key: 'status', label: '详情' }]} actions={(row) => row.state === 'running' && <button type="button" onClick={() => setTerminal({ title: `Docker · ${String(row.names)}`, path: `/api/v1/terminals/docker/${encodeURIComponent(dockerID)}/containers/${encodeURIComponent(String(row.id))}/ws` })}>终端</button>} /></div>
        </div>
        <div className="resource-section"><div className="section-heading"><h3>Kubernetes Pods</h3></div><div className="inline-form compact"><select value={clusterID} onChange={(event) => setClusterID(event.target.value)}>{clusters.map((item) => <option key={String(item.id)} value={String(item.id)}>{String(item.name)}</option>)}</select><input value={namespace} onChange={(event) => setNamespace(event.target.value)} placeholder="命名空间" /><button type="button" onClick={() => void loadPods()}>加载 Pods</button></div>
          <div className="resource-panel"><ResourceTable rows={pods} columns={[{ key: 'name', label: 'Pod' }, { key: 'namespace', label: '命名空间' }, { key: 'phase', label: '阶段' }, { key: 'containers', label: '容器' }]} actions={(row) => row.phase === 'Running' && Array.isArray(row.containers) && row.containers.map((container) => <button type="button" key={String(container)} onClick={() => setTerminal({ title: `Pod · ${String(row.name)} / ${String(container)}`, path: `/api/v1/terminals/kubernetes/${encodeURIComponent(clusterID)}/namespaces/${encodeURIComponent(String(row.namespace))}/pods/${encodeURIComponent(String(row.name))}/containers/${encodeURIComponent(String(container))}/ws` })}>{String(container)}</button>)} /></div>
        </div>
      </div>}
      {terminal && <Suspense fallback={null}><TerminalModal title={terminal.title} path={terminal.path} onClose={() => setTerminal(null)} /></Suspense>}
    </section>
  )
}
