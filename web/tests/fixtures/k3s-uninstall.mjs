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
const result=['success','failed','running','uncertain'].includes(params.get('result'))?params.get('result'):''
const includeHistory=params.get('history')==='1'
const project='uninstall-fixture-project'
localStorage.setItem('edo.project',project)
const permissions=params.has('denied')?['cluster.read']:['cluster.read','kubernetes.k3s.uninstall']
const host={id:'uninstall-fixture-host',name:'卸载验收主机',mode:'ssh',address:'192.0.2.24',ssh_port:22,ssh_username:'fixture',is_active:true,is_builtin:false,credential_configured:true,capabilities:[]}
const summary={source_installation_id:'uninstall-fixture-source',host_id:host.id,role:'server',script_path:'/usr/local/bin/k3s-uninstall.sh',service_name:'k3s',data_dir:'/var/lib/rancher/k3s'}
const target={...summary,installation_name:'原生 K3s 测试安装',status:'failed',created_at:'2026-11-14T14:00:00+08:00'}
const hosts=[{id:host.id,name:host.name,address:host.address,role:'server'}]
const plan={id:'uninstall-fixture-plan',action:'uninstall',mode:'native',hosts,uninstall:summary,expires_at:new Date(Date.now()+300_000).toISOString()}
const logTime=value=>new Intl.DateTimeFormat('zh-CN',{hour:'2-digit',minute:'2-digit',second:'2-digit',hour12:false,timeZone:'Asia/Shanghai'}).format(value)
const taskNow=Date.now(),startedAt=new Date(taskNow-2_000).toISOString(),finishedAt=new Date(taskNow).toISOString()
const currentName='本次卸载任务'
const taskState=result==='success'?'succeeded':result==='running'?'running':result?'failed':'pending'
const stage=result==='failed'?'uninstall_guard':result==='success'||result==='uncertain'?'uninstall_verify':'uninstall'
const error=result==='failed'?'卸载目标已变化，请重新核对主机和安装记录。':result==='uncertain'?'官方卸载已执行，结果尚未确认，请查看安全日志。':''
const message=result==='success'?'官方卸载已完成。':result==='running'?'正在执行官方卸载脚本。':error||'前端夹具：仅模拟受理，未执行任何主机卸载。'
const task={
  id:'uninstall-fixture-task',name:currentName,action:'uninstall',mode:'native',status:taskState,phase:stage,message,error,
  created_at:new Date(taskNow-3_000).toISOString(),updated_at:finishedAt,
  ...(result?{started_at:startedAt,...(result!=='running'?{finished_at:finishedAt}:{})}:{}),
  hosts:hosts.map(value=>({...value,status:taskState==='running'?'installing':taskState,stage,message,stage_started_at:startedAt,progress_updated_at:finishedAt})),
  uninstall:summary,
  logs:[
    logTime(taskNow-2000)+ ' 卸载验收主机：核对卸载目标与权限',
    logTime(taskNow-2000)+ ' 卸载验收主机：目标官方卸载脚本已确认',
    result==='failed'?logTime(taskNow-1000)+ ' 卸载验收主机：目标复核失败，未执行官方卸载脚本':logTime(taskNow-1000)+ ' 卸载验收主机：执行官方卸载脚本',
    result==='success'?logTime(taskNow)+ ' 卸载验收主机：官方卸载完成':result==='uncertain'?logTime(taskNow)+ ' 卸载验收主机：卸载结果未确认，主机锁已保留':result==='running'?logTime(taskNow)+ ' 卸载验收主机：等待官方卸载脚本完成':logTime(taskNow)+ ' 卸载验收主机：已保留现场',
  ],
}
const historicalStartedAt=new Date(taskNow-3_602_000).toISOString(),historicalFinishedAt=new Date(taskNow-3_600_000).toISOString()
const historical={
  ...task,id:'uninstall-fixture-history',name:'此前卸载任务',uninstall:{...summary,source_installation_id:'uninstall-fixture-earlier-source'},status:'succeeded',phase:'uninstall_verify',error:'',message:'官方卸载已完成。',
  created_at:new Date(taskNow-3_603_000).toISOString(),started_at:historicalStartedAt,finished_at:historicalFinishedAt,updated_at:historicalFinishedAt,
  hosts:hosts.map(value=>({...value,status:'succeeded',stage:'uninstall_verify',message:'官方卸载已完成。',stage_started_at:historicalStartedAt,progress_updated_at:historicalFinishedAt})),
  logs:[logTime(taskNow-3602000)+ ' 卸载验收主机：核对卸载目标与权限',logTime(taskNow-3602000)+ ' 卸载验收主机：目标官方卸载脚本已确认',logTime(taskNow-3601000)+ ' 卸载验收主机：执行官方卸载脚本',logTime(taskNow-3600000)+ ' 卸载验收主机：官方卸载完成'],
}
let submitted=false
const installationHistory=()=>[...(result||submitted?[task]:[]),...(includeHistory?[historical]:[])]
const response=(config,data)=>({config,data,status:200,statusText:'OK',headers:{}})
// 所有请求在内存中处理，未登记的请求直接拒绝，不连接后台或目标主机。
client.defaults.adapter=async config=>{
  const method=String(config.method??'get').toLowerCase(),path=config.url
  const body=typeof config.data==='string'?JSON.parse(config.data):config.data
  calls.push({method,path,...(body?{body}:{})})
  if(method==='get'&&path==='/kubernetes/k3s/uninstall-targets')return response(config,{targets:[target]})
  if(method==='get'&&path==='/kubernetes/k3s/installations')return response(config,{installations:installationHistory()})
  if(method==='post'&&path==='/kubernetes/k3s/uninstall/plans'&&body.installation_id===summary.source_installation_id&&body.host_id===host.id)return response(config,{...plan,expires_at:new Date(Date.now()+300_000).toISOString()})
  if(method==='post'&&path==='/kubernetes/k3s/uninstallations'&&body.plan_id===plan.id&&body.confirmation==='我已确认卸载'){submitted=true;return response(config,task)}
  if(method==='get'&&path===`/kubernetes/k3s/installations/${task.id}`)return response(config,task)
  if(method==='get'&&path===`/kubernetes/k3s/installations/${historical.id}`)return response(config,historical)
  throw new AxiosError('隔离卸载夹具拒绝未登记请求','FIXTURE_REJECTED',config,undefined,{status:400,data:{message:'隔离卸载夹具拒绝未登记请求'}})
}
const pinia=createPinia(),auth=useAuthStore(pinia)
auth.user={id:'uninstall-fixture-operator',username:'fixture',project_id:project,department_id:'uninstall-fixture-department',is_superuser:false,permissions}
auth.loaded=true;auth.authorizationState='ready'
createApp(K3sUninstallPreview,{host,calls,resultTaskName:result?currentName:''}).use(pinia).use(i18n).mount('#root')
