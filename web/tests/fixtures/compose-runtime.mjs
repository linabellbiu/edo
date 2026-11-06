import {createApp,h,ref,computed} from 'vue'
import {createPinia} from 'pinia'
import {createMemoryHistory,createRouter} from 'vue-router'
import {Button,ConfigProvider,theme} from 'ant-design-vue'
import 'ant-design-vue/dist/reset.css'
import '/src/styles/vben.css'
import '/src/styles/resource-list.css'
import DevOpsView from '/src/views/DevOpsView.vue'
import {useAuthStore} from '/src/stores/auth'
import client from '/src/api/client'
import i18n from '/src/locales'

// 真实组件配合合成接口，只验收展示及确认交互，不连接业务 API 或控制实际容器。
const project='compose-preview',appID='compose-app',recordID='compose-record',mode=ref(new URL(location.href).searchParams.get('state')||'running')
const reader=new URL(location.href).searchParams.get('role')==='reader'
const target={id:'target',platform:'ssh',host_id:'host',environment_id:'env',runtime_id:'',working_directory:'/srv/example',workload_name:'',container_name:''}
const plan={id:'plan',kind:'compose_stack',deployment_target:target,is_active:true}
const workflow={id:'flow',name:'多镜像发布',is_active:true,source:{id:'source',config:{branch:'main',events:['manual']}},stages:[{id:'release',tasks:[{id:'deploy',type:'deploy',config:{deployment_plan_id:'plan'}}]}]}
const app={id:appID,name:'多镜像示例应用',repository_id:'repo',description:'Compose 运行状态验收',is_active:true,sync_status:'synced',workflows:[workflow]}
const record={...target,id:recordID,row_id:1,target_id:target.id,target_name:'示例 Compose 项目',application_id:appID,pipeline_run_id:'run',workflow_node_id:'deploy',deployment_plan_id:plan.id,deployment_plan_kind:'compose_stack',operation:'release',status:'succeeded',created_at:'2026-11-06T06:00:00Z',finished_at:'2026-11-06T06:00:10Z'}
const runtime=()=>({deployment_id:recordID,kind:'compose',name:'example',state:mode.value,running:mode.value==='running',count:mode.value==='partial'?1:2,services:[{name:'api',state:mode.value==='exited'?'exited':'running',count:1,ready_count:mode.value==='exited'?0:1},{name:'worker',state:mode.value==='partial'?'missing':mode.value==='exited'?'exited':'running',count:mode.value==='partial'?0:1,ready_count:mode.value==='running'?1:0}]})
client.defaults.adapter=async config=>{
 let data
 const path=config.url
 if(config.method==='get')data=({'/applications':{applications:[app]},'/deployment-plans':{deployment_plans:[plan]},'/deployments?limit=200':{deployments:[record]},'/workflow-templates':{workflow_templates:[]},[`/deployments/${recordID}/runtime`]:{runtime:runtime()}})[path]
 else if(config.method==='post'&&(path.endsWith('/runtime/stop')||path.endsWith('/runtime/restart'))){mode.value=path.endsWith('/stop')?'exited':'running';data={runtime:runtime()}}
 if(!data)throw new Error(`未登记合成接口：${config.method} ${path}`)
 return {data,status:200,statusText:'OK',headers:{},config}
}
window.fetch=()=>Promise.reject(new Error('验收页不连接实际服务'))
window.WebSocket=class extends EventTarget{readyState=0;close(){this.readyState=3}}
localStorage.setItem('edo.project',project)
const pinia=createPinia(),auth=useAuthStore(pinia)
auth.user={id:'preview-user',username:'preview',project_id:project,department_id:'preview-department',is_superuser:false,permissions:['application.read','deployment.read','workflow.read','deployment_plan.read',...(!reader?['deployment.execute','deployment.update']:[])]}
auth.loaded=true;auth.authorizationState='ready'
const router=createRouter({history:createMemoryHistory(),routes:[{path:'/',component:{render:()=>null}}]});await router.push('/')
const root=createApp({setup(){const dark=ref(new URL(location.href).searchParams.get('theme')==='dark');document.documentElement.dataset.theme=dark.value?'dark':'light';const antTheme=computed(()=>({algorithm:dark.value?theme.darkAlgorithm:theme.defaultAlgorithm}));return()=>h(ConfigProvider,{theme:antTheme.value},{default:()=>h('main',{style:'padding:20px'},[h('p','合成接口验收，不操作实际项目。'),h(Button,{onClick(){mode.value=mode.value==='partial'?'running':'partial'}},()=> '切换服务缺失场景'),h(DevOpsView,{section:'applications'})])})}})
root.use(pinia).use(router).use(i18n).mount('#app')
