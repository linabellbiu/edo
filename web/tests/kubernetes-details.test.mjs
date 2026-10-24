import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {test} from 'node:test'
import {parse, compileScript} from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'

function load(path, modules = {}) {
  const raw = readFileSync(new URL(path, import.meta.url), 'utf8')
  const source = path.endsWith('.vue') ? compileScript(parse(raw).descriptor, {id:'kube-details'}).content : raw
  const code = ts.transpileModule(source, {compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022,esModuleInterop:true}}).outputText
  const module = {exports:{}}
  new Function('require','module','exports',code)(name => modules[name] ?? {}, module, module.exports)
  return module.exports
}
const api = load('../src/api/kubernetes.ts')
const presentation = load('../src/utils/kubernetesPresentation.ts', {'@/api/kubernetes':api})
const details = load('../src/utils/kubernetesDetails.ts', {'@/api/kubernetes':api})
const component = load('../src/components/KubeResourceDetails.vue', {
  vue,
  'vue-i18n':{useI18n:()=>({t:key=>key})},
  '@/api/kubernetes':api,
  '@/utils/kubernetesPresentation':presentation,
  '@/utils/kubernetesDetails':details,
  '@/utils/kubernetesNode':{},
  '@/utils/time':{formatDateTime:value=>value??'—'},
}).default

function harness(object) {
  const props = vue.reactive({object,type:api.kubeTypes.find(type=>type.kind===object.kind),canFollow:()=>false})
  const scope = vue.effectScope()
  let state
  scope.run(()=>{state=component.setup(props,{expose(){},emit(){}})})
  return {state,props,stop:()=>scope.stop()}
}

test('容器按 Init、普通、临时种类展示，同名状态只来自对应状态数组',()=>{
  const object = {kind:'Pod',metadata:{name:'web',namespace:'team'},spec:{
    initContainers:[{name:'same',image:'init:1',restartPolicy:'Always'}],
    containers:[{name:'same',image:'app:1'}],ephemeralContainers:[{name:'same',image:'debug:1'}],
  },status:{
    initContainerStatuses:[{name:'same',ready:false,state:{waiting:{reason:'InitPending'}}}],
    containerStatuses:[{name:'same',ready:true,state:{running:{startedAt:'2026-10-24T00:00:00Z'}}}],
    ephemeralContainerStatuses:[{name:'same',state:{terminated:{reason:'Completed',exitCode:0}}}],
  }}
  const snapshot=structuredClone(object),rows=details.kubeContainerDetails(object)
  assert.deepEqual(rows.map(row=>row.key),['init/same','app/same','ephemeral/same'])
  assert.equal(rows[0].runtime.state.waiting.reason,'InitPending')
  assert.equal(rows[1].runtime.ready,true)
  assert.equal(rows[2].runtime.state.terminated.exitCode,0)
  assert.deepEqual(object,snapshot)
})

test('工作负载模板不借用实际容器状态；CronJob读取jobTemplate中的Pod模板',()=>{
  const row=details.kubeContainerDetails({kind:'CronJob',spec:{jobTemplate:{spec:{template:{spec:{containers:[{name:'scheduled',image:'cron:1'}]}}}}},status:{containerStatuses:[{name:'scheduled',ready:true}]}})
  assert.equal(row.length,1)
  assert.equal(row[0].container.image,'cron:1')
  assert.equal(row[0].runtime,undefined)
  assert.deepEqual(details.kubeContainerDetails({kind:'Deployment',spec:{}}),[])
})

