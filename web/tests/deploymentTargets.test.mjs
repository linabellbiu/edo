import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'
import * as yaml from 'yaml'
import { loadFrontend } from './acceptanceHarness.mjs'

const source = readFileSync(new URL('../src/views/DeploymentPlanView.vue', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(compileScript(parse(source).descriptor, { id: 'deployment-target-test' }).content, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true },
})
const cluster = { id: 'standalone-cluster', name: '独立验收集群', is_active: true }
const target = { id: 'target', platform: 'kubernetes', environment_id: 'env', runtime_id: cluster.id, namespace: 'acceptance', workload_name: 'api', container_name: 'api', rollout_timeout: 600 }
const plan = { id: 'plan', name: 'K8s 验收部署', kind: 'kubernetes', deployment_target_id: target.id, deployment_target: target, timeout_seconds: 600 }

function harness(t, denied = [], props = {}, query = {}) {
  const requests = [], errors = [],events=[],replacements=[]
  const route=vue.reactive({query:{...query}})
  props=vue.reactive(props)
  const permissions = (props.inline?['workflow.read','workflow.update','cluster.read','deployment.read']:['deployment_plan.read', 'deployment_plan.create', 'deployment_plan.update', 'cluster.read', 'cluster.execute', 'deployment.read']).filter(permission => !denied.includes(permission))
  const auth = vue.reactive({ loaded: true, user: { id: 'test-user', department_id: 'test-department', project_id: 'test-project', permissions }, canAny: codes => codes.some(code => auth.user.permissions.includes(code)) })
  let state
  const modules = {
    vue: { ...vue, onMounted() {}, onBeforeUnmount() {} },
    'vue-i18n': { useI18n: () => ({ t: key => key, locale: vue.ref('zh-CN') }) },
    'vue-router': { useRoute: () => route, useRouter: () => ({ resolve: () => ({ href: '/resource' }), async replace(value){replacements.push(value);route.query={...value.query}} }) },
    'ant-design-vue': { message: { error: value => errors.push(value), success() {} } },
    '@/utils/kubernetesYaml': loadFrontend('../src/utils/kubernetesYaml.ts', {yaml}),
    '@/utils/deploymentExecution': loadFrontend('../src/utils/deploymentExecution.ts'),
    '@/stores/auth': { useAuthStore: () => auth },
    '@/utils/resourceList': { useResourceList: () => ({ detailOpen: vue.ref(false), selected: vue.ref(), openDetails() {} }) },
    '@/api/resources': { apiErrorMessage: () => '请求失败' },
    '@/api/infrastructure': {
      environmentIDsOf: host => host.environment_ids || [],
      listEnvironments: async () => state.environments.value,
      listHosts: async () => state.hosts.value,
    },
    '@/api/client': { __esModule: true, default: {
      get: async path => {if(props.inline)requests.push({method:'GET',path});return { data: { deployment_plans: state.plans.value, clusters: state.clusters.value, endpoints: state.docker.value } }},
      post: async (path, body) => {
        requests.push({ method: 'POST', path, body })
        if (path !== '/deployment-plans') return { data: {} }
        const saved = { ...body, id: 'plan' }
        if (body.deployment_target) Object.assign(saved, { deployment_target_id: 'target', deployment_target: { ...body.deployment_target, id: 'target' } })
        state.plans.value = [saved]
        return { data: { deployment_plan: saved } }
      },
      put: async (path, body) => {
        requests.push({ method: 'PUT', path, body })
        const saved = { ...body, id: 'plan', deployment_target_id: 'target', deployment_target: { ...body.deployment_target, id: 'target' } }
        if (saved.deployment_target.platform === 'kubernetes') delete saved.deployment_target.host_id
        state.plans.value = [saved]
        return { data: { deployment_plan: saved } }
      },
    } },
  }
  const module = { exports: {} }
  new Function('require', 'module', 'exports', 'localStorage', 'window', outputText)(name => modules[name] || {}, module, module.exports,
    { getItem: key => key === 'edo.project' ? 'test-project' : null }, { addEventListener() {}, removeEventListener() {} })
  const scope = vue.effectScope()
  t.after(() => scope.stop())
  state = scope.run(() => module.exports.default.setup(props, { expose() {},emit:(name,value)=>events.push({name,value}) }))
  state.environments.value = [{ id: 'env', name: '测试环境', is_active: true }, { id: 'other-env', name: '另一环境', is_active: true }]
  state.clusters.value = [{ ...cluster }]
  return { state, requests, errors,props,events,permissions,auth,route,replacements }
}

