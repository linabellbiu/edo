import 'ant-design-vue/dist/reset.css'
import '/src/styles/vben.css'
import '/src/styles/resource-list.css'
import {createApp,h} from 'vue'
import {createPinia} from 'pinia'
import {createRouter,createMemoryHistory,RouterView} from 'vue-router'
import client from '/src/api/client'
import i18n from '/src/locales'
import {useAuthStore} from '/src/stores/auth'
import AppLayout from '/src/layouts/AppLayout.vue'
import SettingsView from '/src/views/SettingsView.vue'

// 合成数据只保存在当前浏览器，不连接真实 EDO 或外部服务。
const key='edo.interface-settings-preview',project='appearance-preview'
let saved=JSON.parse(sessionStorage.getItem(key)||'null')||{enabled:true,version:0}
window.edoInterfaceTrace=[]
client.defaults.adapter=async config=>{
 const path=config.url,method=config.method||'get';window.edoInterfaceTrace.push({path,method});let data
 if(path==='/settings/github-logo'){
  if(method==='put'){
   const body=typeof config.data==='string'?JSON.parse(config.data):config.data
   if(body.expected_version!==saved.version)throw Error('合成设置版本冲突')
   saved={enabled:body.enabled,version:saved.version+1};sessionStorage.setItem(key,JSON.stringify(saved))
  }
  data={...saved}
 }else if(method==='get')data=({'/projects':{projects:[{id:project,name:'界面验收项目',is_active:true}]},'/assistant/status':{enabled:false,configured:false},'/settings/external-git-webhook':{enabled:false,version:0,providers:[],events:[]},'/settings/login-lockout':{enabled:false,version:0,max_failures:5,window_seconds:300},'/settings/runtime-logging':{level:'info',http_access_enabled:false,version:0},'/settings/log-retention':{enabled:false,version:0,pipeline_log_days:30,audit_log_days:90},'/settings/runtime-directories':{workspace_directory:'/tmp/workspace',build_directory:'/tmp/build',cache_directory:'/tmp/cache',local_artifact_directory:'/tmp/artifacts',version:0}})[path]
 if(!data)throw Error(`未登记的合成接口 ${method} ${path}`)
 return {data,status:200,statusText:'OK',headers:{},config}
}
window.fetch=()=>Promise.reject(Error('合成页面不访问真实 API'))
window.WebSocket=class extends EventTarget{constructor(){super();throw Error('合成页面不连接真实 WebSocket')}}
localStorage.setItem('edo.project',project)
const pinia=createPinia(),auth=useAuthStore(pinia)
auth.user={id:'appearance-operator',username:'preview',nickname:'验收账户',department_id:'preview-department',project_id:project,is_superuser:false,permissions:['config.read','config.update','project.read']};auth.loaded=true;auth.authorizationState='ready';auth.startPermissionSync=()=>()=>{};auth.refreshPermissions=async()=>{}
const router=createRouter({history:createMemoryHistory(),routes:[{path:'/',component:AppLayout,children:[{path:'settings',component:SettingsView}]}]})
await router.push('/settings?section=interface')
createApp({render:()=>h(RouterView)}).use(pinia).use(i18n).use(router).mount('#root')
