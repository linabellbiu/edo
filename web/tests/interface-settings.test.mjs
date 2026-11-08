import assert from 'node:assert/strict'
import {test} from 'node:test'
import * as vue from 'vue'
import {loadFrontend} from './acceptanceHarness.mjs'

const deferred=()=>{let resolve,reject;const promise=new Promise((yes,no)=>{resolve=yes;reject=no});return {promise,resolve,reject}}
const settle=async()=>{await vue.nextTick();await new Promise(resolve=>setImmediate(resolve));await vue.nextTick()}
function storeHarness(get=async()=>({data:{enabled:true,version:0}})){
 const reads=[]
 const module=loadFrontend('../src/stores/interfaceSettings.ts',{vue,pinia:{defineStore:(_id,setup)=>setup},'@/api/client':{__esModule:true,default:{get:(path,options)=>{reads.push({path,options});return get()}}},'@/api/resources':{apiErrorMessage:()=> '读取界面设置失败'}})
 return {store:vue.reactive(module.useInterfaceSettingsStore()),reads}
}

test('GitHub 标识读取统一设置后显示，保存关闭立即隐藏且旧读取不能恢复',async()=>{
 const waiting=deferred(),h=storeHarness(()=>waiting.promise)
 assert.equal(h.store.showGitHubLogo,false)
 const first=h.store.load(),second=h.store.load()
 assert.equal(first,second);assert.equal(h.reads.length,1)
 h.store.apply({enabled:false,version:1})
 waiting.resolve({data:{enabled:true,version:0}});await first
 assert.equal(h.store.showGitHubLogo,false);assert.equal(h.store.githubLogo.version,1)
 assert.equal(h.reads[0].options.signal.aborted,true)
 h.store.apply({enabled:true,version:2});assert.equal(h.store.showGitHubLogo,true)
 h.store.reset();assert.equal(h.store.showGitHubLogo,false);assert.equal(h.store.githubLogo,null)
})
test('全站显示状态读取失败保留已确认值，初始失败不闪现图标',async()=>{
 const h=storeHarness(async()=>{throw Error('failed')})
 await h.store.load();assert.equal(h.store.showGitHubLogo,false);assert.match(h.store.error,/读取/)
 h.store.apply({enabled:false,version:3});await h.store.load();assert.equal(h.store.githubLogo.version,3);assert.equal(h.store.showGitHubLogo,false)
})

function editorHarness(t,permissions=['config.read','config.update'],put=async()=>({data:{enabled:false,version:1}})){
 const auth=vue.reactive({authorizationState:'ready',user:{id:'admin',permissions},canAny(codes){return codes.some(code=>this.user?.permissions.includes(code))}})
 const h=storeHarness(),writes=[],unmounts=[]
 const component=loadFrontend('../src/components/InterfaceSettings.vue',{vue:{...vue,onBeforeUnmount:fn=>unmounts.push(fn)},'@/stores/auth':{useAuthStore:()=>auth},'@/stores/interfaceSettings':{useInterfaceSettingsStore:()=>h.store},'@/api/client':{__esModule:true,default:{put:(path,body,options)=>{writes.push({path,body,options});return put()}}},'@/api/resources':{apiErrorMessage:()=> '保存失败'}}).default
 const scope=vue.effectScope(),state=scope.run(()=>component.setup({}, {expose(){}}))
 t.after(()=>{unmounts.forEach(fn=>fn());scope.stop()})
 return {...h,auth,state,writes}
}
test('界面设置使用保存时的版本，保存成功后立即更新顶部状态',async t=>{
 const h=editorHarness(t);await settle()
 assert.equal(h.state.enabled.value,true);assert.equal(h.state.loaded.value,true)
 h.state.enabled.value=false;await h.state.save()
 assert.deepEqual(h.writes[0].body,{enabled:false,expected_version:0})
 assert.equal(h.writes[0].path,'/settings/github-logo');assert.equal(h.store.showGitHubLogo,false)
 assert.match(h.state.status.value,/已保存/)
})
test('只读、撤权及保存失败均不能显示保存成功或应用未确认值',async t=>{
 const readonly=editorHarness(t,['config.read']);await settle();await readonly.state.save();assert.equal(readonly.writes.length,0)
 const failure=editorHarness(t,undefined,async()=>{throw Error('failed')});await settle()
 failure.state.enabled.value=false;await failure.state.save();assert.equal(failure.store.showGitHubLogo,true);assert.equal(failure.state.status.value,'');assert.match(failure.state.error.value,/保存失败/)
 const waiting=deferred(),changed=editorHarness(t,undefined,()=>waiting.promise);await settle()
 changed.state.enabled.value=false;const save=changed.state.save();changed.auth.user.permissions=[];await settle()
 waiting.resolve({data:{enabled:false,version:1}});await save
 assert.equal(changed.writes[0].options.signal.aborted,true);assert.equal(changed.state.status.value,'');assert.equal(changed.store.showGitHubLogo,true)
})
