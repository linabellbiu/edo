import 'ant-design-vue/dist/reset.css'
import '/src/styles/vben.css'
import '/src/styles/resource-list.css'
import {createApp,h,computed} from 'vue'
import {createPinia} from 'pinia'
import {createMemoryHistory,createRouter,RouterView} from 'vue-router'
import {ConfigProvider,Button,theme} from 'ant-design-vue'
import client from '/src/api/client'
import i18n from '/src/locales'
import {useAuthStore} from '/src/stores/auth'
import {usePreferencesStore} from '/src/stores/preferences'
import PipelineEditorView from '/src/views/PipelineEditorView.vue'
import DeploymentPlanView from '/src/views/DeploymentPlanView.vue'

// 所有数据与写入均为当前浏览器中的合成夹具，不连接真实 EDO。
const libraryMode=false,applicationMode=new URLSearchParams(location.search).has('app')
const key=`edo.pipeline-task-configuration-preview${applicationMode?'-app':''}${libraryMode?'-reference':''}`,project='inline-preview'
const custom={id:'',name:'自定义构建',kind:'script',script:'npm ci\nnpm run build',runtime_image:'node:24-alpine',working_directory:'.',timeout_seconds:1800,is_active:true}
const mirror={id:'',name:'镜像构建',kind:'dockerfile',dockerfile_path:'docker/Dockerfile',context_path:'.',image_registry_id:'registry',timeout_seconds:1800,cache_enabled:true,pull:true,is_active:true}
const targetPlan=(kind)=>({name:kind==='script'?'自定义部署':'docker-compose 部署',kind,script:kind==='script'?'docker run -d --name preview-api "${EDO_IMAGE}"':undefined,compose_yaml:'services:\n  api:\n    image: example/api:${EDO_IMAGE_TAG}\n  worker:\n    image: alpine:3.22\n',timeout_seconds:600,deployment_target:{platform:'ssh',environment_id:'env',working_directory:kind==='script'?'/srv/preview':undefined},execution_config:{deployment_input_version:1,deployment_inputs:[],compose:kind==='compose_stack'?{project_name:'preview',tag_variable:'${EDO_IMAGE_TAG}',use_sudo:false,adopt_existing:false}:undefined}})
let saved=JSON.parse(sessionStorage.getItem(key)||'null')||{id:'inline-workflow',name:'构建部署任务验收',schema_version:1,revision:1,is_active:false,source:{id:'source',type:'trigger',name:'代码源',config:{branch:'main',events:['manual']}},stages:[{id:'build-stage',name:'构建',tasks:[{id:'build',type:'build',name:'自定义构建',config:{inline_build:custom,build_outputs:[{name:'BUILD_ARTIFACT',paths:['./output']}] }},{id:'image',type:'build',name:'镜像构建',config:{inline_build:mirror}}]},{id:'deploy-stage',name:'部署',tasks:[{id:'deploy',type:'deploy',name:'docker-compose 部署',config:{deployment_source:'inline',inline_deployment_plan:targetPlan('compose_stack')}},{id:'custom-deploy',type:'deploy',name:'自定义部署',config:{deployment_source:'inline',inline_deployment_plan:targetPlan('script')}}]}]}

