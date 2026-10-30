import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {test} from 'node:test'
import {compileTemplate,parse} from '@vue/compiler-sfc'
import * as vue from 'vue'
import {componentHarness,loadFrontend} from './acceptanceHarness.mjs'

const target=(id='cluster-a')=>({id,name:`集群-${id}`,api_server:'https://192.0.2.10:6443',default_namespace:'team',is_active:true,updated_at:'2026-10-30T08:00:00Z',hosts:[{id:'host-a'}],kubeconfig:'不得回填的旧配置'})
const deferred=()=>{let resolve,reject;const promise=new Promise((yes,no)=>{resolve=yes;reject=no});return {promise,resolve,reject}}
const settle=()=>new Promise(resolve=>setImmediate(resolve))
function harness(options={}){
  const props=vue.reactive({open:true,canTest:true,canAssociate:true,hosts:[],clusters:[],cluster:target(),...options.props})
  const permissions=vue.reactive(new Set(options.permissions??['cluster.update','cluster.execute','cluster.create']))
  const auth=vue.reactive({user:{id:'operator',project_id:'project-a',department_id:'department-a',is_superuser:false},canAny:required=>required.some(code=>permissions.has(code))})
  let project='project-a'
  const lifecycle=[],listeners=new Map(),storage={getItem:key=>key==='edo.project'?project:null},window={addEventListener:(name,callback)=>listeners.set(name,callback),removeEventListener:name=>listeners.delete(name)}
  const h=componentHarness('../src/components/KubernetesClusterDrawer.vue',{
    props,api:options.api,
    modules:{vue:{...vue,onMounted:callback=>lifecycle.push(callback),onBeforeUnmount(){}},'@/stores/auth':{useAuthStore:()=>auth},'@/utils/infrastructureRelations':loadFrontend('../src/utils/infrastructureRelations.ts')},
    globals:{localStorage:storage,window},
  })
  h.state.formRef.value={validate:async()=>{}}
  lifecycle.forEach(callback=>callback())
  return {...h,props,permissions,auth,project:value=>{project=value},event:name=>listeners.get(name)?.({type:name}),dispose:()=>h.stop()}
}

test('编辑默认保留当前密文与命名空间，不读取或回填旧kubeconfig',()=>{
  const h=harness()
  assert.equal(h.state.editing.value,true)
  assert.equal(h.state.form.name,'集群-cluster-a');assert.equal(h.state.form.default_namespace,'team')
  assert.equal(h.state.form.config_action,'keep');assert.equal(h.state.form.kubeconfig,'')
  assert.equal(h.state.replacingConfig.value,false);assert.equal(h.state.canSubmit.value,true)
  assert.deepEqual([...h.state.form.host_ids],[])
  h.dispose()
})

test('保留配置的信息更新只需update，准确PUT到原UUID且不发送密文或主机关联',async()=>{
  const h=harness({permissions:['cluster.update'],props:{canTest:false},api:{put:async()=>({data:{cluster:target()}})}})
  h.state.form.name='  新名称  '
  await h.state.submit()
  assert.equal(h.requests.length,1);assert.equal(h.requests[0].method,'put');assert.equal(h.requests[0].path,'/kubernetes/clusters/cluster-a')
  assert.deepEqual(h.requests[0].body,{name:'新名称',mode:'kubeconfig',default_namespace:'team'})
  assert.equal(h.requests[0].options.headers['X-EDO-Project-ID'],'project-a')
  assert.deepEqual(h.events.map(event=>event.name),['saved','update:open'])
  h.dispose()
})

test('显式替换必须先测试当前草稿，再PUT新配置，始终不改主机关联',async()=>{
  const h=harness({api:{post:async()=>({data:{api_server:'https://192.0.2.20:6443',version:'v1.37.1'}}),put:async()=>({data:{cluster:target()}})}})
  h.state.form.config_action='replace';h.state.form.kubeconfig='新的专用配置';await vue.nextTick()
  await h.state.submit();assert.equal(h.requests.length,0)
  await h.state.testConnection();assert.equal(h.state.connectionTested.value,true)
  await h.state.submit();assert.deepEqual(h.requests.map(request=>[request.method,request.path]),[['post','/kubernetes/clusters/test'],['put','/kubernetes/clusters/cluster-a']])
  for(const request of h.requests){assert.equal(request.body.kubeconfig,'新的专用配置');assert.equal('host_ids' in request.body,false)}
  h.dispose()
})

