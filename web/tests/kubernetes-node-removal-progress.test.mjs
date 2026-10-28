import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {test} from 'node:test'
import * as vue from 'vue'
import {compileScript,parse} from '@vue/compiler-sfc'
import {renderToString} from '@vue/server-renderer'
import ts from 'typescript'
import {componentHarness,loadFrontend} from './acceptanceHarness.mjs'

const sdk=loadFrontend('../src/api/kubernetes.ts')
const workspace=loadFrontend('../src/utils/kubernetesWorkspace.ts')
const presentation=loadFrontend('../src/utils/kubernetesPresentation.ts',{'@/api/kubernetes':sdk,'@/utils/kubernetesRoles':loadFrontend('../src/utils/kubernetesRoles.ts')})
const settle=async()=>{await vue.nextTick();await new Promise(resolve=>setImmediate(resolve));await vue.nextTick()}
const deferred=()=>{let resolve,reject;const promise=new Promise((yes,no)=>{resolve=yes;reject=no});return {promise,resolve,reject}}
const object=()=>({apiVersion:'v1',kind:'Node',metadata:{name:'nas-test',uid:'node-uid',resourceVersion:'12'}})
async function harness(t){
  const pending=deferred(),writes=[],fresh=[],denied=vue.reactive(new Set())
  const auth=vue.reactive({user:{id:'operator'},canAny:codes=>codes.some(code=>!denied.has(code))})
  const api={...sdk,listKubeResourceScopes:async()=>({items:[],cursor:{}}),withFreshKubeResource:async(cluster,type,target,_message,mutate)=>{fresh.push({cluster,type,target});return mutate({...target,metadata:{...target.metadata,resourceVersion:'13'}})},kubeErrorMessage:()=> '节点记录移除失败，请稍后重试'}
  const h=componentHarness('../src/views/KubernetesView.vue',{
    query:{view:'nodes',cluster:'test'},api:{get:async()=>({data:{counts:{},nodes:[],usage:{},phases:{}}}),delete:async(path,body)=>{writes.push({path,body});return pending.promise}},
    modules:{'@/api/kubernetes':api,'@/utils/kubernetesWorkspace':workspace,'@/utils/kubernetesPresentation':presentation,'@/stores/auth':{useAuthStore:()=>auth}},
    globals:{document:{hidden:true}},
  })
  h.state.clusters.value=[{id:'test',name:'NAS 验收集群',default_namespace:'default',is_active:true}];h.state.cluster.value='test';await settle()
  t.after(()=>{h.state.cancelListPersist();h.stop()})
  return {...h,pending,writes,fresh,denied,prepare(){h.state.showRemoveNode(object());h.state.agentStopped.value=true;h.state.nodeName.value='nas-test'}}
}

test('移除Node记录同步显示提交中，成功保留准确结果而不冒充卸载K3s',async t=>{
  const h=await harness(t);h.prepare();const run=h.state.removeNode()
  assert.equal(h.state.nodeRemovalResult.value.status,'submitting');assert.match(h.state.nodeRemovalResult.value.message,/正在移除节点记录/);assert.ok(h.state.busy.value)
  await settle();assert.equal(h.writes.length,1);assert.equal(h.writes[0].path,'/kubernetes/clusters/test/nodes/nas-test');assert.equal(h.writes[0].body.data.uid,'node-uid');assert.equal(h.writes[0].body.data.resource_version,'13')
  h.pending.resolve({data:{}});await run
  assert.equal(h.state.nodeRemoval.value.object.metadata.name,'nas-test');assert.equal(h.state.nodeRemovalResult.value.status,'succeeded');assert.match(h.state.nodeRemovalResult.value.message,/未卸载主机上的 K3s/);assert.equal(h.state.busy.value,'')
  await h.state.removeNode();assert.equal(h.state.nodeRemoval.value,undefined);assert.equal(h.writes.length,1)
})

