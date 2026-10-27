import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { parse, compileScript } from '@vue/compiler-sfc'
import * as vue from 'vue'
import ts from 'typescript'
import { renderToString } from '@vue/server-renderer'
import { loadFrontend } from './acceptanceHarness.mjs'

const api=loadFrontend('../src/api/kubernetes.ts'),workspace=loadFrontend('../src/utils/kubernetesWorkspace.ts')
const helpers=loadFrontend('../src/utils/kubernetesWorkloadExplorer.ts'),time=loadFrontend('../src/utils/time.ts')
const diagnostics=loadFrontend('../src/utils/kubernetesDiagnostics.ts',{'@/api/kubernetes':api})
const flush=async()=>{await vue.nextTick();await Promise.resolve();await vue.nextTick()}
const source=name=>readFileSync(new URL(`../src/components/${name}.vue`,import.meta.url),'utf8')
const pod=()=>({kind:'Pod',metadata:{uid:'pod-a',name:'long-pod-name',namespace:'team'},spec:{containers:[{name:'app'}],initContainers:[{name:'init'}]},status:{phase:'Running',containerStatuses:[{name:'app',state:{running:{}}}],initContainerStatuses:[{name:'init',state:{terminated:{exitCode:0}}}]}})
function runtime(){
 const requests=[],events=[],permissions=vue.reactive(new Set(['kubernetes.workload.read','terminal.open']))
 const props=vue.reactive({cluster:'cluster-a',pod:pod(),container:'app',autoRefresh:false,refreshToken:0,disabled:false,can:code=>permissions.has(code),canFollow:()=>true})
 const component=loadFrontend('../src/components/KubeWorkloadPodRuntime.vue',{vue:{...vue,onMounted(){},onBeforeUnmount(){}},'vue-i18n':{useI18n:()=>({t:key=>key})},'@/api/kubernetes':{...api,kubeErrorMessage:error=>error.message},'@/api/client':{__esModule:true,default:{get:(url,options)=>new Promise((resolve,reject)=>requests.push({url,options,resolve,reject}))}},'@/utils/kubernetesPresentation':{resourceStatus:()=>({tone:'default',text:'未知'})},'@/utils/kubernetesDiagnostics':diagnostics,'@/utils/kubernetesWorkspace':workspace,'@/utils/kubernetesWorkloadExplorer':helpers,'@/utils/time':time}).default
 const scope=vue.effectScope();let state;scope.run(()=>{state=component.setup(props,{expose(){},emit:(name,...args)=>events.push({name,args})})})
 return {state,props,requests,events,permissions,async respond(index,items=[]){requests[index].resolve({data:{items}});await flush()},stop:()=>scope.stop()}
}

test('唯一日志与终端操作栏位于事件和图表之前，精确选择容器且保留权限与禁用守卫',async()=>{
 const template=parse(source('KubeWorkloadPodRuntime')).descriptor.template.content
 assert.ok(template.indexOf('class="runtime-actions"')<template.indexOf('class="runtime-events"'))
 assert.ok(template.indexOf('class="runtime-actions"')<template.indexOf('<KubeResourceDiagnostics'))
 assert.equal((template.match(/class="container-action-bar"/g)??[]).length,1)
 assert.match(template,/:disabled="!allowedContainer\(container\)"/)
 assert.match(template,/v-if="can\('terminal.open'\)" :disabled="!allowedTerminal\(container\)"/)
 assert.match(template,/@update:value="chooseContainer\(String\(\$event\)\)"/)
 assert.match(template,/<KubeSelect :value="container" :options="containerOptions" :disabled="disabled \|\| !readable \|\| !containers.length" aria-label="日志与终端目标容器" @update:value="chooseContainer\(String\(\$event\)\)"/)
 const h=runtime();await h.respond(0)
 h.state.openLogs('app');assert.deepEqual(h.events.at(-1),{name:'logs',args:[{cluster:'cluster-a',namespace:'team',podUID:'pod-a',container:'app'}]})
 h.state.openLogs('init',true);assert.equal(h.events.filter(event=>event.name==='terminal').length,0)
 h.props.disabled=true;const count=h.events.length;h.state.openLogs('app');h.state.chooseContainer('init');assert.equal(h.events.length,count)
 h.props.disabled=false;h.permissions.delete('kubernetes.workload.read');h.state.openLogs('app');assert.equal(h.events.length,count)
 h.stop()
})

