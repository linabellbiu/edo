import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import ts from 'typescript'
import * as YAML from 'yaml'
import * as vue from 'vue'
import { parse, compileScript } from '@vue/compiler-sfc'

function load(path, modules = {}) {
  const source = readFileSync(new URL(path, import.meta.url), 'utf8')
  const code = path.endsWith('.vue') ? compileScript(parse(source).descriptor, { id: 'yaml-test' }).content : source
  const output = ts.transpileModule(code, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true } }).outputText
  const module = { exports: {} }
  new Function('require', 'module', 'exports', output)(name => modules[name] ?? {}, module, module.exports)
  return module.exports
}
const yaml = load('../src/utils/kubernetesYaml.ts', { yaml: YAML })
const type = { resource: 'configmaps', kind: 'ConfigMap', apiVersion: 'v1', namespaced: true, update: true }
const original = { apiVersion: 'v1', kind: 'ConfigMap', metadata: { name: 'demo', namespace: 'team', uid: 'one', resourceVersion: '12', labels: { app: 'demo', remove: 'yes' }, ownerReferences: [{ uid: 'owner' }], managedFields: [{}] }, data: { keep: 'same', remove: 'old' }, status: {} }
const source = () => yaml.kubeYamlText(yaml.editableKubeObject(type, original))
test('YAML 格式化保留注释，重复键和语法错误返回行列，不返回配置原文', () => {
  const valid = yaml.readKubeYaml('# 说明\napiVersion: v1\nkind: ConfigMap\nmetadata: {name: demo}\n')
  assert.equal(valid.issues.length, 0); assert.match(valid.formatted, /# 说明/)
  const duplicate = yaml.readKubeYaml('data:\n  password: private-value\n  password: hidden\n')
  assert.equal(duplicate.issues[0].code, 'duplicate'); assert.equal(duplicate.issues[0].line, 3)
  assert.ok(duplicate.issues[0].column > 0); assert.equal(duplicate.formatted, undefined)
  assert.doesNotMatch(JSON.stringify(duplicate), /private-value|hidden/)
  assert.ok(yaml.readKubeYaml('data: [\n').issues.length)
})
test('拒绝多文档、非对象、自定义标签、循环引用、非有限数值和过大输入', () => {
  for (const source of ['', '---\na: 1\n---\nb: 2\n', '- one\n- two', 'data: !execute private', 'a: &a {self: *a}', 'a: .inf', '? [a, b]\n: value', 'a:\n' + ' '.repeat(2*1024*1024)]) assert.ok(yaml.readKubeYaml(source).issues.length, source.slice(0, 60))
})
test('身份和只读字段不能修改；节点只允许调度、标签和污点；Secret 只能脱敏查看', () => {
  const target = {type, object: original}
  assert.equal(yaml.readKubeYaml(source(), target).issues.length, 0)
  for (const text of [source().replace('uid: one', 'uid: other'), source().replace('namespace: team', 'namespace: other'), source()+'status: {}\n', source()+'spec: {}\n']) assert.ok(yaml.readKubeYaml(text, target).issues.length)
  const node = { ...original, kind: 'Node', spec: { providerID: 'hidden', taints: [], unschedulable: false } }, nodeType = { ...type, resource: 'nodes', kind: 'Node', namespaced: false }
  assert.deepEqual(yaml.editableKubeObject(nodeType, node).spec, { unschedulable: false, taints: [] })
  const secret = {...original, kind:'Secret', data:{password:'c2VjcmV0'}, stringData:{password:'secret'}, keys:['password']}
  assert.doesNotMatch(yaml.kubeYamlText(secret,true), /c2VjcmV0|password|keys:/)
  assert.equal(yaml.readKubeYaml(source(), {type:{...type,resource:'secrets'},object:secret}).issues[0].code,'readonly')
})
test('YAML 补丁明确删除键，数组整体替换，保留新的状态版本号，不写入状态和所有者', () => {
  const desired = yaml.editableKubeObject(type, original)
  delete desired.data.remove; delete desired.metadata.labels.remove; desired.data.new = 'value'
  const patch = yaml.kubeYamlPatch(type, original, desired, {...original,metadata:{...original.metadata,resourceVersion:'14'}})
  assert.deepEqual(patch.data, {remove:null,new:'value'});assert.equal(patch.metadata.labels.remove,null);assert.equal(patch.metadata.resourceVersion,'14')
  assert.equal(patch.status,undefined);assert.equal(patch.metadata.ownerReferences,undefined);assert.equal(patch.metadata.managedFields,undefined)
  const deploymentType={...type,resource:'deployments',kind:'Deployment',apiVersion:'apps/v1'}, before={...original,spec:{template:{spec:{containers:[{name:'a',image:'old'},{name:'b',image:'keep'}]}}}}
  const after=yaml.editableKubeObject(deploymentType,before);after.spec.template.spec.containers.pop()
  assert.deepEqual(yaml.kubeYamlPatch(deploymentType,before,after,before).spec.template.spec.containers,[{name:'a',image:'old'}])
})
function drawer(client) {
  const component = load('../src/components/KubeYamlDrawer.vue', {vue:{...vue,onBeforeUnmount(){}}, 'vue-i18n':{useI18n:()=>({t:key=>key})}, 'ant-design-vue':{Modal:{confirm:()=>({destroy(){}})},message:{success(){}}}, '@/api/client':{default:client,__esModule:true}, '@/api/kubernetes':{resourceURL:()=>'/resource',withFreshKubeResource:async(_c,_t,o,_message,fn)=>fn(o),kubeErrorMessage:()=> '安全提示'}, '@/utils/kubernetesYaml':yaml}).default
  const props=vue.reactive({open:true,cluster:'test',type,object:original,allowed:true}),events=[]
  let state
  const scope=vue.effectScope();scope.run(()=>{state=component.setup(props,{expose(){},emit:(...args)=>events.push(args)})})
  return {state,props,events,stop:()=>scope.stop()}
}
test('格式化不发请求，无效 YAML 和无更新权限不发集群校验', async()=>{
  let calls=0;const {state,props,stop}=drawer({post:async()=>{calls++}})
  await vue.nextTick();state.format();await vue.nextTick();assert.equal(calls,0)
  state.source.value='invalid: [';await vue.nextTick();await state.checkCluster();assert.equal(calls,0)
  state.source.value=source();props.allowed=false;await vue.nextTick();await state.checkCluster();assert.equal(calls,0);stop()
})
test('旧校验响应不会覆盖新的输入；保存永远先 dry-run 再 PATCH',async()=>{
  let resolve;const calls=[]
  const client={post:()=>{calls.push('validate');return new Promise(done=>{resolve=done})},patch:async()=>calls.push('save')}
  const {state,events,stop}=drawer(client);await vue.nextTick()
  const checking=state.checkCluster();await Promise.resolve();state.source.value+='\n# changed';await vue.nextTick();resolve();await checking
  assert.equal(state.checkStatus.value,'');assert.equal(events.length,0);assert.deepEqual(calls,['validate'])
  const saving=state.checkCluster(true);await Promise.resolve();resolve();await saving;assert.deepEqual(calls,['validate','validate','save']);assert.deepEqual(events,[['update:open',false],['saved']]);stop()
})