test('环境来源保留namespace、键、前缀与optional，不复制环境变量直接值或Secret内容',()=>{
  const container={env:[
    {name:'PASSWORD',value:'synthetic-sensitive-literal'},
    {name:'EMPTY',value:''},
    {name:'CONFIG',valueFrom:{configMapKeyRef:{name:'settings',key:'app.yml',optional:false}}},
    {name:'TOKEN',valueFrom:{secretKeyRef:{name:'credentials',key:'api-token',optional:true}}},
    {name:'POD_NAME',valueFrom:{fieldRef:{fieldPath:'metadata.name'}}},
    {name:'CPU_LIMIT',valueFrom:{resourceFieldRef:{containerName:'app',resource:'limits.cpu',divisor:'1m'}}},
    {name:'FUTURE',valueFrom:{unsupported:{token:'another-sensitive-value'}}},
  ],envFrom:[{prefix:'APP_',configMapRef:{name:'all-settings'}},{prefix:'SECRET_',secretRef:{name:'all-secrets',optional:false}}]}
  const rows=details.kubeEnvironmentSources(container,'team')
  assert.equal(rows[0].kind,'direct')
  assert.equal(rows[1].kind,'direct')
  assert.deepEqual(rows[2].reference,{resource:'configmaps',name:'settings',namespace:'team'})
  assert.equal(rows[2].optional,false)
  assert.deepEqual(rows[3].reference,{resource:'secrets',name:'credentials',namespace:'team'})
  assert.equal(rows[3].key,'api-token')
  assert.equal(rows[4].field,'metadata.name')
  assert.equal(rows[5].field,'app / limits.cpu / 1m')
  assert.equal(rows[6].kind,'unknown')
  assert.equal(rows[7].name,'*')
  assert.equal(rows[7].prefix,'APP_')
  assert.equal(rows[8].optional,false)
  assert.doesNotMatch(JSON.stringify(rows),/synthetic-sensitive-literal|another-sensitive-value/)
})

test('探针保留命名端口、全部类型与显式零，不捏造缺失的阈值',()=>{
  const rows=details.kubeContainerProbes({
    startupProbe:{httpGet:{port:'health',path:'/ready',scheme:'HTTPS'},initialDelaySeconds:0,failureThreshold:30},
    readinessProbe:{tcpSocket:{host:'127.0.0.1',port:8080},periodSeconds:5},
    livenessProbe:{grpc:{port:9090,service:'health'},timeoutSeconds:1},
  })
  assert.deepEqual(rows.map(row=>row.method),['httpGet','tcpSocket','grpc'])
  assert.equal(rows[0].target,'HTTPS health/ready')
  assert.deepEqual(rows[0].timings,[{key:'initialDelaySeconds',value:0},{key:'failureThreshold',value:30}])
  assert.equal(rows[1].target,'127.0.0.1:8080')
  assert.equal(rows[2].target,'9090 / health')
  assert.equal(details.kubeContainerProbes({readinessProbe:{exec:{command:['sh','-c','test -f /ready']}}})[0].target,'sh -c test -f /ready')
  assert.deepEqual(details.kubeContainerProbes({}),[])
})

test('卷引用保留准确资源类型和namespace，projected不读取Secret或令牌值',()=>{
  const rows=details.kubeVolumeDetails({kind:'Deployment',metadata:{namespace:'team'},spec:{template:{spec:{volumes:[
    {name:'storage',persistentVolumeClaim:{claimName:'app-data',readOnly:false}},
    {name:'config',configMap:{name:'settings',defaultMode:420}},
    {name:'secret',secret:{secretName:'credentials'}},
    {name:'projected',projected:{sources:[{configMap:{name:'settings'}},{secret:{name:'credentials'}},{secret:{name:'credentials',items:[{key:'other',path:'other'}]}},{serviceAccountToken:{path:'token'}}]}},
    {name:'memory',emptyDir:{medium:'Memory',sizeLimit:'64Mi'}},
    {name:'unknown',futureVolume:{driver:'future'}},
  ]}}}})
  assert.equal(rows[0].references[0].resource,'persistentvolumeclaims')
  assert.equal(rows[0].source.readOnly,false)
  assert.deepEqual(rows[3].references,[{resource:'configmaps',name:'settings',namespace:'team'},{resource:'secrets',name:'credentials',namespace:'team'}])
  assert.equal(rows[4].source.sizeLimit,'64Mi')
  assert.equal(rows[5].kind,'futureVolume')
  assert.deepEqual(rows[5].references,[])
})

