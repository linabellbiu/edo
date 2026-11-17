import 'ant-design-vue/dist/reset.css'
import '/src/styles/vben.css'
import { createApp, h, ref } from 'vue'
import { createPinia } from 'pinia'
import Antd, { ConfigProvider, theme } from 'ant-design-vue'
import i18n from '/src/locales'
import { useAuthStore } from '/src/stores/auth'
import client from '/src/api/client'
import Guide from '/src/components/K3dManualUninstallModal.vue'

// 独立视觉夹具只读取固定数据，不连接主机、Docker 或 Kubernetes。
const project = 'manual-guide-fixture'
localStorage.setItem('edo.project', project)
const app = createApp({ setup() {
  const auth = useAuthStore(); auth.user = { id: 'fixture-actor', project_id: project, department_id: 'fixture-dept', permissions: ['cluster.read', 'kubernetes.k3s.uninstall'] }; auth.authorizationState = 'ready'
  const open = ref(true), dark = new URLSearchParams(location.search).get('theme') === 'dark'
  document.documentElement.dataset.theme = dark ? 'dark' : 'light'
  const executions = [{ id: 'remote', label: 'SSH 主机', instruction: '登录实际安装主机的新地址，使用 root 或 sudo 执行以下命令。', list_command: `sh -c 'set -eu\nedo_k3d=/var/lib/edo/k3d/fixture/k3d-linux-amd64\nexec \"$edo_k3d\" cluster list'`, delete_command: `sh -c 'set -eu\nedo_k3d=/var/lib/edo/k3d/fixture/k3d-linux-amd64\nexec \"$edo_k3d\" cluster delete __EDO_K3D_NAME__'` }, { id: 'local', label: 'EDO 本地 Docker', instruction: '在 EDO 后端运行环境中执行。Docker Compose 部署先进入后端。', enter_command: 'docker compose exec api sh', list_command: `sh -c 'exec /fixture/edo-cache/k3d cluster list'`, delete_command: `sh -c 'exec /fixture/edo-cache/k3d cluster delete __EDO_K3D_NAME__'` }]
  client.get = async path => {
    if (path !== '/kubernetes/k3s/manual-uninstall-guide') throw new Error('夹具禁止真实接口请求')
    if (new URLSearchParams(location.search).get('record') !== 'present') return { data: { cluster_id: 'fixture-cluster', cluster_name: '没有安装记录的集群 · UI 夹具', local: new URLSearchParams(location.search).get('local') === '1', executions } }
    return { data: { cluster_id: 'fixture-cluster', cluster_name: '测试集群 · 本地 UI 夹具', resource_name: 'edo-fixturecluster00000000000000', host: { id: 'fixture-host', name: '原安装主机', address: '192.0.2.10' }, local: new URLSearchParams(location.search).get('local') === '1', docker_id: 'fixture-docker-id', executions } }
  }
  return () => h(ConfigProvider, { theme: { algorithm: dark ? theme.darkAlgorithm : theme.defaultAlgorithm } }, { default: () => [h('p', { style: 'padding:20px' }, '仅用于视觉测试，不执行任何卸载。'), h(Guide, { open: open.value, cluster: { id: 'fixture-cluster', name: '测试集群' }, 'onUpdate:open': value => open.value = value })] })
} })
app.use(createPinia()).use(i18n).use(Antd).mount('#root')
