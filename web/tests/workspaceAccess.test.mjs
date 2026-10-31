import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {test} from 'node:test'
import {parse,compileScript} from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'

function load(path,modules,globals={}){
 const source=readFileSync(new URL(path,import.meta.url),'utf8')
 const code=path.endsWith('.vue')?compileScript(parse(source).descriptor,{id:'workspace-test'}).content:source
 const output=ts.transpileModule(code,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022,esModuleInterop:true}}).outputText
 const module={exports:{}}
 new Function('require','module','exports',...Object.keys(globals),output)(key=>modules[key]||{},module,module.exports,...Object.values(globals))
 return module.exports
}
function authHarness(projects,stored='revoked-project'){
 const values=new Map([['edo.project',stored]]),calls=[]
 const storage={getItem:key=>values.get(key)||null,setItem:(key,value)=>values.set(key,value),removeItem:key=>values.delete(key)}
 const api={__esModule:true,setAuthorizationFreshness(){},default:{get:async path=>{calls.push(path);return {data:{projects}}}},getCurrentUser:async()=>{calls.push('profile:'+storage.getItem('edo.project'));return {id:'member',project_id:storage.getItem('edo.project')||'',permissions:['application.read']}},login:async()=>calls.push('login')}
 const store=load('../src/stores/auth.ts',{vue,pinia:{defineStore:(_id,factory)=>factory},axios:{__esModule:true,default:{isAxiosError:()=>false}},'@/api/client':api},{localStorage:storage}).useAuthStore()
 return {store,calls,storage}
}
test('登录先恢复有效项目，再获取该项目权限；已撤销项目不会锁死登录',async()=>{
 const {store,calls,storage}=authHarness([{id:'current-project',is_active:true},{id:'disabled',is_active:false}])
 await store.ensureLoaded()
 assert.equal(storage.getItem('edo.project'),'current-project')
 assert.deepEqual(calls,['/projects','profile:current-project'])
 assert.equal(store.canAny(['application.read']),true)
 assert.equal(store.canAny(['workflow.read']),false)
 assert.equal(store.canAny(['delivery.read']),false)
})
test('没有项目成员资格时清除旧项目，仅加载平台身份，不伪造默认项目',async()=>{
 const {store,storage,calls}=authHarness([])
 await store.ensureLoaded()
 assert.equal(storage.getItem('edo.project'),null)
 assert.deepEqual(calls,['/projects','profile:null'])
 assert.ok(store.user.value)
})
test('切换项目采用完整页面上下文切换，避免旧抽屉和旧异步请求混入新项目',()=>{
 const assignments=[],stored=new Map()
 const component=load('../src/components/ProjectSelector.vue',{vue:{...vue,onMounted(){},onBeforeUnmount(){}},'vue-i18n':{useI18n:()=>({t:key=>key})},'@/stores/auth':{useAuthStore:()=>({authorizationState:'ready'})}},{localStorage:{setItem:(key,value)=>stored.set(key,value)},window:{location:{assign:path=>assignments.push(path)}}}).default
 const state=component.setup({}, {expose(){}})
 state.selected.value='old-project';state.change('new-project')
 assert.equal(stored.get('edo.project'),'new-project')
 assert.deepEqual(assignments,['/projects'])
})
test('项目详情快速切换丢弃迟到响应，关闭后也不会重新打开',async()=>{
 const pending=[]
 const component=load('../src/views/ProjectsView.vue',{vue:{...vue,onMounted(){},onBeforeUnmount(){}},'vue-i18n':{useI18n:()=>({t:key=>key})},'@/stores/auth':{useAuthStore:()=>({user:{id:'me'},canAny:()=>false})},'@/api/client':{__esModule:true,default:{get:()=>new Promise(resolve=>pending.push(resolve))}},'ant-design-vue':{message:{error:assert.fail}}}).default
 const state=component.setup({}, {expose(){}})
 const a=state.detail('a'),b=state.detail('b')
 const detail=id=>({data:{project:{id},members:[],users:[],roles:[],permissions:[],catalog:[]}})
 pending[1](detail('b'));await b;pending[0](detail('a'));await a
 assert.equal(state.selected.value.project.id,'b')
 assert.equal(state.can('project.member.update'),false)
 const c=state.detail('c');state.closeDetail();pending[2](detail('c'));await c
 assert.equal(state.open.value,false);assert.equal(state.selected.value,null)
})
test('项目详情检查态关闭编辑层且阻止旧超级管理员授权与迟到详情恢复',async t=>{
 const pending=[],auth=vue.reactive({authorizationState:'ready',user:{id:'me',is_superuser:true},canAny:()=>true})
 const component=load('../src/views/ProjectsView.vue',{vue:{...vue,onMounted(){},onBeforeUnmount(){}},'vue-i18n':{useI18n:()=>({t:key=>key})},'@/stores/auth':{useAuthStore:()=>auth},'@/api/client':{__esModule:true,default:{get:()=>new Promise(resolve=>pending.push(resolve))}},'ant-design-vue':{message:{error:assert.fail}}}).default
 const scope=vue.effectScope();let state
 scope.run(()=>{state=component.setup({}, {expose(){}})});t.after(()=>scope.stop())
 const first=state.detail('a');pending[0]({data:{project:{id:'a'},members:[],users:[],roles:[],permissions:[],catalog:[]}});await first
 state.formOpen.value=true
 assert.equal(state.can('project.member.update'),true)
 const old=state.detail('b')
 auth.authorizationState='checking'
 assert.equal(state.open.value,false);assert.equal(state.formOpen.value,false)
 assert.equal(state.selected.value,null);assert.equal(state.can('project.member.update'),false)
 pending[1]({data:{project:{id:'b'},members:[],users:[],roles:[],permissions:['project.member.update'],catalog:[]}});await old
 assert.equal(state.selected.value,null)
 auth.authorizationState='unavailable'
 assert.equal(state.can('project.member.update'),false)
})
test('项目目录恢复时只读重取，检查前的豁免GET和换用户旧响应不能恢复旧目录',async t=>{
 const pending=[],unmount=[],auth=vue.reactive({authorizationState:'ready',user:{id:'me',permissions:[]},canAny:()=>false})
 const component=load('../src/views/ProjectsView.vue',{vue:{...vue,onMounted(){},onBeforeUnmount:callback=>unmount.push(callback)},'vue-i18n':{useI18n:()=>({t:key=>key})},'@/stores/auth':{useAuthStore:()=>auth},'@/api/client':{__esModule:true,default:{get:()=>new Promise(resolve=>pending.push(resolve))}},'ant-design-vue':{message:{error:assert.fail}}}).default
 const scope=vue.effectScope();let state
 scope.run(()=>{state=component.setup({}, {expose(){}})});t.after(()=>{unmount.forEach(callback=>callback());scope.stop()})
 const old=state.load()
 auth.authorizationState='checking';auth.authorizationState='unavailable';auth.authorizationState='ready'
 assert.equal(pending.length,2)
 pending[0]({data:{projects:[{id:'old'}]}});await old
 assert.deepEqual(state.rows.value,[])
 pending[1]({data:{projects:[{id:'fresh'}]}});await new Promise(resolve=>setImmediate(resolve))
 assert.deepEqual(state.rows.value.map(row=>row.id),['fresh'])
 const next=state.load()
 auth.user={id:'other',permissions:[]}
 assert.equal(pending.length,4)
 assert.deepEqual(state.rows.value,[])
 pending[2]({data:{projects:[{id:'previous-user'}]}});await next
 assert.deepEqual(state.rows.value,[])
 unmount.forEach(callback=>callback())
 pending[3]({data:{projects:[{id:'unmounted'}]}});await new Promise(resolve=>setImmediate(resolve))
 assert.deepEqual(state.rows.value,[])
})
test('部门上级选项排除自己及所有下级，防止界面形成环',()=>{
 const component=load('../src/views/OrganizationView.vue',{vue:{...vue,onMounted(){},onBeforeUnmount(){}},'vue-i18n':{useI18n:()=>({t:key=>key})},'@/stores/auth':{useAuthStore:()=>({canAny:()=>false})}}).default
 const state=component.setup({}, {expose(){}})
 state.rows.value=[{id:'a',name:'研发部',parent_id:''},{id:'b',name:'交付组',parent_id:'a'},{id:'c',name:'测试组',parent_id:'b'},{id:'other',name:'财务部',parent_id:''}]
 state.form.id='a'
 assert.deepEqual(state.parentOptions.value.map(item=>item.value),['','other'])
})
test('无权访问的固定标签和历史入口不显示，查询参数区分发布计划和运行',()=>{
 const items=[{path:'/',permissions:['system.read']},{path:'/projects',permissions:[]},{path:'/release-plans?view=plans',permissions:['release_plan.read']},{path:'/release-plans?view=runs',permissions:['pipeline_run.read']}]
 const visits=items.map(item=>({path:item.path,title:item.path}))
 const component=load('../src/layouts/AppLayout.vue',{
  vue:{...vue,onMounted(){},onBeforeUnmount(){},watch(){}},
  'vue-i18n':{useI18n:()=>({t:key=>key})},
  'vue-router':{useRoute:()=>({fullPath:'/projects'}),useRouter:()=>({resolve:()=>({name:'pipeline-editor',meta:{permissions:['workflow.read']}})})},
  '@/router/navigation':{flatNavigation:()=>items,navigation:[],findNavigationItem:path=>items.find(item=>item.path===path),navigationTrail:()=>[],visibleNavigationItems:items=>items},
  '@/stores/auth':{useAuthStore:()=>({canAny:codes=>codes.length===0||codes.includes('pipeline_run.read')})},
  '@/stores/preferences':{usePreferencesStore:()=>({})},
  '@/stores/tabs':{useTabsStore:()=>({tabs:visits,recent:()=>visits,canGoBack:()=>false})},
 }).default
 const state=component.setup({}, {expose(){}})
 assert.deepEqual(state.visibleTabs.value.map(item=>item.path),['/projects','/release-plans?view=runs'])
 assert.deepEqual(state.recentVisits.value.map(item=>item.path),['/projects','/release-plans?view=runs'])
 assert.equal(state.allowedHistoryPath('/pipeline-plans/editor?id=test'),false)
})
