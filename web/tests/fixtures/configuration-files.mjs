import { createApp, h, ref, computed } from 'vue'
import { createPinia } from 'pinia'
import { ConfigProvider, Button, Form, theme } from 'ant-design-vue'
import 'ant-design-vue/dist/reset.css'
import '/src/styles/vben.css'
import '/src/styles/resource-list.css'
import DeliveryVariablesView from '/src/views/DeliveryVariablesView.vue'
import ComposeTaskFields from '/src/components/ComposeTaskFields.vue'
import ComposeContainerControlModal from '/src/components/ComposeContainerControlModal.vue'
import client from '/src/api/client'
import i18n from '/src/locales'
import { useAuthStore } from '/src/stores/auth'
import { usePreferencesStore } from '/src/stores/preferences'

// 只使用合成数据，不连接实际业务 API。
const project = 'configuration-preview', pinia = createPinia(), auth = useAuthStore(pinia), preferences = usePreferencesStore(pinia)
localStorage.setItem('edo.project', project)
auth.user = { id: 'preview-user', username: 'preview', project_id: project, department_id: 'preview-department', is_superuser: false, permissions: ['delivery_variable.read', 'delivery_variable.create', 'delivery_variable.update', 'delivery_variable.delete', 'delivery_variable.reveal', 'deployment.read', 'deployment.execute', 'deployment_plan.read', 'deployment_plan.update', 'application.read', 'workflow.read', 'workflow.update', 'deployment.container.restart', 'deployment.container.recreate', 'delivery_variable.execute'] }
auth.loaded = true; auth.authorizationState = 'ready'
const rows = [{ id: 'env-config', project_id: project, name: 'orders_runtime', kind: 'config_env', usage: 'deploy', is_active: true, is_secret: true, application_id: 'app', environment_id: 'prod', version: 2 },{ id:'yaml-config',project_id:project,name:'orders_settings',kind:'config_yaml',usage:'deploy',is_active:true,is_secret:false,application_id:'app',environment_id:'',version:3,value:'server:\n  port: 9090\n  mode: production\n',protected_refs:[] }]
const history = {
  'env-config': ['TOKEN=synthetic-old\nPORT=8080\n', 'TOKEN=synthetic-new\nPORT=9090\n'],
  'yaml-config': ['server:\n  port: 8080\n', 'server:\n  port: 8080\n  mode: development\n', 'server:\n  port: 9090\n  mode: production\n'],
}
const plans = ['compose_stack', 'compose', 'docker'].map(kind => ({ id: 'plan-' + kind, kind, timeout_seconds: 600, service_name: 'api', compose_yaml: 'services:\n  api:\n    image: example/api:${IMAGE_TAG}\n', deployment_target: { host_id: 'host', environment_id: 'prod', working_directory: '/srv/example', workload_name: 'example-api' } }))
client.defaults.adapter = async config => {
  let data
  const path = config.url, body = typeof config.data === 'string' ? JSON.parse(config.data) : config.data
  const historical = /^\/delivery-variables\/([^/]+)\/revisions(?:\/(\d+)(\/reveal)?)?$/.exec(path)
  if(historical){
    const row=rows.find(item=>item.id===historical[1]),values=history[row.id]
    const revision=version=>({version,is_secret:row.is_secret,created_by:'preview-user',created_by_name:'示例编辑者',created_at:`2026-11-0${version}T03:00:00Z`,hidden:row.is_secret})
    data=historical[2]?{revision:{...revision(Number(historical[2])),...(!row.is_secret||historical[3]?{value:values[Number(historical[2])-1],hidden:false}:{})}}:{revisions:values.map((_,i)=>revision(i+1)).reverse(),current_version:row.version,total:values.length}
  }
  else if (path.startsWith('/deployments/')) data = config.method === 'get' ? { preview: { kind: 'compose', action: 'restart', effective_action: 'recreate', configuration_changed: true, configuration_files: [{ path: '.prod.env', version: 2 }], container_id: 'a'.repeat(64), digest: 'f'.repeat(64), service: 'api', run_to_completion: false, containers: [{ id: 'a'.repeat(64), name: 'api-1', image: 'example/api@sha256:sample' }, { id: 'b'.repeat(64), name: 'api-2', image: 'example/api@sha256:sample' }] } } : { runtime: { deployment_id: 'sample', kind: 'compose', state: 'running' } }
  else if (config.method === 'get') data = ({ '/delivery-variables': { variables: rows.map(item => ({ ...item })) }, '/projects': { projects: [{ id: project, name: '示例项目' }] }, '/applications': { applications: [{ id: 'app', name: '订单应用' }] }, '/environments': { environments: [{ id: 'test', name: '测试环境' }, { id: 'prod', name: '生产环境' }] }, '/deployment-plans': { deployment_plans: plans } })[path]
  else if (path.startsWith('/delivery-variables')) {
    if (config.method === 'post') { const row = { ...body, id: `config-${rows.length}`, project_id: project, version: 1, value: body.is_secret ? undefined : body.value }; rows.push(row); data = { variable: row } }
    else if (config.method === 'put') { const row = rows.find(item => path.endsWith(item.id)); history[row.id].push(body.value??history[row.id].at(-1)); Object.assign(row, body, { version: row.version + 1, value: body.is_secret ? undefined : body.value }); data = { variable: row } }
  }
  if (!data) throw new Error('未登记的合成接口')
  return { data, status: 200, statusText: 'OK', headers: {}, config }
}
window.fetch = () => Promise.reject(new Error('验收页不连接真实 API'))
createApp({ setup() {
  const compose = { project_name: 'example', services: [], tag_variable: 'IMAGE_TAG', use_sudo: false, adopt_existing: false, env_file: '/home/ubuntu/legacy.env' }
  const deploymentConfig = ref({ deployment_plan_id: 'plan-compose_stack', configuration_files: [{ id: 'env-config', path: '.prod.env' }], compose }), confirmOpen = ref(false), dark = ref(false)
  const componentTheme = computed(() => ({ token: { colorPrimary: '#4f6ef7', borderRadius: 6, borderRadiusLG: 8, colorBgLayout: dark.value ? '#0f1014' : '#f5f6f8', colorBgContainer: dark.value ? '#17181d' : '#ffffff', colorBorderSecondary: dark.value ? '#292b33' : '#e9eaee' }, algorithm: dark.value ? theme.darkAlgorithm : theme.defaultAlgorithm }))
  return () => h(ConfigProvider, { theme: componentTheme.value }, { default: () => h('main', { style: 'padding:20px;display:grid;gap:24px;min-width:0' }, [
    h(Button, { onClick() { dark.value = !dark.value; if(preferences.theme!==(dark.value?'dark':'light'))preferences.toggleTheme() } }, () => '切换主题'),
    h(DeliveryVariablesView, { configurationFiles: true, applicationId: 'app', applicationName: '订单应用' }),
    h('h2', '部署阶段'),
    h('div', { style: 'display:flex;gap:8px;flex-wrap:wrap' }, ['compose_stack', 'compose', 'docker'].map(kind => h(Button, { onClick() { deploymentConfig.value = { ...deploymentConfig.value, deployment_plan_id: 'plan-' + kind, compose: kind === 'compose_stack' ? compose : undefined } } }, () => ({ compose_stack: '多服务 Compose', compose: '单服务 Compose', docker: 'Docker' })[kind]))),
    h(Form, { layout: 'vertical' }, { default: () => h(ComposeTaskFields, { node: { id: 'deploy', type: 'deploy', config: deploymentConfig.value }, buildNodes: [], applicationID: 'app', applicationName: '订单应用', disabled: false, onChange: patch => { deploymentConfig.value = { ...deploymentConfig.value, ...patch } } }) }),
    h(Button, { onClick() { confirmOpen.value = true } }, () => '打开重启确认'),
    h(ComposeContainerControlModal, { open: confirmOpen.value, deploymentID: 'sample', containerID: 'a'.repeat(64), action: 'restart', 'onUpdate:open': value => { confirmOpen.value = value } }),
  ]) })
} }).use(pinia).use(i18n).mount('#app')
