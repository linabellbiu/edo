import assert from 'node:assert/strict'
import {test} from 'node:test'
import {readFileSync} from 'node:fs'
import * as vue from 'vue'
import {compileScript,compileTemplate,parse} from '@vue/compiler-sfc'
import {renderToString} from '@vue/server-renderer'
import ts from 'typescript'
import {loadFrontend,componentHarness} from './acceptanceHarness.mjs'

const utils=loadFrontend('../src/utils/kubernetesCommonOptions.ts')
const settle=async()=>{await vue.nextTick();await new Promise(resolve=>setImmediate(resolve));await vue.nextTick()}
function harness(options,disabled=false,codes=['kubernetes.workload.update']){
 const props=vue.reactive({modelValue:options,disabled})
 const icon=vue.defineComponent({setup:()=>()=>vue.h('span')})
 const h=componentHarness('../src/components/KubernetesCommonOptionsFields.vue',{props,codes,modules:{'@/utils/kubernetesCommonOptions':utils,'lucide-vue-next':{Plus:icon,Trash2:icon}}})
 return {...h,props,updates:()=>h.events.filter(event=>event.name==='update:modelValue'),async apply(){const event=h.events.filter(event=>event.name==='update:modelValue').at(-1);if(event)props.modelValue=event.args[0];await settle()}}
}

test('通用参数采用真实 Kubernetes 标识及引用，保留空值和零值',()=>{
 const options={labels:{'app.example.com/tier':'web','empty':''},annotations:{'example.com/note':'第一行\n第二行'},environment:[{name:'APP_ENV',kind:'value',value:''},{name:'my.env-name',kind:'configmap',source_name:'app-config',source_key:'settings.yaml'},{name:'API_TOKEN',kind:'secret',source_name:'api-token',source_key:'token'}],volumes:[{name:'data',kind:'pvc',source_name:'app-data',mount_path:'/app/data',read_only:false},{name:'configuration',kind:'configmap',source_name:'app-config',mount_path:'/app/config',sub_path:'settings.yaml',read_only:true},{name:'temporary',kind:'emptyDir',mount_path:'/tmp/cache'}],node_selector:{'kubernetes.io/os':'linux'},tolerations:[{key:'dedicated',operator:'Equal',value:'web',effect:'NoSchedule'},{operator:'Exists',effect:'NoExecute',toleration_seconds:0}],image_pull_secrets:['registry-auth'],min_ready_seconds:0,revision_history_limit:0,progress_deadline_seconds:1}
 const original=JSON.stringify(options)
 assert.equal(utils.kubernetesCommonOptionsError(options),'')
 assert.equal(JSON.stringify(options),original)
 assert.equal(utils.kubernetesCommonOptionsError(undefined),'')
 assert.equal(utils.kubernetesCommonOptionsError({labels:{},annotations:{},environment:[],volumes:[],node_selector:{},tolerations:[],image_pull_secrets:[]}), '')
})

test('失焦校验保留已有错误区域，避免按下添加按钮后布局插入错误行吞掉点击',async t=>{
 const h=harness({labels:{'':''},environment:[{name:'',kind:'value',value:''}],volumes:[{name:'',kind:'pvc',mount_path:''}],tolerations:[{key:'',operator:'Equal',value:''}]});t.after(h.stop)
 const source=readFileSync(new URL('../src/components/KubernetesCommonOptionsFields.vue',import.meta.url),'utf8')
 const descriptor=parse(source).descriptor,bindings=compileScript(descriptor,{id:'common-error-layout'}).bindings
 const compiled=compileTemplate({source:descriptor.template.content,filename:'KubernetesCommonOptionsFields.vue',id:'common-error-layout',compilerOptions:{bindingMetadata:bindings}})
 assert.deepEqual(compiled.errors,[])
 const code=ts.transpileModule(compiled.code,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022}}).outputText
 const module={exports:{}};new Function('require','module','exports',code)(()=>vue,module,module.exports)
 const slot=vue.defineComponent({inheritAttrs:false,setup:(_,context)=>()=>vue.h('div',context.slots.default?.())})
 const render=async()=>{
  const app=vue.createSSRApp({render:()=>module.exports.render({},[],h.props,vue.proxyRefs(h.state),{}, {})})
  for(const name of ['a-collapse','a-collapse-panel','a-form-item','a-button','a-input','a-textarea','a-select','a-checkbox','a-input-number'])app.component(name,slot)
  return renderToString(app)
 }
 const before=await render()
 assert.equal((before.match(/inline-error-slot/g)||[]).length,5)
 h.state.markMap('labels',0);h.state.mark('environment',0);h.state.mark('volumes',0);h.state.mark('tolerations',0)
 const after=await render()
 assert.equal((after.match(/inline-error-slot/g)||[]).length,5,'错误只填充已有区域，不新增布局行')
 assert.ok(after.includes('环境变量名格式无效'))
 assert.ok(after.includes('标签键格式无效'))
 assert.ok(after.includes('卷名称须为'))
 assert.ok(after.includes('容忍键格式无效'))
 assert.equal((after.match(/aria-live="polite"/g)||[]).length,5)
})

