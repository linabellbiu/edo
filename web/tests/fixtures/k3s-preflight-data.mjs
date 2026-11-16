import { AxiosError, CanceledError } from 'axios'

export const preflightPreviewScenarios=['passed','warning','failed','delay','denied','sudo','centos','image-error','image-empty']
export const preflightPreviewHostID='11111111-1111-4111-8111-111111111111'
const permissions=['cluster.read','cluster.create','cluster.execute','cluster.update','kubernetes.node.create']
const clone=value=>JSON.parse(JSON.stringify(value))
export function createPreflightPreview(scenario='passed',{calls=[],delay=30_000,now=()=>Date.now(),allowK3dPlan=false}={}){
  if(!preflightPreviewScenarios.includes(scenario))scenario='passed'
  const hosts=[{id:preflightPreviewHostID,name:'前端测试主机',address:'192.0.2.24',mode:'ssh',ssh_port:22,ssh_username:'preview',ssh_auth_type:'password',ssh_host_key_fingerprint:'SHA256:fixture-not-used',architecture:'amd64',environment_ids:[],is_builtin:false,is_active:true,capabilities:[{kind:'ssh',status:'ready'}],credential_configured:true,use_sudo:!['sudo','centos'].includes(scenario)}]
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
    const privilege=['sudo','centos'].includes(scenario)?{distribution:scenario==='centos'?'CentOS 9':'Ubuntu 24.04',account:'standard',sudo:scenario==='centos'?'not_installed':'passwordless',sudo_enabled:false,ready:false,message:scenario==='centos'?'当前使用普通账号，主机未安装 sudo，暂时无法安装 K3s。':'当前账号可以免密使用 sudo，但尚未允许 EDO 在安装时使用该权限。',guidance:[...(scenario==='centos'?['请联系主机管理员安装 sudo，通过 wheel 组或 sudoers 授权当前账号，或改用 root 账号连接。']:[]),'打开这台主机的配置，勾选“使用 sudo 执行 Docker / 安装操作”。','点击“测试连接”，成功后保存，再返回这里重新检测。']}:undefined
    if(privilege)checks.push({id:'privilege',name:'安装所需的管理员权限',status:'failed',message:privilege.message})
    const passed=!checks.some(check=>check.status==='failed')
    return {checked_at:new Date(now()).toISOString(),passed,checks:[],hosts:selected.map(host=>({host_id:host.id,host_name:'前端测试主机',hostname:'fixture-host',role:host.role,passed,checks,...(privilege?{privilege}:{})}))}
  }
  async function adapter(config){
    if(config.signal?.aborted)throw new CanceledError('前端检测已取消',config)
    const method=String(config.method??'get').toLowerCase(),path=String(config.url??'').split('?')[0]
    calls.push({method,path,outcome:'requested'});const call=calls[calls.length-1]
    if(!path.startsWith('/')||path.startsWith('//')||path.includes('..')){call.outcome='rejected';return reject(config,'前端夹具不连接外部或未知接口')}
    if(method==='get'&&path==='/hosts'){call.outcome='fixture';return response(config,{hosts})}
    if(method==='get'&&path==='/kubernetes/k3s/options'){call.outcome='fixture';return response(config,catalog)}
    if(method==='get'&&path==='/kubernetes/k3s/installations'){call.outcome='fixture';return response(config,{installations:[]})}
    if(method==='get'&&path==='/kubernetes/k3s/k3d/images'){
      if(scenario==='image-error'){call.outcome='rejected';return reject(config,'前端夹具：镜像版本读取失败，请重试、切换来源或手动填写 K3s 镜像地址',502)}
      const source=config.params?.source??'domestic',repository=source==='domestic'?'swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/rancher/k3s':'docker.io/rancher/k3s'
      const versions=source==='domestic'?[{version:'v1.37.1+k3s1',tag:'v1.37.1-k3s1',image:`${repository}:v1.37.1-k3s1`,platforms:['linux/amd64']},{version:'v1.29.6+k3s2',tag:'v1.29.6-k3s2-linuxarm64',image:`${repository}:v1.29.6-k3s2-linuxarm64`,platforms:['linux/arm64']}]:[{version:'v1.37.2+k3s1',tag:'v1.37.2-k3s1',image:`${repository}:v1.37.2-k3s1`,platforms:['linux/amd64','linux/arm64']}]
      call.outcome='fixture';return response(config,{source,repository,checked_at:new Date(now()).toISOString(),versions:scenario==='image-empty'?[]:versions})
    }
    if(allowK3dPlan&&method==='post'&&path==='/kubernetes/k3s/plans'){
      const payload=typeof config.data==='string'?JSON.parse(config.data):config.data
      if(payload.mode!=='k3d'||payload.server_host_id!==preflightPreviewHostID){call.outcome='rejected';return reject(config,'前端夹具仅提供所选测试主机的 k3d 内存计划')}
      call.outcome='fixture';return response(config,{id:'fixture-k3d-images-plan',mode:'k3d',action:'create',name:payload.name,status:'planned',endpoint:payload.endpoint,k3d:payload.k3d,version:payload.k3d?.image?.split(':').at(-1)?.replace('-k3s','+k3s')??'',expires_at:new Date(now()+300000).toISOString(),hosts:[{id:preflightPreviewHostID,name:'前端测试主机',address:'192.0.2.24',role:'host'}],warnings:[],commands:[],preflight:report(payload),server_config:'',agent_config:'',installer_config:''})
    }
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
  return {scenario,allowK3dPlan,hosts,permissions:scenario==='denied'?['cluster.read']:[...permissions],calls,adapter}
}