test('原生KubeSelect精确转发候选原类型，拒绝未知选项、禁用项及迟到的禁用选择事件',()=>{
 const events=[],props=vue.reactive({value:10,options:[{value:10,label:'10'},{value:20,label:'20'},{value:50,label:'50',disabled:true}],disabled:false})
 const component=loadFrontend('../src/components/KubeSelect.vue',{vue}).default
 const state=component.setup(props,{expose(){},emit:(name,...args)=>events.push({name,args})})
 const choose=value=>state.choose({target:{value}})
 choose('20');assert.deepEqual(events,[{name:'update:value',args:[20]}]);assert.equal(typeof events[0].args[0],'number')
 choose('50');choose('999');assert.equal(events.length,1)
 props.disabled=true;choose('10');assert.equal(events.length,1)
 props.disabled=false;props.options=[{value:'app',label:'app · 容器'},{value:'init',label:'init · Init 容器'},{value:'10',label:'字符串编号'},{value:'',label:'全部状态'}]
 choose('init');assert.deepEqual(events.at(-1),{name:'update:value',args:['init']})
 choose('10');assert.equal(events.at(-1).args[0],'10');assert.equal(typeof events.at(-1).args[0],'string')
 choose('');assert.deepEqual(events.at(-1),{name:'update:value',args:['']})
 const template=parse(source('KubeSelect')).descriptor.template.content
 assert.match(template,/<select v-bind="\$attrs" :value="value" :disabled="disabled" @change="choose"/)
 assert.match(template,/:selected="option.value===value" :disabled="option.disabled"/)
 assert.doesNotMatch(source('KubeSelect'),/MutationObserver|aria-owns|aria-activedescendant|querySelector/)
})

test('原生KubeSelect将name、id与ARIA落在实际select，SSR严格选中原值且禁用候选清晰',async()=>{
 const code=compileScript(parse(source('KubeSelect')).descriptor,{id:'select-ux',inlineTemplate:true}).content
 const output=ts.transpileModule(code,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022,esModuleInterop:true}}).outputText,module={exports:{}}
 new Function('require','module','exports',output)(name=>name==='vue'?vue:name==='lucide-vue-next'?{ChevronDown:{setup:(_props,{attrs})=>()=>vue.h('svg',attrs)}}:{},module,module.exports)
 const options=[{value:10,label:'10 条'},{value:20,label:'20 条'},{value:50,label:'50 条',disabled:true}]
 const render=props=>renderToString(vue.createSSRApp(module.exports.default,{value:20,options,name:'limit',id:'page-limit','aria-label':'每页条数','aria-describedby':'page-help',...props}))
 const html=await render()
 assert.match(html,/<select name="limit" id="page-limit" aria-label="每页条数" aria-describedby="page-help"[^>]*>/)
 assert.match(html,/<option value="10">10 条<\/option>/)
 assert.match(html,/<option value="20" selected>20 条<\/option>/)
 assert.match(html,/<option value="50" disabled>50 条<\/option>/)
 assert.equal((html.match(/ selected/g)??[]).length,1)
 assert.match(html,/<svg[^>]*aria-hidden="true"/)
 assert.doesNotMatch(html,/aria-owns|aria-activedescendant|role="combobox"/)
 const disabled=await render({disabled:true});assert.match(disabled,/<select[^>]* disabled>/)
 const missing=await render({value:'missing'});assert.doesNotMatch(missing,/<option[^>]* selected/)
})