const settle=async()=>{await vue.nextTick();await new Promise(resolve=>setImmediate(resolve));await vue.nextTick()}
test('修改链接在数据加载后精确打开指定方案，只消费编辑参数且不执行写入',async t=>{
 const h=harness(t,[],{}, {plan:'chosen',edit:'1',keep:'value'})
 h.state.plans.value=[plan,{...plan,id:'chosen',name:'准确方案',timeout_seconds:900}]
 await h.state.refresh();await settle()
 assert.equal(h.state.formOpen.value,true);assert.equal(h.state.editingID.value,'chosen')
 assert.equal(h.state.form.name,'准确方案');assert.equal(h.state.form.timeout_seconds,900)
 assert.deepEqual({...h.route.query},{plan:'chosen',keep:'value'})
 assert.equal(h.replacements.length,1);assert.deepEqual(h.requests,[])
 h.state.formOpen.value=false;await h.state.refresh();await settle()
 assert.equal(h.state.formOpen.value,false)
})

test('只定位链接不打开编辑；无权限、无目标和范围变化不打开其他方案',async t=>{
 for(const options of [{query:{plan:'plan'}},{query:{plan:'missing',edit:'1'}},{denied:['deployment_plan.update'],query:{plan:'plan',edit:'1'}},{denied:['deployment_plan.read'],query:{plan:'plan',edit:'1'}}]){
  const h=harness(t,options.denied||[],{},options.query)
  h.state.plans.value=[plan];await h.state.refresh();await settle()
  assert.equal(h.state.formOpen.value,false);assert.equal(h.state.editingID.value,'');assert.deepEqual(h.requests,[])
 }
 const changed=harness(t,[],{}, {plan:'plan',edit:'1'})
 changed.auth.user.project_id='other-project';changed.state.plans.value=[plan]
 await changed.state.refresh();await settle();assert.equal(changed.state.formOpen.value,false)
})

test('流水线内创建方案只提交给流水线，不读取或写入方案库，也不要求方案库创建权限',async t=>{
 const h=harness(t,[],{inline:true,inlineOpen:false,inlineKinds:['docker','compose']})
 h.state.hosts.value=[{id:'docker-host',name:'测试主机',mode:'ssh',is_active:true,environment_ids:['env'],capabilities:[{kind:'docker',status:'ready',runtime_id:'docker-runtime'}]}]
 h.state.docker.value=[{id:'docker-runtime',host_id:'docker-host',is_active:true}]
 h.props.inlineOpen=true;await vue.nextTick();await new Promise(resolve=>setImmediate(resolve));await vue.nextTick()
 assert.equal(h.state.formOpen.value,true);assert.equal(h.state.form.reusable,false)
 h.state.changeEnvironment('env');h.state.form.name='当前流水线方案'
 h.state.form.execution_config={...h.state.form.execution_config,deployment_registry_id:'saved-registry'}
 await h.state.save()
 assert.deepEqual(h.errors,[]);assert.equal(h.requests.filter(r=>r.path==='/deployment-plans').length,0)
 const saved=h.events.find(event=>event.name==='save-inline')?.value
 assert.equal(saved.name,'当前流水线方案');assert.equal(saved.deployment_target.environment_id,'env');assert.equal(saved.kind,'docker')
 assert.ok(h.events.some(event=>event.name==='update:inlineOpen'&&event.value===false))
})

