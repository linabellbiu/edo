import 'ant-design-vue/dist/reset.css'
import '/src/styles/vben.css'
import '/src/styles/resource-list.css'
import {createApp,reactive} from 'vue'
import {createPinia} from 'pinia'
import {AxiosError} from 'axios'
import client from '/src/api/client'
import {useAuthStore} from '/src/stores/auth'
import i18n from '/src/locales'
import K3sUninstallPreview from './K3sUninstallPreview.vue'

const calls=reactive([]),params=new URLSearchParams(location.search)
const project='uninstall-fixture-project'
localStorage.setItem('edo.project',project)
const permissions=params.has('denied')?['cluster.read']:['cluster.read','kubernetes.k3s.uninstall']
const host={id:'uninstall-fixture-host',name:'卸载验收主机',mode:'ssh',address:'192.0.2.24',ssh_port:22,ssh_username:'fixture',is_active:true,is_builtin:false,credential_configured:true,capabilities:[]}
const summary={source_installation_id:'uninstall-fixture-source',host_id:host.id,role:'server',script_path:'/usr/local/bin/k3s-uninstall.sh',service_name:'k3s',data_dir:'/var/lib/rancher/k3s'}
const target={...summary,installation_name:'原生 K3s 测试安装',status:'failed',created_at:'2026-11-14T14:00:00+08:00'}
const hosts=[{id:host.id,name:host.name,address:host.address,role:'server'}]
const plan={id:'uninstall-fixture-plan',action:'uninstall',mode:'native',hosts,uninstall:summary,expires_at:new Date(Date.now()+300_000).toISOString()}
const task={id:'uninstall-fixture-task',action:'uninstall',mode:'native',status:'pending',hosts,uninstall:summary,logs:'前端夹具：仅模拟受理，未执行任何主机卸载。'}
const response=(config,data)=>({config,data,status:200,statusText:'OK',headers:{}})
// 所有请求在内存中处理，未登记的请求直接拒绝，不连接后台或目标主机。
client.defaults.adapter=async config=>{
  const method=String(config.method??'get').toLowerCase(),path=config.url
  const body=typeof config.data==='string'?JSON.parse(config.data):config.data
  calls.push({method,path,...(body?{body}:{})})
  if(method==='get'&&path==='/kubernetes/k3s/uninstall-targets')return response(config,{targets:[target]})
  if(method==='get'&&path==='/kubernetes/k3s/installations')return response(config,{installations:[]})
  if(method==='post'&&path==='/kubernetes/k3s/uninstall/plans'&&body.installation_id===summary.source_installation_id&&body.host_id===host.id)return response(config,{...plan,expires_at:new Date(Date.now()+300_000).toISOString()})
  if(method==='post'&&path==='/kubernetes/k3s/uninstallations'&&body.plan_id===plan.id&&body.confirmation==='我已确认卸载')return response(config,task)
  if(method==='get'&&path===`/kubernetes/k3s/installations/${task.id}`)return response(config,task)
  throw new AxiosError('隔离卸载夹具拒绝未登记请求','FIXTURE_REJECTED',config,undefined,{status:400,data:{message:'隔离卸载夹具拒绝未登记请求'}})
}
const pinia=createPinia(),auth=useAuthStore(pinia)
auth.user={id:'uninstall-fixture-operator',username:'fixture',project_id:project,department_id:'uninstall-fixture-department',is_superuser:false,permissions}
auth.loaded=true;auth.authorizationState='ready'
createApp(K3sUninstallPreview,{host,calls}).use(pinia).use(i18n).mount('#root')