test('Warning事件首次自动展开且折叠后不被刷新抢回，换Pod清理意图；失败始终有可见重试',async()=>{
 const h=runtime(),warning={metadata:{uid:'warning-a'},involvedObject:{uid:'pod-a'},type:'Warning',reason:'Failed',message:'真实失败'}
 await h.respond(0,[warning]);assert.equal(h.state.warningCount.value,1);assert.equal(h.state.eventsExpanded.value,true)
 h.state.toggleEvents();assert.equal(h.state.eventsExpanded.value,false)
 h.state.retryEvents();await h.respond(1,[warning,{...warning,metadata:{uid:'warning-b'}}]);assert.equal(h.state.warningCount.value,2);assert.equal(h.state.eventsExpanded.value,false)
 h.props.pod={...pod(),metadata:{...pod().metadata,uid:'pod-b'}};await flush();assert.equal(h.state.eventsExpanded.value,false)
 h.requests[2].reject(new Error('事件读取失败'));await flush();assert.equal(h.state.eventsExpanded.value,true);assert.equal(h.state.eventsError.value,'事件读取失败')
 const count=h.requests.length;h.props.disabled=true;h.state.retryEvents();assert.equal(h.requests.length,count)
 h.props.disabled=false;h.permissions.delete('kubernetes.workload.read');h.state.retryEvents();assert.equal(h.requests.length,count)
 const template=parse(source('KubeWorkloadPodRuntime')).descriptor.template.content
 assert.match(template,/v-if="warningCount" class="event-warning-count"/)
 assert.match(template,/v-if="eventsError"[^>]*message="Pod 事件读取失败"/)
 assert.match(template,/@click="retryEvents"/)
 h.stop()
})

test('紧凑事件可用原生details读取完整原因、绝对时间及消息，不依赖title悬停',async()=>{
 const descriptor=parse(source('KubeWorkloadEvents')).descriptor
 const code=compileScript(descriptor,{id:'events-ux',inlineTemplate:true}).content
 const output=ts.transpileModule(code,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022,esModuleInterop:true}}).outputText,module={exports:{}}
 new Function('require','module','exports',output)(name=>name==='vue'?vue:name==='@/utils/time'?time:{},module,module.exports)
 const message='完整挂载失败原因'.repeat(60),app=vue.createSSRApp(module.exports.default,{compact:true,events:[{metadata:{uid:'event'},reason:'FailedMount',message,eventTime:'2026-10-28T01:00:00Z'}]})
 app.component('a-spin',{setup:(_props,{slots})=>()=>vue.h('div',slots.default?.())})
 app.component('a-alert',{setup:(_props,{slots})=>()=>vue.h('div',slots.default?.())})
 const html=await renderToString(app)
 assert.match(html,/<details class="event-details"><summary>/);assert.match(html,/>详情<\/span>/)
 assert.ok(html.includes(`<dd>${message}</dd>`));assert.match(html,/<dt>原因<\/dt><dd>FailedMount<\/dd>/)
 assert.ok(html.includes(`<dt>时间</dt><dd>${time.formatDateTime('2026-10-28T01:00:00Z')}</dd>`))
 assert.doesNotMatch(html,/v-html|onerror=/)
 assert.match(source('KubeWorkloadEvents'),/\.event-details\[open\]>summary::before/)
})