test('拒绝覆盖EDO身份、重复变量/挂载及无效或含跳转的资源路径',()=>{
 for(const key of ['edo.io/target-id','edo.dev/project','app.kubernetes.io/managed-by']){
  assert.match(utils.kubernetesCommonOptionsError({labels:{[key]:'other'}}),/资源身份/)
  assert.match(utils.kubernetesCommonOptionsError({annotations:{[key]:'other'}}),/资源身份/)
 }
 const cases=[
  [{labels:{'example..com/key':'web'}},/标签键/],
  [{environment:[{name:'APP_ENV',kind:'value',value:'dev'},{name:'APP_ENV',kind:'value',value:'prod'}]},/不能重复/],
  [{environment:[{name:'TOKEN',kind:'secret',source_name:'token',source_key:'../token'}]},/名称和键/],
  [{environment:[{name:'TOKEN',kind:'secret',source_name:'token',source_key:'token',value:'secret-inline'}]},/名称和键/],
  [{volumes:[{name:'data',kind:'pvc',source_name:'data',mount_path:'/'}]},/挂载路径/],
  [{volumes:[{name:'data',kind:'pvc',source_name:'data',mount_path:'/app/../private'}]},/挂载路径/],
  [{volumes:[{name:'data',kind:'pvc',source_name:'data',mount_path:'/app/data',sub_path:'../private'}]},/subPath/],
  [{volumes:[{name:'first',kind:'emptyDir',mount_path:'/data'},{name:'second',kind:'emptyDir',mount_path:'/data'}]},/不能重复/],
  [{volumes:[{name:'data',kind:'hostPath',source_name:'/etc',mount_path:'/data'}]},/来源/],
  [{image_pull_secrets:['registry-auth','registry-auth']},/不能重复/],
 ]
 for(const [options,message]of cases)assert.match(utils.kubernetesCommonOptionsError(options),message)
})

test('按UTF8字节计量值与总量，时序边界跟后端一致并保留0',()=>{
 assert.equal(utils.kubernetesCommonOptionsError({environment:[{name:'APP',kind:'value',value:'x'.repeat(4096)}]}),'')
 assert.match(utils.kubernetesCommonOptionsError({environment:[{name:'APP',kind:'value',value:'中'.repeat(1366)}]}),/4 KiB/)
 assert.match(utils.kubernetesCommonOptionsError({annotations:{'example.com/note':'中'.repeat(1366)}}),/4 KiB/)
 assert.match(utils.kubernetesCommonOptionsError({environment:Array.from({length:17},(_,i)=>({name:`APP_${i}`,kind:'value',value:'a'.repeat(4096)}))}),/总大小/)
 assert.match(utils.kubernetesCommonOptionsError({environment:Array.from({length:65},(_,i)=>({name:`APP_${i}`,kind:'value',value:''}))}),/最多 64/)
 assert.equal(utils.kubernetesCommonOptionsError({min_ready_seconds:0,revision_history_limit:0,progress_deadline_seconds:1}), '')
 assert.match(utils.kubernetesCommonOptionsError({min_ready_seconds:600}),/默认.*600/)
 assert.match(utils.kubernetesCommonOptionsError({min_ready_seconds:86401,progress_deadline_seconds:86402}),/86400/)
 assert.match(utils.kubernetesCommonOptionsError({revision_history_limit:101}),/100/)
 assert.match(utils.kubernetesCommonOptionsError({tolerations:[{operator:'Exists',effect:'NoSchedule',toleration_seconds:0}]}),/NoExecute/)
 assert.match(utils.kubernetesCommonOptionsError({tolerations:[{operator:'Exists',effect:'NoExecute',toleration_seconds:31536001}]}),/31536000/)
 assert.equal(utils.kubernetesCommonOptionsError({tolerations:[{operator:'',key:'app',value:'web',effect:'NoSchedule'}]}),'')
})

