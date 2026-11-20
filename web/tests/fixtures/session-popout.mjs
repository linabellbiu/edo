import 'ant-design-vue/dist/reset.css'
import '@xterm/xterm/css/xterm.css'
import '/src/styles/vben.css'
import '/src/styles/resource-list.css'
import '/src/styles/kubernetes.css'
import { createApp, h } from 'vue'
import { createPinia } from 'pinia'
import { createRouter, createWebHistory, RouterView } from 'vue-router'
import { ConfigProvider, theme } from 'ant-design-vue'
import client from '/src/api/client'
import { useAuthStore } from '/src/stores/auth'
import i18n from '/src/locales'
import SessionView from '/src/views/SessionView.vue'
import SessionPopoutPreview from './SessionPopoutPreview.vue'

// 夹具仅以明确 URL 打开，不加入生产入口；HTTP、WS、终端输入都在浏览器内存中模拟。
localStorage.setItem('edo.project','preview-project')
const created='2026-11-16T02:00:00Z'
const pod={apiVersion:'v1',kind:'Pod',metadata:{uid:'preview-pod-uid',name:'preview-web',namespace:'preview',creationTimestamp:created},spec:{nodeName:'preview-node',restartPolicy:'Always',containers:[{name:'web',image:'demo-web:v1',imagePullPolicy:'IfNotPresent'}]},status:{phase:'Running',podIP:'10.0.0.2',conditions:[{type:'Ready',status:'True',lastTransitionTime:created}],containerStatuses:[{name:'web',ready:true,started:true,restartCount:0,state:{running:{startedAt:created}}}]}}
const installation={id:'preview-installation',name:'preview-k3d',action:'create',status:'succeeded',started_at:created,finished_at:'2026-11-16T02:01:00Z',hosts:[],logs:['[preview] k3d cluster create preview-k3d','[preview] API 与节点就绪','[preview] 本地模拟日志，不执行安装']}
const user={id:'preview-user',username:'preview',nickname:'本地预览',project_id:'preview-project',department_id:'preview-department',is_superuser:false,permissions:['kubernetes.workload.read','cluster.read','terminal.open','host.terminal.open','pipeline_run.read','cluster.create','monitor.read']}
const pipelineLogs=[{id:1,node_id:'preview-task',stage:'build',level:'info',message:'docker build --platform linux/amd64 .',created_at:created},{id:2,node_id:'preview-task',stage:'build',level:'success',message:'模拟构建完成',created_at:created}]
const requests=[],connections=[],sent=[]
window.__sessionPreview={requests,connections,sent}
client.defaults.adapter=async config=>{
  config.signal?.throwIfAborted()
  const url=new URL(config.url,'http://preview.invalid'),method=String(config.method||'get').toLowerCase()
  requests.push({method,path:url.pathname,params:config.params})
  const response=data=>({data,status:200,statusText:'OK',headers:{},config})
  if(method!=='get')throw new Error('本地日志夹具不允许写操作')
  if(url.pathname==='/auth/me')return response({user})
  if(url.pathname==='/projects')return response({projects:[{id:'preview-project',is_active:true}]})
  if(url.pathname==='/hosts')return response({hosts:[{id:'preview-host',name:'本地主机',mode:'local',is_active:true,capabilities:[{kind:'local_exec',status:'ready'}]}]})
  if(url.pathname.endsWith('/pods/preview-web'))return response({object:pod})
  if(url.pathname.endsWith('/pods/preview-web/events'))return response({items:[]})
  if(url.pathname.endsWith('/pods/preview-web/diagnostics'))return response({uid:pod.metadata.uid,state:'ok',metrics_at:new Date().toISOString(),containers:[{name:'web',cpu:.002,memory:12*1024**2}]})
  if(url.pathname==='/pipeline-runs/preview-run/logs')return response({logs:pipelineLogs,status:'succeeded',next_after_id:2})
  if(url.pathname==='/kubernetes/k3s/installations/preview-installation')return response(installation)
  throw new Error(`本地夹具未登记只读接口：${url.pathname}`)
}
const originalFetch=window.fetch.bind(window)
window.fetch=(input,options)=>{const url=new URL(typeof input==='string'?input:input.url,location.href);if(url.origin!==location.origin||url.pathname.startsWith('/api/'))return Promise.reject(new Error('本地夹具禁止真实网络请求'));return originalFetch(input,options)}
const bytes=text=>new TextEncoder().encode(text).buffer
window.WebSocket=class PreviewSocket extends EventTarget{
  static CONNECTING=0;static OPEN=1;static CLOSING=2;static CLOSED=3
  readyState=0;binaryType='arraybuffer'
  constructor(path,protocol){
    super();this.url=String(path);this.protocol=protocol;connections.push({url:this.url,protocol})
    queueMicrotask(()=>{
      if(this.readyState!==0)return
      const url=new URL(this.url,location.href)
      if(url.origin!==location.origin.replace(/^http/,'ws')||!url.pathname.startsWith('/api/v1/')){this.close(1008);return}
      this.readyState=1;this.onopen?.(new Event('open'))
      if(protocol==='edo-auth-v1'){const heartbeat=()=>this.readyState===1&&this.onmessage?.({data:JSON.stringify({type:'heartbeat'})});heartbeat();this.heartbeat=setInterval(heartbeat,10_000);return}
      if(protocol==='edo-pipeline-logs-v1'){this.onmessage?.({data:JSON.stringify({type:'complete',status:'succeeded'})});return}
      if(!['edo-container-logs-v1','edo-terminal-v1'].includes(protocol)){this.close(1008);return}
      this.onmessage?.({data:JSON.stringify({type:'ready'})})
      this.onmessage?.({data:bytes(protocol==='edo-terminal-v1'?'EDO 模拟终端 — 不执行真实命令\r\npreview@edo:~$ ':'2026-11-16 10:00:00 INFO preview-web 服务已就绪\r\n2026-11-16 10:00:01 INFO GET /health 200\r\n')})
    })
  }
  send(value){sent.push({url:this.url,kind:typeof value==='string'?'control':'input'});if(typeof value!=='string'&&this.readyState===1)this.onmessage?.({data:bytes(new TextDecoder().decode(value))})}
  close(code=1000){if(this.readyState===3)return;clearInterval(this.heartbeat);this.readyState=3;this.onclose?.(new CloseEvent('close',{code}))}
}
const pinia=createPinia(),auth=useAuthStore(pinia)
auth.user=user;auth.loaded=true;auth.authorizationState='ready'
const router=createRouter({history:createWebHistory(),routes:[{path:'/sessions/view',component:SessionView},{path:'/:pathMatch(.*)*',component:SessionPopoutPreview,props:{pod,installation}}]})
createApp({render:()=>h(ConfigProvider,{theme:{algorithm:theme.defaultAlgorithm,token:{colorPrimary:'#5269ff',borderRadius:4,fontSize:13}}},()=>h(RouterView))}).use(pinia).use(router).use(i18n).mount('#root')