test('移除失败保留确认框和安全失败文案，不把原始内部错误写到页面',async t=>{
  const h=await harness(t);h.prepare();const run=h.state.removeNode();await settle();h.pending.reject(new Error('private token=do-not-render @ 192.0.2.99'));await run
  assert.equal(h.state.nodeRemovalResult.value.status,'failed');assert.equal(h.state.nodeRemovalResult.value.message,'节点记录移除失败，请稍后重试');assert.ok(h.state.nodeRemoval.value);assert.equal(h.state.agentStopped.value,true);assert.equal(h.state.nodeName.value,'nas-test');assert.equal(h.state.busy.value,'')
  assert.doesNotMatch(JSON.stringify(h.notifications),/private|do-not-render|192\.0\.2/)
})

test('移除Node记录缺少权限或确认条件不提交，双击也只有一个请求',async t=>{
  const h=await harness(t);h.denied.add('kubernetes.node.delete');h.state.showRemoveNode(object());assert.equal(h.state.nodeRemoval.value,undefined)
  h.denied.clear();h.state.showRemoveNode(object());await h.state.removeNode();assert.equal(h.writes.length,0)
  h.state.agentStopped.value=true;h.state.nodeName.value='wrong';await h.state.removeNode();assert.equal(h.writes.length,0)
  h.state.nodeName.value='nas-test';const run=h.state.removeNode();await h.state.removeNode();await settle();assert.equal(h.writes.length,1)
  h.denied.add('kubernetes.node.delete');await settle();assert.equal(h.state.nodeRemoval.value,undefined);assert.equal(h.state.nodeRemovalResult.value,undefined)
  h.pending.resolve({data:{}});await run;assert.equal(h.state.nodeRemovalResult.value,undefined)
})

const source=readFileSync(new URL('../src/views/KubernetesView.vue',import.meta.url),'utf8')
const nodes=[]
function find(node){if(node.type===1&&node.tag==='a-modal'&&node.props.some(p=>p.type===7&&p.name==='on'&&p.arg?.content==='ok'&&p.exp?.content==='removeNode'))nodes.push(node);for(const child of node.children??[])find(child)}
find(parse(source).descriptor.template.ast)
assert.equal(nodes.length,1)
const fragment=`<script setup lang="ts">
import {toRefs} from 'vue'
const props=defineProps<{nodeRemoval:any;nodeRemovalResult:any;busy:string;nodeName:string;agentStopped:boolean;allowed:boolean}>()
const {nodeRemoval,nodeRemovalResult,busy,nodeName,agentStopped}=toRefs(props)
const clusterInfo={name:'NAS 验收集群'},t=(key:string)=>key,can=()=>props.allowed,removeNode=()=>{}
</script><template>${nodes[0].loc.source}</template>`
const output=ts.transpileModule(compileScript(parse(fragment).descriptor,{id:'node-removal-render',inlineTemplate:true}).content,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022}}).outputText,module={exports:{}}
new Function('require','module','exports',output)(name=>name==='vue'?vue:{},module,module.exports)

test('真实Node确认Modal片段将提交/失败/成功结果渲染在框内，成功不再保留危险表单',async()=>{
  for(const status of ['submitting','failed','succeeded']){
    const app=vue.createSSRApp(module.exports.default,{nodeRemoval:{cluster:'test',object:object()},nodeRemovalResult:{status,message:`结果-${status}`},busy:status==='submitting'?'node':'',nodeName:'nas-test',agentStopped:true,allowed:true})
    for(const name of ['a-modal','a-alert','a-checkbox','a-form-item','a-input'])app.component(name,{inheritAttrs:false,setup:(_p,{attrs,slots})=>()=>vue.h(name==='a-input'?'input':'div',attrs,[String(attrs.message??''),slots.default?.()])})
    const html=await renderToString(app);assert.match(html,new RegExp(`结果-${status}`));assert.match(html,/NAS 验收集群 \/ nas-test/)
    if(status==='succeeded'){assert.match(html,/ok-text="关闭"/);assert.doesNotMatch(html,/node-removal-name/)}else assert.match(html,/node-removal-name/)
  }
})