test('独立集群无关联主机时，编辑显示实际名称及 K8s 方式，修改超时可保存且不发送虚构主机', async t => {
  const { state, requests, errors } = harness(t)
  state.edit(plan)
  assert.equal(state.form.kind, 'kubernetes')
  assert.equal(state.form.runtime_id, cluster.id)
  assert.equal(state.form.host_id, '')
  assert.deepEqual(state.availableKindOptions.value.map(option => option.value), ['kubernetes'])
  assert.equal(state.kubernetesRuntimeOptions.value[0].label, cluster.name)
  assert.equal(state.executionTargetError.value, '')
  assert.equal(state.validate(), '')
  state.form.timeout_seconds = 1200
  await state.save()
  const saved = requests.find(request => request.method === 'PUT')
  assert.equal(saved.path, '/deployment-plans/plan')
  assert.equal(saved.body.timeout_seconds, 1200)
  assert.equal(saved.body.deployment_target.rollout_timeout, 1200)
  assert.equal(saved.body.deployment_target.runtime_id, cluster.id)
  assert.equal(saved.body.deployment_target.host_id, '')
  assert.equal(saved.body.deployment_target.namespace, 'acceptance')
  assert.equal(state.formOpen.value, false)
  assert.deepEqual(errors, [])
})

test('创建固定 K8s 方案及切换环境不依赖环境主机，也不丢失独立集群', t => {
  const { state } = harness(t)
  state.create()
  state.form.reusable = false
  state.changeEnvironment('env')
  assert.equal(state.form.kind, 'kubernetes')
  state.changeKubernetesRuntime(cluster.id)
  Object.assign(state.form, { name: '新 K8s 方案', namespace: 'test', workload_name: 'web', container_name: 'web' })
  assert.equal(state.validate(), '')
  state.changeEnvironment('other-env')
  assert.equal(state.form.runtime_id, cluster.id)
  assert.equal(state.form.host_id, '')
  assert.equal(state.validate(), '')
})

test('K8s 连接检查直接使用所选集群；旧主机关联与多个环境主机均不产生歧义', async t => {
  const { state, requests, errors } = harness(t)
  state.hosts.value = ['first', 'second'].map(id => ({ id, is_active: true, environment_ids: ['env'], capabilities: [] }))
  state.edit({ ...plan, deployment_target: { ...target, host_id: 'old-host' } })
  assert.equal(state.form.host_id, '')
  assert.equal(state.executionTargetError.value, '')
  await state.testConnection()
  assert.deepEqual(requests, [{ method: 'POST', path: `/kubernetes/clusters/${cluster.id}/ping`, body: undefined }])
  assert.deepEqual(errors, [])
})

test('停用或不可读集群仍明确阻止保存和检查，不把 UUID 当成有效名称', async t => {
  const disabled = harness(t)
  disabled.state.clusters.value = [{ ...cluster, is_active: false }]
  disabled.state.edit(plan)
  const option = disabled.state.kubernetesRuntimeOptions.value.find(item => item.value === cluster.id)
  assert.equal(option.label, cluster.name)
  assert.equal(option.disabled, true)
  assert.equal(disabled.state.executionTargetError.value, 'deploymentTarget.clusterUnavailable')
  assert.ok(disabled.state.validate())
  await disabled.state.testConnection()
  assert.deepEqual(disabled.requests, [])
  const denied = harness(t, ['cluster.read'])
  denied.state.edit(plan)
  assert.equal(denied.state.executionTargetError.value, 'deploymentTarget.clusterPermission')
  await denied.state.testConnection()
  assert.deepEqual(denied.requests, [])
})

