import { createApp, h, ref, computed } from 'vue'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import { ConfigProvider, Button, theme } from 'ant-design-vue'
import 'ant-design-vue/dist/reset.css'
import '/src/styles/vben.css'
import '/src/styles/resource-list.css'
import DevOpsView from '/src/views/DevOpsView.vue'
import ComposeTaskFields from '/src/components/ComposeTaskFields.vue'
import client from '/src/api/client'
import i18n from '/src/locales'
import { useAuthStore } from '/src/stores/auth'

// 合成接口仅用于真实组件的页面验收，不连接实际项目或发布服务。
const project='app-env-preview', application='app-env-application'
localStorage.setItem('edo.project',project)
const pinia=createPinia(), auth=useAuthStore(pinia)
auth.user={id:'preview-user',username:'preview',department_id:'preview-department',project_id:project,is_superuser:false,permissions:['application.read','delivery_variable.read','delivery_variable.create','delivery_variable.update','delivery_variable.delete','deployment.read','deployment_plan.read','workflow.read','workflow.update']}
auth.loaded=true;auth.authorizationState='ready'
const rows=[]
client.defaults.adapter=async config=>{
 let data, path=config.url
 if(config.method==='get'){
  const responses={
   '/applications':{applications:[{id:application,project_id:project,name:'示例应用',description:'应用环境配置验收',repository_id:'preview-repository',is_active:true,workflows:[],sync_status:'idle',poll_interval_seconds:60}]},
   '/projects':{projects:[{id:project,name:'示例项目'}]},
   '/environments':{environments:[{id:'test',name:'测试环境'},{id:'prod',name:'生产环境'}]},
   '/deployments?limit=200':{deployments:[]},'/workflow-templates':{workflow_templates:[]},
   '/deployment-plans':{deployment_plans:[{id:'compose-plan',kind:'compose_stack',compose_yaml:'services:\n  api:\n    image: alpine:3.22\n  worker:\n    image: alpine:3.22\n'}]},
   '/delivery-variables':{variables:[...rows]},
  }
  data=responses[path]
 }else if(path.startsWith('/delivery-variables')){
  const body=JSON.parse(config.data||'{}')
  if(config.method==='post'){const row={...body,id:`variable-${rows.length}`,project_id:project,version:1,...(body.is_secret?{value:undefined}:{})};rows.unshift(row);data={variable:row}}
  else if(config.method==='put'){const row=rows.find(item=>path.endsWith(item.id));Object.assign(row,{...body,version:row.version+1,...(body.is_secret?{value:undefined}:{})});data={variable:row}}
 } 
 if(!data)throw new Error(`未登记的合成接口：${config.method} ${path}`)
 return {data,status:200,statusText:'OK',headers:{},config}
}
window.fetch=()=>Promise.reject(new Error('验收页不连接真实 API'))
window.WebSocket=class extends EventTarget{readyState=0;close(){this.readyState=3}}
const router=createRouter({history:createMemoryHistory(),routes:[{path:'/',component:{render:()=>null}}]});await router.push('/')
const app=createApp({setup(){
 const dark=ref(false),node=ref({id:'deploy',type:'deploy',name:'部署',config:{deployment_plan_id:'compose-plan',compose:{project_name:'example',tag_variable:'IMAGE_TAG'}}})
 const antTheme=computed(()=>({algorithm:dark.value?theme.darkAlgorithm:theme.defaultAlgorithm}))
 return ()=>h(ConfigProvider,{theme:antTheme.value},{default:()=>h('main',{style:'padding:20px'},[
  h(Button,{onClick(){dark.value=!dark.value;document.documentElement.dataset.theme=dark.value?'dark':'light'}},()=> '切换主题'),
  h('p','合成接口验收，不代表实际服务部署。'),h(DevOpsView,{section:'applications'}),
  h('section',{style:'max-width:700px;margin-top:24px'},[h('h2','Compose 任务'),h(ComposeTaskFields,{node:node.value,buildNodes:[],disabled:false,onChange:patch=>node.value={...node.value,config:{...node.value.config,...patch}}})]),
 ])})
}});app.use(pinia).use(router).use(i18n).mount('#app')