test('版本与Pod列表有listitem，长名称自然换行；列内恢复不滚外层、不改失效选择、不抢焦点',async()=>{
 const file=source('KubeWorkloadExplorer'),template=parse(file).descriptor.template.content
 assert.match(template,/class="revision-entry" role="listitem"/)
 assert.match(template,/:key="pod.metadata.uid" role="listitem"><button/)
 assert.match(file,/white-space: normal; overflow-wrap: anywhere/)
 assert.match(file,/scroll-padding-top: 48px/)
 const scope=vue.effectScope(),props=vue.reactive({cluster:'a',object:{kind:'StatefulSet',metadata:{uid:'workload',namespace:'team'}},type:api.kubeTypes.find(item=>item.kind==='StatefulSet'),pods:[],podsLoading:false,podsError:'',podsPartial:false,autoRefresh:false,refreshToken:0,can:()=>true,canFollow:()=>true,selection:{revisionUID:'missing',podUID:'missing'}})
 const component=loadFrontend('../src/components/KubeWorkloadExplorer.vue',{vue:{...vue,onMounted(){},onBeforeUnmount(){}},'vue-i18n':{useI18n:()=>({t:key=>key})},'@/api/kubernetes':api,'@/utils/kubernetesWorkspace':workspace,'@/utils/kubernetesWorkloadExplorer':helpers}).default
 let state;scope.run(()=>{state=component.setup(props,{expose(){},emit(){}})})
 const selected={getBoundingClientRect:()=>({top:0,bottom:30})},column={scrollTop:100,getBoundingClientRect:()=>({top:0,bottom:200}),querySelector:selector=>selector==='header'?{getBoundingClientRect:()=>({height:36})}:selected}
 state.revealInColumn(column,selected);assert.equal(column.scrollTop,58)
 state.root.value={querySelector:()=>undefined};await state.restoreVisibleSelection();assert.equal(state.podUID.value,'missing');assert.equal(state.revisionUID.value,'missing')
 props.visible=false;state.root.value={querySelector:selector=>selector==='.pod-column'?column:undefined};await state.restoreVisibleSelection();assert.equal(column.scrollTop,58)
 props.visible=true;await flush();assert.equal(column.scrollTop,16);assert.equal(state.podUID.value,'missing')
 assert.doesNotMatch(file,/\.scrollIntoView\(|\.focus\(/)
 assert.match(file,/\.explorer-column,\.is-deployment \.revision-column,\.is-deployment \.pod-column \{ max-height: none; overflow: visible; \}/)
 scope.stop()
})

test('折叠趋势仍累计真实采样、显示当前用量和失效提示，展开不会额外请求或补造点',async()=>{
 const requests=[],props=vue.reactive({cluster:'cluster',object:{...pod(),spec:{containers:[{name:'app'}]}},type:api.kubeTypes.find(item=>item.kind==='Pod'),trend:true,compact:true,embedded:true,disclosure:true,autoRefresh:false,canFollow:()=>true})
 const component=loadFrontend('../src/components/KubeResourceDiagnostics.vue',{vue:{...vue,onMounted(){},onBeforeUnmount(){}},'vue-i18n':{useI18n:()=>({t:key=>key,te:()=>false,locale:vue.ref('zh-CN')})},'echarts/core':{use(){}},'@/stores/preferences':{usePreferencesStore:()=>({theme:'light'})},'@/api/kubernetes':api,'@/utils/kubernetesDiagnostics':diagnostics,'@/api/client':{__esModule:true,default:{get:()=>new Promise(resolve=>requests.push(resolve))}}}).default
 const scope=vue.effectScope();let state;scope.run(()=>{state=component.setup(props,{expose(){},emit(){}})})
 assert.equal(state.metricsExpanded.value,false)
 const pending=state.load(),at=new Date().toISOString();requests[0]({data:{uid:'pod-a',state:'ok',metrics_at:at,containers:[{name:'app',cpu:.1,memory:1024}]}});await pending
 assert.equal(state.history.value.points.length,1);assert.equal(state.metricTotal('cpu'),'100 m')
 state.metricsExpanded.value=true;await flush();state.metricsExpanded.value=false;await flush();assert.equal(requests.length,1);assert.equal(state.history.value.points.length,1)
 state.now.value=Date.now()+180000;assert.equal(state.trendCurrent.value,false);assert.equal(state.trendHint.value,'k8s.podTrendStale')
 const template=parse(source('KubeResourceDiagnostics')).descriptor.template.content
 assert.ok(template.indexOf('role="status"')<template.indexOf('v-show="!disclosure||metricsExpanded"'))
 assert.match(template,/:message="error"><template #action>/)
 assert.match(source('KubeResourceDiagnostics'),/\.trend-state.is-unavailable\{color:var\(--workload-warning-text\)\}/)
 scope.stop()
})