test('详情选择容器切换配置，资源切换重置选择，不修改传入对象或发起请求',async()=>{
  const object={kind:'Pod',metadata:{name:'web',namespace:'team',uid:'pod1'},spec:{containers:[{name:'app',image:'app:1',env:[{name:'A',value:'not-copied'}]},{name:'sidecar',image:'sidecar:1',ports:[{containerPort:9090}]}]}}
  const snapshot=structuredClone(object),{state,props,stop}=harness(object)
  assert.equal(state.selectedContainer.value.container.name,'app')
  assert.equal(state.environment.value[0].name,'A')
  state.selectedContainerKey.value='app/sidecar'
  assert.equal(state.selectedContainer.value.container.name,'sidecar')
  assert.deepEqual(state.environment.value,[])
  props.object={kind:'Pod',metadata:{name:'other',namespace:'team',uid:'pod2'},spec:{containers:[{name:'app',image:'other:1'},{name:'sidecar',image:'other-sidecar:1'}]}}
  await vue.nextTick()
  assert.equal(state.selectedContainer.value.container.name,'app')
  assert.deepEqual(object,snapshot)
  stop()
})
test('Pod详情容器跟随日志工具栏选择，内部选择发出准确容器名',async()=>{
 const events=[],props=vue.reactive({object:{kind:'Pod',metadata:{uid:'p'},spec:{containers:[{name:'app',image:'app:1'}],initContainers:[{name:'prepare',image:'init:1'}]}},container:'app',type:api.kubeTypes.find(item=>item.kind==='Pod'),canFollow:()=>false}),scope=vue.effectScope();let state
 scope.run(()=>{state=component.setup(props,{expose(){},emit:(...args)=>events.push(args)})})
 assert.equal(state.selectedContainer.value.container.name,'app');props.container='prepare';assert.equal(state.selectedContainer.value.kind,'init')
 state.selectContainer('app/app','app');assert.deepEqual(events,[['container','app']]);scope.stop()
})

test('缺失状态不补零，false/退出码零保持真实含义；资源单位沿用统一格式',()=>{
  const {state,stop}=harness({kind:'Pod',metadata:{uid:'p'},spec:{containers:[{name:'app',image:'app:1'}]}})
  assert.equal(state.containerState(undefined),'—')
  assert.equal(state.booleanValue(undefined),'—')
  assert.equal(state.booleanValue(false),'k8s.no')
  assert.equal(state.booleanValue(true),'k8s.yes')
  assert.equal(state.containerState({state:{terminated:{reason:'Completed',exitCode:0}}}),'Completed')
  assert.equal(state.resources({resources:{requests:{cpu:'250m',memory:'1Gi'}}},'requests'),'cpu: 250 m · memory: 1 GiB')
  assert.equal(state.resources({},'limits'),'—')
  assert.equal(state.rawResources({resources:{limits:{memory:'1024Mi'}}},'limits'),'memory: 1024Mi')
  stop()
})

test('调度显示显式字段和实际规则，不补集群默认值；卷容量转换只用于展示',()=>{
  const {state,stop}=harness({kind:'Deployment',metadata:{uid:'deployment'},spec:{template:{spec:{containers:[{name:'app',image:'app:1'}],hostNetwork:false,terminationGracePeriodSeconds:0,serviceAccountName:'application',nodeSelector:{zone:'a'},affinity:{nodeAffinity:{}},tolerations:[],topologySpreadConstraints:[{topologyKey:'zone',maxSkew:1}]}}}})
  assert.deepEqual(state.schedulingFields.value.map(field=>[field.key,field.value]),[['serviceAccountName','application'],['terminationGracePeriodSeconds',0],['hostNetwork',false]])
  assert.deepEqual(state.schedulingRules.value,['affinity','topologySpreadConstraints'])
  assert.deepEqual(state.volumeProperties({source:{sizeLimit:'64Mi',readOnly:false}}),[{key:'sizeLimit',value:'64 MiB'},{key:'readOnly',value:'k8s.no'}])
  stop()
})

test('模板的关联按钮逐项检查canFollow，环境变量模型不渲染直接值，配置不提供写操作',()=>{
  const template=parse(readFileSync(new URL('../src/components/KubeResourceDetails.vue',import.meta.url),'utf8')).descriptor.template.content
  assert.match(template,/v-if="canFollow\(env\.reference\)"/)
  assert.match(template,/v-if="canFollow\(reference\)"/)
  assert.match(template,/aria-pressed="selectedContainer\?\.key===item\.key"/)
  assert.doesNotMatch(template,/env\.value|\.post\(|\.patch\(|\.delete\(/)
})
