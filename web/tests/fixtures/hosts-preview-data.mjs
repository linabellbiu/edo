const MiB = 1024 ** 2
const GiB = 1024 ** 3
export const hostPreviewScenarios = ['ready', 'empty', 'error', 'readonly', 'no-resources', 'docker-empty', 'docker-error', 'metadata-error']
export const normalizeHostPreviewScenario = value => hostPreviewScenarios.includes(value) ? value : 'ready'

// 仅用于本地 UI 验收；地址使用文档保留网段，不包含真实主机、凭据或业务记录。
export function createHostsPreviewData(scenario = 'ready', now = Date.now()) {
  scenario = normalizeHostPreviewScenario(scenario)
  const at = minutes => new Date(now - minutes * 60_000).toISOString()
  const base = { ssh_port: 22, ssh_username: 'operator', ssh_auth_type: 'private_key', ssh_host_key_fingerprint: 'SHA256:preview-fingerprint-only', architecture: 'amd64', is_active: true, credential_configured: true, created_at: at(1440), updated_at: at(2) }
  const hosts = [
    { ...base, id: 'preview-local', name: '本地', mode: 'local', address: '', is_builtin: true, environment_ids: ['preview-production'], capabilities: [{ kind: 'docker', runtime_id: 'preview-docker-local', status: 'ready', version: '28.4.0' }, { kind: 'local_exec', runtime_id: '', status: 'ready' }], capability_options: [{ kind: 'docker', available: true, version: '28.4.0' }, { kind: 'local_exec', available: true }] },
    { ...base, id: 'preview-edge', name: '边缘节点-用于验证很长的主机名称与运行资源关联', mode: 'ssh', address: '192.0.2.41', is_builtin: false, environment_ids: ['preview-production', 'preview-testing'], capabilities: [{ kind: 'ssh', runtime_id: '', status: 'ready' }, { kind: 'docker', runtime_id: 'preview-docker-edge', status: 'ready', version: '27.5.1' }, { kind: 'kubernetes', runtime_id: 'preview-k3s', status: 'ready', version: 'v1.34.1+k3s1' }] },
    { ...base, id: 'preview-offline', name: '备用归档主机', mode: 'ssh', address: '192.0.2.42', is_builtin: false, is_active: false, architecture: 'arm64', environment_ids: ['preview-testing'], capabilities: [{ kind: 'ssh', runtime_id: '', status: 'unchecked' }, { kind: 'docker', runtime_id: 'preview-docker-offline', status: 'unreachable' }] },
  ]
  const environments = [
    { id: 'preview-production', name: '生产环境', description: '本地预览分组', is_active: true, hosts: hosts.slice(0, 2) },
    { id: 'preview-testing', name: '测试环境与较长的分组名称', description: '本地预览分组', is_active: true, hosts: hosts.slice(1) },
  ]
  const clusters = [{ id: 'preview-k3s', name: '预览 K3s 集群', api_server: 'https://192.0.2.41:6443', default_namespace: 'preview', is_active: true, hosts: [hosts[1]], created_at: at(1440), updated_at: at(2) }]
  const endpoints = [
    { id: 'preview-docker-local', name: '本地 Docker', local: true, is_active: true, host_id: hosts[0].id },
    { id: 'preview-docker-edge', name: '边缘 Docker', local: false, host: '192.0.2.41:22', is_active: true, host_id: hosts[1].id },
    { id: 'preview-docker-offline', name: '归档 Docker', local: false, host: '192.0.2.42:22', is_active: false, host_id: hosts[2].id },
  ]
  const containers = [
    { id: 'preview-container-web', names: ['/preview-web'], image: 'registry.example.com/team/web:2026.10.28', image_display: 'registry.example.com/team/web:2026.10.28', state: 'running', status: 'Up 2 hours (healthy)', created: Math.floor(now / 1000) - 7200, ports: [{ IP: '0.0.0.0', PrivatePort: 8080, PublicPort: 18080, Type: 'tcp' }] },
    { id: 'preview-container-worker', names: ['/preview-long-running-worker-with-a-name-that-needs-to-wrap'], image: 'registry.example.com/team/worker:2026.10.28', image_display: 'registry.example.com/team/worker:2026.10.28', state: 'running', status: 'Up 35 minutes', created: Math.floor(now / 1000) - 2100, ports: [] },
    { id: 'preview-container-stopped', names: ['/preview-completed-task'], image: 'alpine:3.21', image_display: 'alpine:3.21', state: 'exited', status: 'Exited (0) 4 hours ago', created: Math.floor(now / 1000) - 14400, ports: [] },
    { id: 'preview-container-restarting', names: ['/preview-restarting'], image: 'registry.example.com/team/retry:1', image_display: 'registry.example.com/team/retry:1', state: 'restarting', status: 'Restarting (1) 12 seconds ago', created: Math.floor(now / 1000) - 600, ports: [] },
  ]
  const resources = {
    'preview-local': { state: 'ok', source: 'local', scope: 'current_environment', cpu_cores: 8, memory_total_bytes: 16 * GiB, disk_total_bytes: 512 * GiB, disk_mount: '/', os: 'Ubuntu 24.04 LTS', collected_at: at(0) },
    'preview-edge': { state: 'ok', source: 'ssh', scope: 'current_environment', cpu_cores: 4, memory_total_bytes: 8 * GiB, disk_total_bytes: null, disk_mount: '/', os: 'Debian GNU/Linux 12', collected_at: at(0) },
    'preview-offline': { state: 'unavailable', source: 'ssh', scope: 'current_environment', cpu_cores: null, memory_total_bytes: null, disk_total_bytes: null, disk_mount: '/', os: '', collected_at: at(0) },
  }
  const topology = { cluster_id: clusters[0].id, nodes: [{ node: { apiVersion: 'v1', kind: 'Node', metadata: { uid: 'preview-node', name: 'edge-worker', labels: { 'node-role.kubernetes.io/control-plane': 'true' } }, status: { addresses: [{ type: 'InternalIP', address: hosts[1].address }], conditions: [{ type: 'Ready', status: 'True' }] } }, roles: ['control-plane', 'worker'], cpu: { capacity: 4, allocatable: 3.8, used: .2 }, memory: { capacity: 8 * GiB, allocatable: 7.5 * GiB, used: 768 * MiB }, pods: { count: 4, capacity: 110, state: 'ok' }, hosts: [hosts[1]], metrics_timestamp: at(0) }], hosts: [{ ...hosts[1], state: 'matched', node_uids: ['preview-node'], roles: ['control-plane', 'worker'] }], hosts_state: 'ok', metrics_state: 'ok', collected_at: at(0) }
  const permissions = scenario === 'readonly' ? ['cluster.read', 'deployment.read', 'kubernetes.node.read'] : scenario === 'no-resources' ? ['deployment.read'] : ['cluster.read', 'cluster.create', 'cluster.update', 'cluster.delete', 'cluster.execute', 'deployment.read', 'deployment.create', 'deployment.update', 'deployment.delete', 'terminal.open', ...['node', 'workload', 'config', 'storage', 'network'].map(kind => `kubernetes.${kind}.read`)]
  return { scenario, hosts: scenario === 'empty' ? [] : hosts, environments: scenario === 'empty' ? [] : environments, clusters: scenario === 'empty' ? [] : clusters, endpoints: scenario === 'empty' ? [] : endpoints, containers: scenario === 'docker-empty' || scenario === 'empty' ? [] : containers, resources, topology, permissions }
}