if(libraryMode&&!sessionStorage.getItem(key))saved.stages[1].tasks[0].config={deployment_plan_id:'library-plan',compose:{project_name:'preview',tag_variable:'${EDO_IMAGE_TAG}',services:[]}}
const clone=value=>JSON.parse(JSON.stringify(value)),trace=[];window.edoInlineTrace=trace
const target={id:'target',platform:'docker',environment_id:'env',host_id:'host',runtime_id:'runtime',workload_name:'example-api',rollout_timeout:600}
const library={id:'library-plan',name:'已有部署方案',kind:'docker',is_active:true,deployment_target_id:'target',deployment_target:target,timeout_seconds:600,docker_config:{network:'bridge',restart_policy:'unless-stopped'}}
if(libraryMode)Object.assign(library,{kind:'compose_stack',compose_yaml:'services:\n  app:\n    image: example/api:${EDO_IMAGE_TAG}\n    restart: unless-stopped\n',deployment_target:{...target,platform:'ssh',working_directory:'/srv/preview'}})
client.defaults.adapter=async config=>{
 const path=config.url,method=config.method||'get';trace.push({path,method});let data
 if(method==='get')data=({'/workflow-templates':{workflow_templates:[clone(saved)]},'/workflows':{workflows:[{...clone(saved),kind:'template',project_id:project}]},'/workflow-templates/inline-workflow':{workflow_template:clone(saved),issues:[]},'/applications':{applications:applicationMode?[{id:'preview-app',name:'验收应用',repository:{id:'repo',name:'验收仓库',default_branch:'main'},workflows:[clone(saved)]}]:[]},'/applications/preview-app/workflows/inline-workflow':{workflow:clone(saved),issues:[]},'/build-plans':{build_plans:[{id:'build-plan',name:'镜像构建方案',kind:'dockerfile',is_active:true}]},'/deployment-plans':{deployment_plans:[library]},'/notification-channels':{channels:[]},'/settings/builtin-variables':{variables:[]},'/image-registries':{image_registries:[{id:'registry',name:'验收镜像仓库',project_id:project,is_active:true}]},'/delivery-variables':{variables:[{id:'compose-config',name:'应用 Compose 配置',kind:'config_yaml',version:1,project_id:project,is_active:true}]},'/application-databases':{databases:[]},'/environments':{environments:[{id:'env',name:'验收环境',is_active:true}]},'/hosts':{hosts:[{id:'host',name:'验收主机',mode:'ssh',address:'192.0.2.10',architecture:'amd64',environment_ids:['env'],is_active:true,capabilities:[{kind:'docker',runtime_id:'runtime',status:'ready'},{kind:'ssh',runtime_id:'',status:'ready'}]}]},'/docker/endpoints':{endpoints:[{id:'runtime',name:'验收 Docker',host_id:'host',is_active:true}]},'/kubernetes/clusters':{clusters:[]}})[path]
 else if(method==='put'&&(path==='/workflow-templates/inline-workflow'||path==='/applications/preview-app/workflows/inline-workflow')){
  const payload=typeof config.data==='string'?JSON.parse(config.data):config.data
  if(payload.activate)throw new Error('夹具不执行发布')
  saved={...clone(saved),...clone(payload),revision:saved.revision+1};sessionStorage.setItem(key,JSON.stringify(saved));data=applicationMode?{workflow:clone(saved),issues:[]}:{workflow_template:clone(saved),issues:[]}
 }else if(method==='post'&&path==='/workflows/deployment-script/check')data={issues:[]}
 else if(method==='post'&&path.endsWith('/compose-services'))data={services:['api','worker'],commit_sha:'a'.repeat(40)}
 else if(method==='post'&&path.endsWith('/validate'))data={valid:true,issues:[]}
 if(!data)throw new Error(`未登记的合成接口 ${method} ${path}`)
 return {data,status:200,statusText:'OK',headers:{},config}
}
window.fetch=()=>Promise.reject(new Error('夹具不访问真实 API'))
window.WebSocket=class extends EventTarget{constructor(){super();throw new Error('夹具不访问真实 WebSocket')}}
localStorage.setItem('edo.project',project)
const pinia=createPinia(),auth=useAuthStore(pinia),preferences=usePreferencesStore(pinia)
auth.user={id:'preview',project_id:project,department_id:'department',is_superuser:false,permissions:['workflow.read','workflow.update','image_registry.read','image_registry.execute','build_plan.read','deployment_plan.read','deployment.read','cluster.read','application.read','delivery_variable.read',...(libraryMode?['deployment_plan.update']:[])]};auth.loaded=true;auth.authorizationState='ready'
if(new URLSearchParams(location.search).get('readonly')==='1')auth.user.permissions=auth.user.permissions.filter(code=>!code.endsWith('.update'))
const router=createRouter({history:createMemoryHistory(),routes:[{path:'/pipeline-plans/editor',component:PipelineEditorView},{path:'/deployment-plans',component:DeploymentPlanView}]});await router.push({path:'/pipeline-plans/editor',query:applicationMode?{application:'preview-app',workflow:saved.id}:{template:saved.id}})
createApp({setup(){const tokens=computed(()=>({algorithm:preferences.theme==='dark'?theme.darkAlgorithm:theme.defaultAlgorithm,token:{colorBgLayout:preferences.theme==='dark'?'#0f1014':'#f5f6f8',colorBgContainer:preferences.theme==='dark'?'#17181d':'#ffffff'}}));return()=>h(ConfigProvider,{theme:tokens.value},{default:()=>[h(Button,{onClick:()=>preferences.toggleTheme()},()=> '切换主题'),h(Button,{onClick:()=>router.push({path:'/pipeline-plans/editor',query:applicationMode?{application:'preview-app',workflow:saved.id}:{template:saved.id}})},()=> '返回验收流水线'),h(RouterView)]})}}).use(pinia).use(i18n).use(router).mount('#root')
