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
  client.get = async path => {
    if (path !== '/kubernetes/k3s/manual-uninstall-guide') throw new Error('夹具禁止真实接口请求')
    if (new URLSearchParams(location.search).get('record') !== 'present') return { data: { cluster_id: 'fixture-cluster', cluster_name: '没有安装记录的集群 · UI 夹具', local: false, list_command: 'k3d cluster list' } }
    return { data: { cluster_id: 'fixture-cluster', cluster_name: '测试集群 · 本地 UI 夹具', resource_name: 'edo-fixturecluster00000000000000', host: { id: 'fixture-host', name: '原安装主机', address: '192.0.2.10' }, local: false, docker_id: 'fixture-docker-id', command: "k3d cluster delete 'edo-fixturecluster00000000000000'", list_command: 'k3d cluster list' } }
  }
  return () => h(ConfigProvider, { theme: { algorithm: dark ? theme.darkAlgorithm : theme.defaultAlgorithm } }, { default: () => [h('p', { style: 'padding:20px' }, '仅用于视觉测试，不执行任何卸载。'), h(Guide, { open: open.value, cluster: { id: 'fixture-cluster', name: '测试集群' }, 'onUpdate:open': value => open.value = value })] })
} })
app.use(createPinia()).use(i18n).use(Antd).mount('#root')