test('Docker、Compose 和脚本继续要求当前环境内唯一已就绪主机', t => {
  const { state } = harness(t)
  state.docker.value = [{ id: 'docker-first', is_active: true }, { id: 'docker-second', is_active: true }]
  state.hosts.value = ['first', 'second'].map(id => ({
    id, is_active: true, mode: 'ssh', environment_ids: ['env'],
    capabilities: [{ kind: 'docker', status: 'ready', runtime_id: `docker-${id}` }, { kind: 'ssh', status: 'ready' }],
  }))
  state.form.reusable = false
  state.form.environment_id = 'env'
  for (const kind of ['docker', 'compose', 'script']) {
    state.form.kind = kind
    state.reconcileExecutionTarget()
    assert.equal(state.form.host_id, '')
    assert.match(state.executionTargetError.value, /多台/)
  }
  state.hosts.value = state.hosts.value.slice(0, 1)
  state.form.kind = 'docker'
  state.reconcileExecutionTarget()
  assert.equal(state.form.host_id, 'first')
  assert.equal(state.form.runtime_id, 'docker-first')
  assert.equal(state.executionTargetError.value, '')
})

function dockerHostHarness(t) {
  const result = harness(t)
  result.state.hosts.value = [{
    id: 'docker-host', name: '测试主机', mode: 'ssh', is_active: true, environment_ids: ['env'],
    capabilities: [{ kind: 'docker', status: 'ready', runtime_id: 'docker-runtime' }, { kind: 'ssh', status: 'ready' }],
  }]
  result.state.docker.value = [{ id: 'docker-runtime', host_id: 'docker-host', is_active: true }]
  return result
}

function composePlan(workloadName = '') {
  return {
    id: 'plan', name: 'Compose 测试方案', kind: 'compose', service_name: 'app',
    compose_yaml: 'services:\n  app:\n    restart: no\n', timeout_seconds: 120,
    deployment_target_id: 'target', deployment_target: {
      id: 'target', platform: 'docker', environment_id: 'env', host_id: 'docker-host',
      runtime_id: 'docker-runtime', workload_name: workloadName, rollout_timeout: 120,
    },
  }
}

test('新建 Docker Compose 只有通用入口，原单镜像方案保留身份且显示同一名称', t => {
  const {state}=harness(t)
  assert.deepEqual(state.kindOptions.filter(option=>option.value.startsWith('compose')).map(option=>[option.value,option.label]),[['compose_stack','Docker Compose']])
  state.edit(composePlan())
  state.form.reusable=true
  const choices=state.availableKindOptions.value.filter(option=>option.value.startsWith('compose'))
  assert.deepEqual(choices.map(option=>[option.value,option.label]),[['compose','Docker Compose']])
  assert.equal(state.form.kind,'compose')
  assert.equal(state.editingID.value,'plan')
})

test('Compose 编辑仅改 YAML 保存不把服务名写入容器名，也不继承旧误填值', async t => {
  for (const previousName of ['', 'old-container']) {
    const { state, requests, errors } = dockerHostHarness(t)
    state.edit(composePlan(previousName))
    state.form.compose_yaml += '# 仅修改测试配置\n'
    state.form.execution_config={...state.form.execution_config,deployment_registry_id:'saved-registry'}
    await state.save()
    const saved = requests.find(x => x.method === 'PUT')
    assert.ok(saved)
    assert.equal(saved.body.deployment_target.workload_name, '')
    assert.equal(saved.body.service_name, 'app')
    assert.equal(saved.body.deployment_target.host_id, 'docker-host')
    assert.equal(saved.body.deployment_target.runtime_id, 'docker-runtime')
    assert.equal(saved.body.deployment_target.environment_id, 'env')
    assert.equal(saved.body.compose_yaml, composePlan().compose_yaml + '# 仅修改测试配置\n')
    assert.deepEqual(errors, [])
  }
})

test('Compose 服务改名只写独立服务字段，目标容器名称保持空', async t => {
  const { state, requests, errors } = dockerHostHarness(t)
  state.edit(composePlan())
  state.form.service_name = 'api'
  state.form.compose_yaml = 'services:\n  api:\n    restart: no\n'
  state.form.execution_config={...state.form.execution_config,deployment_registry_id:'saved-registry'}
  await state.save()
  const saved = requests.find(x => x.method === 'PUT')
  assert.equal(saved.body.service_name, 'api')
  assert.equal(saved.body.deployment_target.workload_name, '')
  assert.deepEqual(errors, [])
})