test('缺少update或execute时不能借编辑入口替换连接，撤权使测试与敏感草稿失效',async()=>{
  for(const permissions of [['cluster.execute'],['cluster.update']]){
    const h=harness({permissions})
    h.state.form.config_action='replace';h.state.form.kubeconfig='新配置';await vue.nextTick();await h.state.testConnection();await h.state.submit()
    assert.equal(h.requests.length,0);assert.equal(h.events.length,0);h.dispose()
  }
  const pending=deferred(),h=harness({api:{post:()=>pending.promise}})
  h.state.form.config_action='replace';h.state.form.kubeconfig='新配置';await vue.nextTick()
  const testing=h.state.testConnection();await vue.nextTick();h.permissions.delete('cluster.update');await vue.nextTick()
  pending.resolve({data:{api_server:target().api_server,version:'v1.37.1'}});await testing
  assert.equal(h.state.connectionTested.value,false);assert.equal(h.state.form.kubeconfig,'');assert.equal(h.successes?.length??h.notifications.filter(item=>item.kind==='success').length,0)
  h.dispose()
})

test('切换编辑UUID或同目标快照后丢弃旧测试结果，不能用旧草稿更新新目标',async()=>{
  for(const changed of [target('cluster-b'),{...target(),updated_at:'2026-10-30T08:01:00Z'}]){
    const pending=deferred(),h=harness({api:{post:()=>pending.promise}})
    h.state.form.config_action='replace';h.state.form.kubeconfig='旧目标新配置';await vue.nextTick()
    const testing=h.state.testConnection();await settle();const signal=h.requests[0].options.signal
    h.props.cluster=changed;await vue.nextTick();pending.resolve({data:{api_server:target().api_server,version:'v1.37.1'}});await testing
    assert.equal(signal.aborted,true);assert.equal(h.state.form.kubeconfig,'');assert.equal(h.state.form.name,changed.name);assert.equal(h.state.connectionTested.value,false)
    assert.equal(h.notifications.filter(item=>item.kind==='success').length,0);h.dispose()
  }
})

test('项目未发事件也不能沿用原编辑器提交，收到项目事件立即清空凭据',async()=>{
  for(const sendEvent of [false,true]){
    const h=harness();h.state.form.config_action='replace';h.state.form.kubeconfig='仅当前项目配置';await vue.nextTick()
    h.project('project-b');if(sendEvent)h.event('edo:projects-changed')
    await h.state.submit();assert.equal(h.requests.length,0);assert.equal(h.events.length,0);assert.equal(h.state.form.kubeconfig,'');h.dispose()
  }
})

test('草稿变动使旧测试失效，旧请求结束不能解除新请求加载或写回结果',async()=>{
  const first=deferred(),second=deferred();let calls=0
  const h=harness({api:{post:()=>++calls===1?first.promise:second.promise}})
  h.state.form.config_action='replace';h.state.form.kubeconfig='配置一';await vue.nextTick()
  const old=h.state.testConnection();await settle();h.state.form.kubeconfig='配置二';await vue.nextTick()
  const current=h.state.testConnection();await settle();assert.equal(calls,2)
  first.resolve({data:{api_server:'https://192.0.2.1:6443',version:'old'}});await old
  assert.equal(h.state.testing.value,true);assert.equal(h.state.connectionTested.value,false)
  second.resolve({data:{api_server:'https://192.0.2.2:6443',version:'new'}});await current
  assert.equal(h.state.testing.value,false);assert.equal(h.state.testResult.value.version,'new');h.dispose()
})

test('更新失败保留草稿，目标或项目变更后的迟到成功不得关闭其他编辑器',async()=>{
  const pending=deferred(),h=harness({api:{put:()=>pending.promise}})
  const saving=h.state.submit();await vue.nextTick();h.props.cluster=target('cluster-b');await vue.nextTick()
  pending.resolve({data:{cluster:target()}});await saving;assert.equal(h.events.length,0);assert.equal(h.props.open,true);assert.equal(h.state.form.name,'集群-cluster-b');h.dispose()
  const failed=harness({api:{put:async()=>{throw new Error('保存失败')}}});failed.state.form.name='未保存名称';await failed.state.submit()
  assert.equal(failed.props.open,true);assert.equal(failed.state.form.name,'未保存名称');assert.equal(failed.events.length,0);failed.dispose()
})

