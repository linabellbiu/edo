import { createApp, h, ref, computed } from 'vue'
import { createPinia } from 'pinia'
import i18n from '/src/locales'
import { ConfigProvider, Button, theme } from 'ant-design-vue'
import 'ant-design-vue/dist/reset.css'
import '/src/styles/vben.css'
import '/src/styles/resource-list.css'
import DeliveryVariablesView from '/src/views/DeliveryVariablesView.vue'
import { useAuthStore } from '/src/stores/auth'
import client from '/src/api/client'

// 仅用于页面交互验收；合成接口不会访问或修改真实项目。
localStorage.setItem('edo.project', 'env-fixture-project')
const pinia = createPinia(), auth = useAuthStore(pinia)
auth.user = { id: 'env-fixture-user', username: 'env-fixture', nickname: '变量验收', project_id: 'env-fixture-project', department_id: 'env-fixture-department', is_superuser: true, permissions: [] }
auth.authorizationState = 'ready'
const rows = []
client.defaults.adapter = async config => {
  let data = {}
  if (config.url === '/projects') data = { projects: [{ id: 'env-fixture-project', name: '合成验收项目' }] }
  if (config.url === '/applications') data = { applications: [{ id: 'env-fixture-app', name: '合成应用' }] }
  if (config.url === '/environments') data = { environments: [{ id: 'env-fixture-test', name: '测试环境' }, { id: 'env-fixture-prod', name: '生产环境' }] }
  if (config.url === '/delivery-variables' && config.method === 'get') data = { variables: [...rows] }
  if (config.method === 'post') {
    const body = JSON.parse(config.data)
    const row = { ...body, id: `env-fixture-${rows.length}`, project_id: 'env-fixture-project', version: 1, ...(body.is_secret ? { value: undefined } : {}) }
    rows.unshift(row); data = { variable: row }
  }
  return { data, status: 200, statusText: 'OK', headers: {}, config }
}
const app = createApp({ setup() {
  const dark = ref(false), antTheme = computed(() => ({ algorithm: dark.value ? theme.darkAlgorithm : theme.defaultAlgorithm }))
  return () => h(ConfigProvider, { theme: antTheme.value }, { default: () => h('main', { style: 'padding:24px' }, [
    h(Button, { onClick() { dark.value = !dark.value; document.documentElement.dataset.theme = dark.value ? 'dark' : 'light' } }, () => '切换主题'),
    h('p', '合成接口页面验收，不代表实际服务部署。'), h(DeliveryVariablesView),
  ]) })
} })
app.use(pinia).use(i18n).mount('#app')