function previewError(message, config, status = 503) {
  return Object.assign(new Error(message), { isAxiosError: true, config, response: { status, data: { message } } })
}

export function createHostsPreviewAdapter(data = createHostsPreviewData(), requests = []) {
  return async config => {
    config.signal?.throwIfAborted()
    const method = String(config.method || 'get').toLowerCase()
    const url = new URL(config.url, 'http://preview.invalid')
    requests.push({ method, path: url.pathname, search: url.search })
    if (method !== 'get') throw previewError('本地预览不提交主机或运行资源变更', config, 405)
    if (url.origin !== 'http://preview.invalid') throw previewError('本地预览不连接外部接口', config, 400)
    const path = url.pathname
    let response
    if (path === '/hosts' || path === '/hosts/statuses') {
      if (data.scenario === 'error') throw previewError('预览场景：主机列表暂不可用，请重试', config)
      response = { hosts: path.endsWith('/statuses') ? data.hosts.map(({ id, is_active, capabilities }) => ({ id, is_active, capabilities })) : data.hosts }
    } else if (path === '/environments') response = { environments: data.environments }
    else if (path === '/kubernetes/clusters') response = { clusters: data.clusters }
    else if (path === '/docker/endpoints') {
      if (data.scenario === 'metadata-error') throw previewError('预览场景：Docker 运行时列表暂不可用', config)
      response = { endpoints: data.endpoints }
    } else if (/^\/hosts\/[^/]+\/resources$/.test(path)) {
      const id = decodeURIComponent(path.split('/')[2])
      if (!data.hosts.some(host => host.id === id)) throw previewError('预览主机不存在', config, 404)
      response = { resources: data.resources[id] }
    } else if (/^\/docker\/endpoints\/[^/]+\/containers$/.test(path)) {
      if (!data.endpoints.some(endpoint => endpoint.id === decodeURIComponent(path.split('/')[3]))) throw previewError('预览 Docker 运行时不存在', config, 404)
      if (data.scenario === 'docker-error') throw previewError('预览场景：容器加载失败，请重试', config)
      response = { containers: url.searchParams.get('all') === 'true' || config.params?.all === true || config.params?.all === 'true' ? data.containers : data.containers.filter(container => container.state === 'running') }
    } else if (path === '/kubernetes/clusters/preview-k3s/topology') response = data.topology
    else if (path === '/kubernetes/clusters/preview-k3s/pods') response = { pods: [{ name: 'preview-web', namespace: 'preview', phase: 'Running', containers: ['web'] }, { name: 'preview-pending', namespace: 'preview', phase: 'Pending', containers: ['worker'] }] }
    else throw previewError('本地预览未登记此只读接口', config, 404)
    return { data: structuredClone(response), status: 200, statusText: 'OK', headers: {}, config }
  }
}
