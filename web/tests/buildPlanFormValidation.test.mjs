import assert from 'node:assert/strict'
import { test } from 'node:test'
import { componentHarness, loadFrontend } from './acceptanceHarness.mjs'
import * as vue from 'vue'

function harness(t, { editing = false, api = {}, codes = ['build_plan.read', 'build_plan.create', 'build_plan.update'] } = {}) {
  const auth = vue.reactive({ user: { id: 'operator', department_id: 'department-a', project_id: 'project-a', permissions: [...codes] }, canAny(required) { return required.some(code => this.user.permissions.includes(code)) } })
  const h = componentHarness('../src/views/DevOpsView.vue', {
    props: { section: 'build-plans' }, codes, api,
    modules: { '@/stores/auth': { useAuthStore: () => auth }, '@/utils/deploymentInstances': loadFrontend('../src/utils/deploymentInstances.ts') },
    globals: { document: { hidden: true }, localStorage: { getItem: () => 'project-a' } },
  })
  t.after(h.stop)
  if (editing) h.state.edit({ id: 'plan-a', name: '构建方案', kind: 'dockerfile' })
  else h.state.create()
  Object.assign(h.state.buildForm, { name: '构建方案', script: 'echo public', runtime_image: 'alpine:3.22' })
  return h
}
const writes = h => h.requests.filter(item => item.method !== 'get')

for (const editing of [false, true]) for (const kind of ['script', 'dockerfile']) {
  test(`${editing ? '编辑' : '新建'}切换为 ${kind} 后，隐藏方式的无效参数不得阻止保存或进入请求`, async t => {
    const h = harness(t, { editing })
    h.state.buildForm.kind = kind
    h.state.buildArgsText.value = kind === 'dockerfile' ? 'VERSION=1=public' : 'INVALID DOCKER ARG'
    h.state.buildEnvironmentText.value = kind === 'script' ? 'MODE=public' : 'INVALID SHELL ENV'
    await h.state.save()
    assert.equal(writes(h).length, 1)
    const request = writes(h)[0]
    assert.equal(request.method, editing ? 'put' : 'post')
    assert.equal(request.path, editing ? '/build-plans/plan-a' : '/build-plans')
    assert.equal(request.body.kind, kind)
    assert.deepEqual(request.body.build_args, kind === 'dockerfile' ? { VERSION: '1=public' } : {})
    assert.deepEqual(request.body.environment_variables, kind === 'script' ? { MODE: 'public' } : {})
    assert.equal(h.state.formOpen.value, false)
    assert.equal(h.state.saving.value, false)
    assert.equal(h.notifications.some(item => item.kind === 'error'), false)
  })
}

for (const [kind, field, value, expected] of [
  ['dockerfile', 'buildArgsText', 'INVALID', /构建参数第 1 行格式无效/],
  ['dockerfile', 'buildArgsText', 'X=1\nX=2', /构建参数第 2 行的 X 重复/],
  ['dockerfile', 'buildArgsText', Array.from({ length: 101 }, (_, i) => `X${i}=v`).join('\n'), /构建参数最多配置 100 项/],
  ['script', 'buildEnvironmentText', 'INVALID', /环境变量第 1 行格式无效/],
  ['script', 'buildEnvironmentText', 'X=1\nX=2', /环境变量第 2 行的 X 重复/],
  ['script', 'buildEnvironmentText', 'HOME=/tmp', /环境变量第 1 行使用了系统保留变量 HOME/],
  ['script', 'buildEnvironmentText', Array.from({ length: 101 }, (_, i) => `X${i}=v`).join('\n'), /环境变量最多配置 100 项/],
]) {
  test(`${kind} 当前可见字段仍校验 ${expected.source}，失败保留草稿且零写入`, async t => {
    const h = harness(t)
    h.state.buildForm.kind = kind
    h.state[field].value = value
    await h.state.save()
    assert.equal(writes(h).length, 0)
    assert.equal(h.state.formOpen.value, true)
    assert.equal(h.state[field].value, value)
    assert.equal(h.state.saving.value, false)
    assert.match(h.notifications.find(item => item.kind === 'error').value, expected)
  })
}

for (const kind of ['dockerfile', 'script']) test(`${kind} 校验失败后切换方式可以直接保存，无需清理旧方式草稿`, async t => {
  const h = harness(t)
  h.state.buildForm.kind = kind
  const field = kind === 'dockerfile' ? 'buildArgsText' : 'buildEnvironmentText'
  h.state[field].value = 'INVALID'
  await h.state.save()
  assert.equal(writes(h).length, 0)
  h.state.buildForm.kind = kind === 'dockerfile' ? 'script' : 'dockerfile'
  await h.state.save()
  assert.equal(writes(h).length, 1)
  assert.deepEqual(writes(h)[0].body.build_args, {})
  assert.deepEqual(writes(h)[0].body.environment_variables, {})
})