test('异步表单验证期间切换目标不发请求，异常响应UUID不冒充保存成功',async()=>{
  for(const action of ['submit','testConnection']){
    const validation=deferred(),h=harness();h.state.formRef.value.validate=()=>validation.promise
    if(action==='testConnection'){h.state.form.config_action='replace';h.state.form.kubeconfig='新配置';await vue.nextTick()}
    const pending=h.state[action]();h.props.cluster=target('cluster-b');await vue.nextTick();validation.resolve();await pending
    assert.equal(h.requests.length,0);h.dispose()
  }
  const h=harness({api:{put:async()=>({data:{cluster:target('wrong-target')}})}})
  await h.state.submit();assert.equal(h.events.length,0);assert.equal(h.notifications.some(item=>item.kind==='success'),false);assert.match(h.state.requestError.value,/未确认/);h.dispose()
})

test('更换文件的迟到读取不能覆盖新目标，切回保留模式清空新配置',async()=>{
  const readers=[],previousReader=globalThis.FileReader
  globalThis.FileReader=class{constructor(){readers.push(this)}readAsText(){}}
  const h=harness()
  try{
    h.state.form.config_action='replace';await vue.nextTick()
    const reading=h.state.readKubeconfigFile({name:'test-cluster.yaml',size:30});assert.equal(readers.length,1)
    h.props.cluster=target('cluster-b');await vue.nextTick();readers[0].result='旧目标的文件';readers[0].onload();await reading
    assert.equal(h.state.form.kubeconfig,'');assert.equal(h.state.form.name,'集群-cluster-b')
    h.state.form.config_action='replace';h.state.form.kubeconfig='新配置';await vue.nextTick();h.state.form.config_action='keep';await vue.nextTick()
    assert.equal(h.state.form.kubeconfig,'');assert.equal('kubeconfig' in h.state.payload(),false)
  }finally{h.dispose();globalThis.FileReader=previousReader}
})

test('集群编辑动作只接受当前列表对象，保存后清除旧连接状态并刷新而不关闭详情',()=>{
  const props=vue.reactive({clusters:[target()],hosts:[],loading:false}),resourceList=loadFrontend('../src/utils/resourceList.ts',{vue,'vue-i18n':{useI18n:()=>({t:key=>key})}})
  const h=componentHarness('../src/components/ClusterInventory.vue',{props,codes:['cluster.update'],modules:{'@/utils/resourceList':resourceList,'@/utils/infrastructureRelations':loadFrontend('../src/utils/infrastructureRelations.ts')}})
  h.state.editCluster(target('missing'));assert.equal(h.state.editOpen.value,false)
  h.state.editCluster(target());assert.equal(h.state.editOpen.value,true);assert.equal(h.state.editingCluster.value.id,'cluster-a')
  h.state.detailOpen.value=true;h.state.connection['cluster-a']={ok:true};h.state.clusterSaved(target())
  assert.equal('cluster-a' in h.state.connection,false);assert.equal(h.events.at(-1).name,'refresh');assert.equal(h.state.detailOpen.value,true)
  h.auth.permissions.delete('cluster.update');h.state.editOpen.value=false;h.state.editCluster(target());assert.equal(h.state.editOpen.value,false);h.stop()
})

test('真实集群列表与详情同时提供update权限的编辑入口，复用同一编辑抽屉',()=>{
  const source=readFileSync(new URL('../src/components/ClusterInventory.vue',import.meta.url),'utf8'),{descriptor}=parse(source)
  assert.equal(compileTemplate({id:'cluster-edit-entry',source:descriptor.template.content}).errors.length,0)
  for(const context of ['more','actions'])assert.match(descriptor.template.content,new RegExp(`<template[^>]*#${context}[^>]*>[^]*?@click="editCluster\\((?:record|selected)\\)"`))
  assert.match(descriptor.template.content,/v-if="auth\.canAny\(\['cluster.update'\]\)"[^>]*@click="editCluster\(/)
  assert.match(descriptor.template.content,/<KubernetesClusterDrawer[^>]*v-model:open="editOpen"[^>]*:cluster="editingCluster"[^>]*:can-test="auth\.canAny\(\['cluster.execute'\]\)"[^>]*@saved="clusterSaved"/)
})
