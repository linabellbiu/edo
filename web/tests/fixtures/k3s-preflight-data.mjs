import { AxiosError, CanceledError } from 'axios'

export const preflightPreviewScenarios=['passed','warning','failed','delay','denied']
export const preflightPreviewHostID='11111111-1111-4111-8111-111111111111'
const permissions=['cluster.read','cluster.create','cluster.execute','cluster.update','kubernetes.node.create']
const clone=value=>JSON.parse(JSON.stringify(value))
export function createPreflightPreview(scenario='passed',{calls=[],delay=30_000,now=()=>Date.now()}={}){
  if(!preflightPreviewScenarios.includes(scenario))scenario='passed'
  const hosts=[{id:preflightPreviewHostID,name:'前端测试主机',address:'192.0.2.24',mode:'ssh',ssh_port:22,ssh_username:'preview',ssh_auth_type:'password',ssh_host_key_fingerprint:'SHA256:fixture-not-used',architecture:'amd64',environment_ids:[],is_builtin:false,is_active:true,capabilities:[],credential_configured:true,use_sudo:true}]
  const catalog={version:'v1.37.1+k3s1',default_version:'v1.37.1+k3s1',catalog_version:'frontend-fixture',sources:[],server:[],agent:[],installer:[]}
  const response=(config,data)=>({config,data:clone(data),status:200,statusText:'OK',headers:{}})
  const reject=(config,message,status=400)=>{throw new AxiosError(message,'FIXTURE_REJECTED',config,undefined,{config,status,statusText:'Fixture rejected',headers:{},data:{message}})}
  function report(payload){
    const selected=payload.action==='join'?(payload.join_host_ids??[]).map(id=>({id,role:payload.join_role??'agent'})):[{id:payload.server_host_id,role:payload.mode==='k3d'?'host':'server'},...(payload.mode==='k3d'?[]:(payload.agent_host_ids??[]).map(id=>({id,role:'agent'})))]
    if(!selected.length||selected.some(host=>host.id!==preflightPreviewHostID))throw new Error('前端夹具未登记该主机')
    const checks=payload.mode==='k3d'?[
      {id:'docker',name:'Docker 连接',status:'passed',message:'前端夹具：模拟 Docker 可用，未连接 Docker。'},
      {id:'runc',name:'容器运行时',status:scenario==='failed'?'failed':'passed',message:scenario==='failed'?'前端夹具：模拟 runc 条件不满足，不会安装或修复。':'前端夹具：模拟运行时要求满足。'},
      {id:'memory',name:'可用资源',status:'passed',message:'前端夹具：模拟 CPU 与内存要求满足。'},
    ]:[
      {id:'system',name:'操作系统与权限',status:'passed',message:'前端夹具：模拟 Linux、systemd 和 sudo 要求满足。'},
      {id:'overlay_kernel',name:'OverlayFS 支持',status:scenario==='failed'?'failed':'passed',message:scenario==='failed'?'前端夹具：模拟 OverlayFS 条件不满足，不会安装或修复。':'前端夹具：模拟内核要求满足。'},
      {id:'cni_kernel',name:'CNI 内核能力',status:'passed',message:'前端夹具：模拟网桥与转发条件满足。'},
      {id:'cni_ports',name:'端口配置',status:'passed',message:'前端夹具：仅模拟端口要求；未探测真实网络。'},
    ]
    checks.push({id:'component_images',name:'系统组件镜像配置',status:scenario==='warning'?'warning':'passed',message:scenario==='warning'?'前端夹具：镜像来源可配置，但未实际拉取；请在真实主机检测。':'前端夹具：仅模拟配置满足，未进行实际镜像拉取。'})
    const passed=!checks.some(check=>check.status==='failed')
    return {checked_at:new Date(now()).toISOString(),passed,checks:[],hosts:selected.map(host=>({host_id:host.id,host_name:'前端测试主机',hostname:'fixture-host',role:host.role,passed,checks}))}
  }
  async function adapter(config){
    if(config.signal?.aborted)throw new CanceledError('前端检测已取消',config)
    const method=String(config.method??'get').toLowerCase(),path=String(config.url??'').split('?')[0]
    calls.push({method,path,outcome:'requested'});const call=calls[calls.length-1]
    if(!path.startsWith('/')||path.startsWith('//')||path.includes('..')){call.outcome='rejected';return reject(config,'前端夹具不连接外部或未知接口')}
    if(method==='get'&&path==='/hosts'){call.outcome='fixture';return response(config,{hosts})}
    if(method==='get'&&path==='/kubernetes/k3s/options'){call.outcome='fixture';return response(config,catalog)}
    if(method==='get'&&path==='/kubernetes/k3s/installations'){call.outcome='fixture';return response(config,{installations:[]})}
    if(method==='post'&&path==='/kubernetes/k3s/preflight'){
      if(scenario==='denied'){call.outcome='rejected';return reject(config,'前端夹具：无安装检测权限',403)}
      const payload=typeof config.data==='string'?JSON.parse(config.data):config.data
      if(scenario==='delay')await new Promise((resolve,rejectPromise)=>{
        const finish=()=>{config.signal?.removeEventListener('abort',cancel);resolve()},timer=setTimeout(finish,delay)
        const cancel=()=>{clearTimeout(timer);call.outcome='cancelled';rejectPromise(new CanceledError('前端检测已取消',config))}
        config.signal?.addEventListener('abort',cancel,{once:true})
      })
      call.outcome='fixture';return response(config,report(payload))
    }
    call.outcome='rejected'
    return reject(config,'前端夹具拒绝生成计划、安装及所有未登记请求')
  }
  return {scenario,hosts,permissions:scenario==='denied'?['cluster.read']:[...permissions],calls,adapter}
}