test('Shell 保存失败时保留当前脚本、有效环境变量和隐藏 Docker 草稿，允许修正后重试', async t => {
  let reject = true
  const h = harness(t, { api: { post: async () => { if (reject) throw new Error('保存失败'); return { data: {} } } } })
  h.state.buildForm.kind = 'script'
  h.state.buildArgsText.value = 'INVALID DOCKER ARG'
  h.state.buildEnvironmentText.value = 'MODE=public'
  await h.state.save()
  assert.equal(writes(h).length, 1)
  assert.equal(h.state.formOpen.value, true)
  assert.equal(h.state.buildForm.script, 'echo public')
  assert.equal(h.state.buildArgsText.value, 'INVALID DOCKER ARG')
  assert.equal(h.state.buildEnvironmentText.value, 'MODE=public')
  assert.equal(h.state.saving.value, false)
  reject = false
  await h.state.save()
  assert.equal(writes(h).length, 2)
  assert.equal(h.state.formOpen.value, false)
})


test('构建方案修改深链接打开准确对象',t=>{
 const h=harness(t,{codes:['build_plan.read','build_plan.update']})
 h.state.formOpen.value=false
 h.state.buildPlans.value=[{id:'first',name:'第一项',kind:'dockerfile'},{id:'linked',name:'原构建方案',kind:'dockerfile',dockerfile_path:'services/Dockerfile'}]
 h.route.query={plan:'linked',edit:'1'}
 h.state.consumeBuildEditRequest()
 assert.equal(h.state.editingID.value,'linked')
 assert.equal(h.state.buildForm.dockerfile_path,'services/Dockerfile')
 assert.equal(h.state.formOpen.value,true)
})


test('没有构建方案修改权限时深链接不能打开编辑',t=>{
 const h=harness(t,{codes:['build_plan.read']})
 h.state.buildPlans.value=[{id:'linked',name:'原构建方案',kind:'dockerfile'}]
 h.route.query={plan:'linked',edit:'1'}
 h.state.consumeBuildEditRequest()
 assert.equal(h.state.formOpen.value,false)
 assert.equal(writes(h).length,0)
})

test('流水线中的库构建参数阻止旧编辑回调写入，内联参数文本错误不会静默丢弃',t=>{
 for(const library of [false,true]){
  const props=vue.reactive({node:{id:'build',config:{inline_build:{id:'',kind:'dockerfile',build_args:{KEEP:'original'}}}},disabled:false,...(library?{buildPlan:{id:'shared',kind:'dockerfile',build_args:{KEEP:'library'}}}:{})})
  const h=componentHarness('../src/components/PipelineBuildFields.vue',{props,codes:[],globals:{localStorage:{getItem:()=>''}}});t.after(h.stop)
  h.state.update({dockerfile_path:'other/Dockerfile'})
  if(library){assert.equal(h.events.some(event=>event.name==='change'),false);continue}
  h.state.variableText('INVALID')
  assert.match(h.state.fieldError.value,/KEY=value/)
  assert.deepEqual(props.node.config.inline_build.build_args,{KEEP:'original'})
  h.state.variableText('VERSION=1=public')
  assert.equal(h.state.fieldError.value,'')
  const change=h.events.filter(event=>event.name==='change').at(-1)
  assert.deepEqual(change.args[0].inline_build.build_args,{VERSION:'1=public'})
 }
})


for (const editing of [false,true]) test(`单 target ${editing?'编辑':'新建'}保存、无效输入阻止提交并保留草稿`,async t=>{
 const h=harness(t,{editing})
 h.state.buildForm.kind='dockerfile'
 h.state.buildForm.target_stage='web,api'
 await h.state.save()
 assert.equal(writes(h).length,0)
 assert.equal(h.state.formOpen.value,true)
 assert.equal(h.state.buildForm.target_stage,'web,api')
 assert.match(h.notifications.at(-1).value,/只能填写一个阶段名/)
 h.state.buildForm.target_stage=' runtime '
 await h.state.save()
 assert.equal(writes(h).length,1)
 assert.equal(writes(h)[0].body.target_stage,'runtime')
})

test('方案重新打开回显 target，新建不沿用旧方案阶段，脚本保存不发送隐藏 target',async t=>{
 const h=harness(t)
 h.state.edit({id:'shared',name:'公共方案',kind:'dockerfile',target_stage:'runtime'})
 assert.equal(h.state.buildForm.target_stage,'runtime')
 h.state.create();assert.equal(h.state.buildForm.target_stage,'')
 Object.assign(h.state.buildForm,{name:'脚本构建',kind:'script',script:'echo ok',target_stage:'web,api'})
 await h.state.save()
 assert.equal(writes(h).length,1);assert.equal(writes(h)[0].body.target_stage,'')
})
