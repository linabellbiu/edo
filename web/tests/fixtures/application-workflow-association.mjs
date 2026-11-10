import 'ant-design-vue/dist/reset.css'
import '/src/styles/vben.css'
import '/src/styles/resource-list.css'
import {createApp,h,computed} from 'vue'
import {createPinia} from 'pinia'
import {createRouter,createMemoryHistory} from 'vue-router'
import {ConfigProvider,Button,theme} from 'ant-design-vue'
import client from '/src/api/client'
import i18n from '/src/locales'
import {useAuthStore} from '/src/stores/auth'
import {usePreferencesStore} from '/src/stores/preferences'
import DevOpsView from '/src/views/DevOpsView.vue'
// 仅提供合成读取数据，拒绝真实接口和所有写操作。
const project='workflow-association-preview'
const retryPreview=new URLSearchParams(location.search).has('retry')
const flow={id:'flow',name:'已有发布流水线',is_active:true,source:{id:'source',type:'trigger',config:{events:['manual'],branch:'main'}},stages:[]}
const app={id:'app',name:'example_app',repository_id:'repo',repository:{id:'repo',name:'示例仓库'},is_active:true,workflows:[flow],poll_interval_seconds:60,sync_status:'synced'}
if(retryPreview)app.workflows.push({...flow,id:'other-flow',name:'另一路流水线'})
const previewRuns=retryPreview?[{id:'failed-flow-run',application_id:app.id,workflow_id:flow.id,status:'failed',stage:'failed',ref:'refs/tags/demo-v1',commit_sha:'a'.repeat(40),created_at:'2026-11-11T00:00:00Z'},{id:'other-flow-success',application_id:app.id,workflow_id:'other-flow',status:'succeeded',stage:'completed',created_at:'2026-11-11T01:00:00Z'}]:[]
const trace=[];window.edoWorkflowAssociationTrace=trace
client.defaults.adapter=async config=>{
 const path=config.url.split('?')[0],method=config.method||'get';trace.push({method,path})
 if(method!=='get')throw new Error('隔离视觉验收不执行写入')
 const data=({'/applications':{applications:[app,{...app,id:'empty-app',name:'empty_app',workflows:[]}]},'/repositories':{repositories:[{id:'repo',name:'示例仓库',is_active:true,default_branch:'main'}]},'/git-credentials':{credentials:[]},'/build-plans':{build_plans:[]},'/image-registries':{image_registries:[]},'/pipeline-runs':{pipeline_runs:previewRuns},'/release-plans':{release_plans:[]},'/deployments':{deployments:[]},'/deployment-plans':{deployment_plans:[]},'/workflow-templates':{workflow_templates:[{...flow,id:'shared',name:'新的共用流水线'}]},'/artifacts':{artifacts:[]},...(retryPreview?{'/pipeline-runs/failed-flow-run/retry-options':{ref:'refs/tags/demo-v1',commit_sha:'a'.repeat(40),artifacts:[]}}:{})})[path]
 if(!data)throw new Error('未登记的隔离接口 '+path)
 return {data,status:200,statusText:'OK',headers:{},config}
}
window.WebSocket=class extends EventTarget{readyState=0;close(){this.readyState=3}}
localStorage.setItem('edo.project',project)
const pinia=createPinia(),auth=useAuthStore(pinia),preferences=usePreferencesStore(pinia)
auth.user={id:'preview',project_id:project,department_id:'department',is_superuser:true,permissions:[]};auth.loaded=true;auth.authorizationState='ready'
const router=createRouter({history:createMemoryHistory(),routes:[{path:'/',component:{render:()=>null}}]});await router.push('/')
createApp({setup(){const tokens=computed(()=>({algorithm:preferences.theme==='dark'?theme.darkAlgorithm:theme.defaultAlgorithm}));return()=>h(ConfigProvider,{theme:tokens.value},{default:()=>[h(Button,{onClick:()=>preferences.toggleTheme()},()=> '切换主题'),h(DevOpsView,{section:'applications'})]})}}).use(pinia).use(i18n).use(router).mount('#root')
