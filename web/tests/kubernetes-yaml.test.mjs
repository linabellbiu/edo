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
function drawer(client,dialogs=[]) {
  const component = load('../src/components/KubeYamlDrawer.vue', {vue:{...vue,onBeforeUnmount(){}}, 'vue-i18n':{useI18n:()=>({t:key=>key})}, 'ant-design-vue':{Modal:{confirm:options=>{dialogs.push(options);return{destroy(){}}}},message:{success(){}}}, '@/api/client':{default:client,__esModule:true}, '@/api/kubernetes':{resourceURL:()=>'/resource',withFreshKubeResource:async(_c,_t,o,_message,fn)=>fn(o),kubeErrorMessage:()=> '安全提示'}, '@/utils/kubernetesYaml':yaml}).default
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
test('YAML离开确认取消不丢输入、确认不重复提交，校验中拒绝离开',async()=>{
 const dialogs=[],{state,events,stop}=drawer({},dialogs);assert.equal(await state.confirmLeave(),true)
 state.source.value+='\n# draft';await vue.nextTick();const denied=state.confirmLeave();dialogs[0].onCancel();dialogs[0].afterClose();assert.equal(await denied,false);assert.equal(events.length,0);assert.match(state.source.value,/# draft/)
 const allowed=state.close();dialogs[1].onOk();dialogs[1].afterClose();await allowed;assert.deepEqual(events,[['update:open',false]])
 state.checking.value=true;assert.equal(await state.confirmLeave(),false);assert.equal(dialogs.length,2);stop()
})

function freshDrawer({validate}={}){
 const gets=[],validations=[],patches=[],events=[],successes=[],order=[],unmounts=[],dialogs=[]
 const client={
  get:(url,options)=>{order.push('get');return new Promise((resolve,reject)=>gets.push({url,options,resolve,reject}))},
  post:async(url,body,options)=>{order.push('validate');validations.push({url,body,options});return validate?validate(url,body,options):{data:{valid:true}}},
  patch:async(url,body,options)=>{order.push('patch');patches.push({url,body,options});return {data:{}}},
 }
 const api=load('../src/api/kubernetes.ts',{'./client':{__esModule:true,default:client},axios:{__esModule:true,default:{isAxiosError:err=>Boolean(err.response)}},'./resources':{apiErrorMessage:()=> '安全提示'}})
 const component=load('../src/components/KubeYamlDrawer.vue',{
  vue:{...vue,onBeforeUnmount:fn=>unmounts.push(fn)},'vue-i18n':{useI18n:()=>({t:key=>key})},
  'ant-design-vue':{Modal:{confirm:options=>{const dialog={options,updates:[],destroyed:false};dialogs.push(dialog);return {update:update=>{dialog.updates.push(update);Object.assign(options,update)},destroy(){dialog.destroyed=true}}}},message:{success:value=>successes.push(value)}},
  '@/api/client':{__esModule:true,default:client},'@/api/kubernetes':api,'@/utils/kubernetesYaml':yaml,
 }).default
 const props=vue.reactive({open:true,cluster:'test',type,object:JSON.parse(JSON.stringify(original)),allowed:true}),scope=vue.effectScope()
 let state,stopped=false
 scope.run(()=>{state=component.setup(props,{expose(){},emit:(...args)=>events.push(args)})})
 const stop=()=>{if(stopped)return;stopped=true;unmounts.forEach(fn=>fn());scope.stop()}
 return {state,props,gets,validations,patches,events,successes,order,dialogs,unmount:stop,stop}
}

test('YAML 保存通过真实 fresh helper 将同一取消信号传给 GET、dry-run 和 PATCH',async t=>{
 const h=freshDrawer();t.after(h.stop);await vue.nextTick()
 h.state.source.value+='\n# draft';await vue.nextTick()
 const saving=h.state.checkCluster(true);assert.equal(h.gets.length,1);assert.ok(h.gets[0].options.signal instanceof AbortSignal)
 h.gets[0].resolve({data:{object:{...original,metadata:{...original.metadata,resourceVersion:'14'}}}});await saving
 assert.deepEqual(h.order,['get','validate','patch']);assert.equal(h.validations.length,1);assert.equal(h.patches.length,1)
 assert.equal(h.gets[0].options.signal,h.validations[0].options.signal);assert.equal(h.gets[0].options.signal,h.patches[0].options.signal)
 assert.equal(h.validations[0].body.metadata.resourceVersion,'14');assert.deepEqual(h.validations[0].body,h.patches[0].body)
 assert.deepEqual(h.events,[['update:open',false],['saved']]);assert.deepEqual(h.successes,['yamlEditor.saved'])
})

test('YAML 读取新版本期间输入变化、关闭或卸载会中止 GET，迟到响应不验证、保存或污染新状态',async t=>{
 for(const save of [false,true])for(const change of ['input','close','unmount']){
  const h=freshDrawer();t.after(h.stop);await vue.nextTick()
  const pending=h.state.checkCluster(save);assert.equal(h.gets.length,1);const signal=h.gets[0].options.signal
  assert.ok(signal instanceof AbortSignal);assert.equal(signal.aborted,false)
  if(change==='input')h.state.source.value+='\n# another draft'
  else if(change==='close')h.props.open=false
  else h.unmount()
  await vue.nextTick();assert.equal(signal.aborted,true,`${save} ${change}`)
  h.state.error.value='新上下文提示';h.state.checkStatus.value='新校验状态';h.state.checking.value=true;h.state.working.value=true
  h.gets[0].resolve({data:{object:{...original,metadata:{...original.metadata,resourceVersion:'14'}}}});await pending;await vue.nextTick()
  assert.deepEqual(h.order,['get']);assert.equal(h.validations.length,0);assert.equal(h.patches.length,0);assert.deepEqual(h.events,[]);assert.deepEqual(h.successes,[])
  assert.equal(h.state.error.value,'新上下文提示');assert.equal(h.state.checkStatus.value,'新校验状态');assert.equal(h.state.checking.value,true);assert.equal(h.state.working.value,true)
 }
})

const settle=async()=>{await vue.nextTick();await new Promise(resolve=>setImmediate(resolve));await vue.nextTick()}
const freshObject=()=>({data:{object:{...original,metadata:{...original.metadata,resourceVersion:'14'}}}})

test('YAML 保存校验期间锁定确认窗关闭入口，强行取消或关闭也中止本次保存',async t=>{
 for(const action of ['onCancel','afterClose']){
  let finish
  const h=freshDrawer({validate:()=>new Promise(resolve=>{finish=resolve})});t.after(h.stop)
  h.state.source.value+='\n# draft';await settle();h.state.save();const dialog=h.dialogs[0]
  assert.equal(dialog.options.cancelButtonProps.disabled,false);assert.equal(dialog.options.keyboard,true);assert.equal(dialog.options.closable,true)
  const saving=dialog.options.onOk()
  assert.equal(dialog.options.cancelButtonProps.disabled,true);assert.equal(dialog.options.keyboard,false);assert.equal(dialog.options.maskClosable,false);assert.equal(dialog.options.closable,false)
  h.gets[0].resolve(freshObject());await settle();assert.equal(h.validations.length,1)
  const signal=h.validations[0].options.signal;dialog.options[action]();if(action==='onCancel')dialog.options.afterClose()
  assert.equal(signal.aborted,true);assert.equal(h.state.working.value,false);assert.equal(h.state.confirming.value,false)
  h.state.error.value='新上下文提示';h.state.checking.value=true;h.state.working.value=true
  finish({data:{valid:true}});await saving;await settle()
  assert.equal(h.patches.length,0);assert.deepEqual(h.events,[]);assert.deepEqual(h.successes,[]);assert.equal(h.state.error.value,'新上下文提示');assert.equal(h.state.checking.value,true);assert.equal(h.state.working.value,true)
  await dialog.options.onOk();assert.equal(h.gets.length,1);assert.equal(h.validations.length,1)
 }
})

test('YAML 同一确认窗校验失败恢复取消入口并可重试，保存中重复确认不双写',async t=>{
 let attempts=0,finish
 const h=freshDrawer({validate:()=>{if(++attempts===1)throw new Error('private validation failure');return new Promise(resolve=>{finish=resolve})}});t.after(h.stop)
 h.state.source.value+='\n# draft';await settle();const draft=h.state.source.value;h.state.save();const dialog=h.dialogs[0]
 const first=dialog.options.onOk();h.gets[0].resolve(freshObject());await assert.rejects(first)
 assert.equal(dialog.options.cancelButtonProps.disabled,false);assert.equal(dialog.options.keyboard,true);assert.equal(dialog.options.maskClosable,false);assert.equal(dialog.options.closable,true)
 assert.equal(h.state.working.value,false);assert.equal(h.state.confirming.value,true);assert.equal(h.state.source.value,draft);assert.equal(h.patches.length,0)
 const retry=dialog.options.onOk();h.gets[1].resolve(freshObject());await settle();assert.equal(h.validations.length,2);assert.equal(dialog.options.cancelButtonProps.disabled,true)
 await dialog.options.onOk();assert.equal(h.gets.length,2);assert.equal(h.validations.length,2);assert.equal(h.patches.length,0)
 finish({data:{valid:true}});await retry
 assert.deepEqual(h.order,['get','validate','get','validate','patch']);assert.equal(h.patches.length,1);assert.deepEqual(h.events,[['update:open',false],['saved']]);assert.deepEqual(h.successes,['yamlEditor.saved'])
})

test('YAML 保存校验失败后可以正常取消，已取消确认窗不能再次发起请求',async t=>{
 const h=freshDrawer({validate:async()=>{throw new Error('private validation failure')}});t.after(h.stop)
 h.state.source.value+='\n# draft';await settle();const draft=h.state.source.value;h.state.save();const dialog=h.dialogs[0]
 const saving=dialog.options.onOk();h.gets[0].resolve(freshObject());await assert.rejects(saving)
 dialog.options.onCancel();dialog.options.afterClose();assert.equal(h.state.confirming.value,false);assert.equal(h.state.source.value,draft)
 await dialog.options.onOk();assert.equal(h.gets.length,1);assert.equal(h.validations.length,1);assert.equal(h.patches.length,0);assert.deepEqual(h.events,[]);assert.deepEqual(h.successes,[])
})

test('YAML 旧确认窗关闭和迟到校验不会取消或重置新确认窗请求',async t=>{
 let finishOld,attempts=0
 const h=freshDrawer({validate:()=>++attempts===1?new Promise(resolve=>{finishOld=resolve}):{data:{valid:true}}});t.after(h.stop)
 h.state.source.value+='\n# old draft';await settle();h.state.save();const old=h.dialogs[0],oldSaving=old.options.onOk()
 h.gets[0].resolve(freshObject());await settle();assert.equal(h.validations.length,1)
 h.props.cluster='new-cluster';await settle();assert.equal(h.validations[0].options.signal.aborted,true)
 h.state.source.value+='\n# new draft';await settle();const currentDraft=h.state.source.value;h.state.save();const current=h.dialogs[1],currentSaving=current.options.onOk()
 assert.equal(h.gets.length,2);const currentSignal=h.gets[1].options.signal;h.state.error.value='新上下文提示'
 old.options.afterClose();await old.options.onOk();assert.equal(currentSignal.aborted,false);assert.equal(h.state.confirming.value,true);assert.equal(h.state.working.value,true);assert.equal(h.state.error.value,'新上下文提示')
 finishOld({data:{valid:true}});await oldSaving;assert.equal(h.patches.length,0);assert.equal(currentSignal.aborted,false);assert.equal(h.state.working.value,true);assert.equal(h.state.source.value,currentDraft);assert.deepEqual(h.events,[])
 h.gets[1].resolve(freshObject());await currentSaving;assert.equal(h.patches.length,1);assert.deepEqual(h.events,[['update:open',false],['saved']]);assert.deepEqual(h.successes,['yamlEditor.saved'])
})
