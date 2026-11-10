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
import DevOpsView from '/src/views/DevOpsView.vue'

// 接口与写入仅保存在当前浏览器，不连接真实 EDO。
const project='build-preview',key='edo.build-plan-preview',clone=x=>JSON.parse(JSON.stringify(x))
let plan={id:'shared-build',project_id:project,department_id:'department',name:'通用镜像构建',kind:'dockerfile',dockerfile_path:'Dockerfile',context_path:'.',image_registry_id:'registry',pull:true,cache_enabled:true,build_args:{MODE:'release'},environment_variables:{},timeout_seconds:1800,is_active:true}
let workflow=JSON.parse(sessionStorage.getItem(key)||'null')||{id:'flow',name:'构建方案验收',schema_version:1,revision:1,is_active:false,source:{id:'source',type:'trigger',name:'代码源',config:{branch:'main',events:['manual']}},stages:[{id:'build-stage',name:'构建',tasks:[{id:'build',type:'build',name:'镜像构建',config:{build_plan_id:plan.id,image_tag:'${EDO_GIT_TAG}'}}]}]}
const trace=[];window.edoBuildTrace=trace
client.defaults.adapter=async config=>{
 const path=config.url,method=config.method||'get',payload=typeof config.data==='string'?JSON.parse(config.data):config.data
 trace.push({path,method,...(payload?{payload}: {})})
 let data
 if(method==='get')data=({'/workflow-templates':{workflow_templates:[clone(workflow)]},'/workflows':{workflows:[{...clone(workflow),kind:'template',project_id:project}]},'/workflow-templates/flow':{workflow_template:clone(workflow),issues:[]},'/applications':{applications:[]},'/build-plans':{build_plans:[clone(plan)]},'/deployment-plans':{deployment_plans:[]},'/deployments?limit=200':{deployments:[]},'/notification-channels':{channels:[]},'/settings/builtin-variables':{variables:[]},'/image-registries':{image_registries:[{id:'registry',name:'验收镜像仓库',project_id:project,is_active:true,endpoint:'https://registry.example.com',namespace:'demo'}]},'/repositories':{repositories:[]},'/credentials':{credentials:[]},'/artifacts':{artifacts:[]}})[path]
 else if(method==='put'&&path==='/workflow-templates/flow'){
  if(payload.activate)throw new Error('夹具不执行发布')
  workflow={...workflow,...clone(payload),revision:workflow.revision+1};sessionStorage.setItem(key,JSON.stringify(workflow));data={workflow_template:clone(workflow),issues:[]}
 }else if(method==='put'&&path==='/build-plans/shared-build'){plan={...plan,...payload};data={build_plan:clone(plan)}}
 else if(method==='post'&&path.endsWith('/validate'))data={valid:true,issues:[]}
 if(!data)throw new Error(`未登记的隔离接口 ${method} ${path}`)
 return {data,status:200,statusText:'OK',headers:{},config}
}
window.fetch=()=>Promise.reject(new Error('夹具不访问真实 API'))
window.WebSocket=class extends EventTarget{constructor(){super();throw new Error('夹具不访问真实 WebSocket')}}
localStorage.setItem('edo.project',project)
const pinia=createPinia(),auth=useAuthStore(pinia),preferences=usePreferencesStore(pinia)
auth.user={id:'preview',project_id:project,department_id:'department',is_superuser:false,permissions:['workflow.read','workflow.update','build_plan.read','build_plan.update','build_plan.create','deployment_plan.read','deployment.read','application.read','image_registry.read','repository.read','notification.read']};auth.loaded=true;auth.authorizationState='ready'
const router=createRouter({history:createMemoryHistory(),routes:[{path:'/pipeline-plans/editor',component:PipelineEditorView},{path:'/build-plans',component:DevOpsView,props:{section:'build-plans'}}]});await router.push({path:'/pipeline-plans/editor',query:{template:workflow.id}})
createApp({setup(){const tokens=computed(()=>({algorithm:preferences.theme==='dark'?theme.darkAlgorithm:theme.defaultAlgorithm,token:{colorBgLayout:preferences.theme==='dark'?'#0f1014':'#f5f6f8',colorBgContainer:preferences.theme==='dark'?'#17181d':'#ffffff'}}));return()=>h(ConfigProvider,{theme:tokens.value},{default:()=>[h(Button,{onClick:()=>preferences.toggleTheme()},()=> '切换主题'),h(Button,{onClick:()=>router.push({path:'/pipeline-plans/editor',query:{template:workflow.id}})},()=> '返回验收流水线'),h(RouterView)]})}}).use(pinia).use(i18n).use(router).mount('#root')