test('打开通用参数不补写、不查询集群或Secret，无权限无法通过事件修改',async t=>{
 const h=harness(undefined);t.after(h.stop);await settle()
 assert.deepEqual(h.updates(),[]);assert.deepEqual(h.requests,[])
 for(const denied of [harness({},true),harness({},false,[])]){
  t.after(denied.stop)
  denied.state.addMap('labels');denied.state.addEnvironment();denied.state.addVolume();denied.state.addToleration();denied.state.pullSecrets(['secret']);denied.state.number('revision_history_limit',0)
  assert.deepEqual(denied.updates(),[]);assert.deepEqual(denied.state.maps.value.labels,[])
 }
})

test('标签重复键保留本地草稿并报告阻断，回显与其他字段修改不重置空行',async t=>{
 const original={labels:{app:'web'}}
 const h=harness(original);t.after(h.stop);await settle()
 h.state.addMap('labels');assert.equal(h.state.maps.value.labels.length,2)
 await settle();assert.match(h.state.validationError.value,/标签键/)
 h.state.number('revision_history_limit',0);await h.apply()
 assert.equal(h.state.maps.value.labels.length,2);assert.equal(h.state.maps.value.labels[1].key,'')
 h.state.mapRow('labels',1,{key:'app',value:'worker'});await settle()
 assert.equal(h.state.maps.value.labels[1].value,'worker')
 assert.match(h.events.filter(event=>event.name==='validation-change').at(-1).args[0],/重复/)
 assert.deepEqual(h.props.modelValue.labels,{app:'web'})
 h.state.mapRow('labels',1,{key:'component'});await h.apply()
 assert.deepEqual(h.props.modelValue.labels,{app:'web',component:'worker'})
 assert.equal(h.state.validationError.value,'')
 assert.deepEqual(original,{labels:{app:'web'}})
 h.state.addMap('annotations');await settle()
 assert.match(h.state.validationError.value,/注解键/)
 h.props.modelValue=undefined;await settle()
 assert.deepEqual(h.state.maps.value,{labels:[],annotations:[],node_selector:[]});assert.equal(h.state.validationError.value,'')
})

test('删除最后条目提交显式空集合，0值和无时限不混淆，来源切换不夹带敏感值',async t=>{
 const h=harness({labels:{app:'web'},environment:[{name:'TOKEN',kind:'value',value:'local'}],volumes:[{name:'cache',kind:'pvc',source_name:'cache',mount_path:'/data'}],tolerations:[{operator:'Exists',effect:'NoExecute',toleration_seconds:9}],image_pull_secrets:['registry-auth']});t.after(h.stop)
 h.state.environmentKind(0,'secret');await h.apply()
 assert.equal(h.props.modelValue.environment[0].name,'TOKEN');assert.equal(h.props.modelValue.environment[0].value,undefined)
 h.state.environment(0,{source_name:'token',source_key:'value'});await h.apply()
 assert.deepEqual(h.props.modelValue.environment[0],{name:'TOKEN',kind:'secret',value:undefined,source_name:'token',source_key:'value'})
 h.state.volumeKind(0,'emptyDir');await h.apply();assert.equal(h.props.modelValue.volumes[0].source_name,undefined);assert.equal(h.props.modelValue.volumes[0].mount_path,'/data')
 h.state.toleration(0,{toleration_seconds:0});await h.apply();assert.equal(h.props.modelValue.tolerations[0].toleration_seconds,0)
 h.state.number('min_ready_seconds',0);await h.apply();h.state.number('revision_history_limit',0);await h.apply()
 assert.equal(h.props.modelValue.min_ready_seconds,0);assert.equal(h.props.modelValue.revision_history_limit,0)
 h.state.number('min_ready_seconds',null);await h.apply();assert.equal(h.props.modelValue.min_ready_seconds,undefined)
 h.state.removeMap('labels',0);await h.apply();assert.deepEqual(h.props.modelValue.labels,{})
 h.state.removeEnvironment(0);await h.apply();assert.deepEqual(h.props.modelValue.environment,[])
 h.state.removeVolume(0);await h.apply();assert.deepEqual(h.props.modelValue.volumes,[])
 h.state.removeToleration(0);await h.apply();assert.deepEqual(h.props.modelValue.tolerations,[])
 h.state.pullSecrets([]);await h.apply();assert.deepEqual(h.props.modelValue.image_pull_secrets,[])
 assert.equal(utils.kubernetesCommonOptionsError(h.props.modelValue),'')
 assert.deepEqual(h.requests,[])
})