test('Compose、Docker 和 K8s 草稿分别恢复服务名及目标名，不互相污染', t => {
  const { state } = dockerHostHarness(t)
  state.edit(composePlan())
  state.form.service_name = 'api'
  state.changeKind('docker')
  assert.equal(state.form.workload_name, '')
  assert.equal(state.form.service_name, '')
  state.form.workload_name = 'docker-app'
  state.changeKind('compose')
  assert.equal(state.form.service_name, 'api')
  assert.equal(state.form.workload_name, '')
  state.changeKind('kubernetes')
  assert.equal(state.form.workload_name, '')
  state.form.workload_name = 'kube-app'
  state.changeKind('compose')
  assert.equal(state.form.service_name, 'api')
  state.changeKind('docker')
  assert.equal(state.form.workload_name, 'docker-app')
  state.changeKind('kubernetes')
  assert.equal(state.form.workload_name, 'kube-app')
})

test('Docker 显式容器名称和留空自动命名的提交语义不变', async t => {
  for (const name of ['', 'docker-app']) {
    const { state, requests, errors } = dockerHostHarness(t)
    const dockerPlan = composePlan(name)
    dockerPlan.kind = 'docker'
    dockerPlan.service_name = name
    state.edit(dockerPlan)
    state.form.service_name = '不可混入Docker的Compose服务名'
    state.form.execution_config={...state.form.execution_config,deployment_registry_id:'saved-registry'}
    await state.save()
    const saved = requests.find(x => x.method === 'PUT')
    assert.equal(saved.body.deployment_target.workload_name, name)
    assert.equal(saved.body.service_name, name)
    assert.deepEqual(errors, [])
  }
})

test('脚本提交继续清空容器名称，不混入Compose服务字段', async t => {
  const { state, requests, errors } = dockerHostHarness(t)
  const scriptPlan = composePlan('ignored-container')
  scriptPlan.kind = 'script'
  scriptPlan.script = 'printf test\n'
  scriptPlan.deployment_target.platform = 'ssh'
  state.edit(scriptPlan)
  state.form.service_name = 'ignored-service'
  await state.save()
  const saved = requests.find(x => x.method === 'PUT')
  assert.equal(saved.body.deployment_target.workload_name, '')
  assert.equal(saved.body.service_name, '')
  assert.deepEqual(errors, [])
})

test('新建可复用Compose默认服务app且不生成部署目标', async t => {
  const { state, requests, errors } = dockerHostHarness(t)
  state.create()
  state.changeKind('compose')
  state.form.name = '可复用Compose'
  state.form.execution_config={...state.form.execution_config,deployment_registry_id:'saved-registry'}
  await state.save()
  const saved = requests.find(x => x.method === 'POST')
  assert.equal(saved.body.service_name, 'app')
  assert.equal(saved.body.deployment_target, undefined)
  assert.deepEqual(errors, [])
})

test('Compose 服务输入绑定独立服务字段，不再绑定单容器名称', () => {
  assert.ok(source.includes('<a-input v-model:value="form.service_name"'))
})

test('Docker 和两种 compose 方案保存前必须有显式仓库，上游显示值不能绕过校验',async t=>{
 for(const kind of ['docker','compose','compose_stack']){
  const {state,requests,errors}=dockerHostHarness(t)
  state.create();state.changeKind(kind);state.form.name='必须保存仓库'
  state.form.execution_config={}
  assert.equal(state.validate(),'请选择并保存镜像仓库')
  await state.save();assert.deepEqual(requests,[]);assert.deepEqual(errors,['请选择并保存镜像仓库'])
  state.form.execution_config={deployment_registry_id:'chosen-registry'}
  await state.save()
  assert.equal(requests.find(item=>item.method==='POST').body.execution_config.deployment_registry_id,'chosen-registry')
 }
})
