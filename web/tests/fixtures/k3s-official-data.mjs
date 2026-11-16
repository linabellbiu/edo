import {AxiosError} from 'axios'
import {parseDocument} from 'yaml'
import {createPreflightPreview,preflightPreviewHostID} from './k3s-preflight-data.mjs'

// 只模拟确认与失败展示，不执行远端安装，不透传任何请求。
export function createOfficialPreview({calls=[],progress=false,failureLog=false,review=false}={}){
  const base=createPreflightPreview('passed',{calls}),planID='official-fixture-plan'
  if(review)base.hosts.push({...base.hosts[0],id:'22222222-2222-4222-8222-222222222222',name:'前端验收工作节点',address:'192.0.2.25'})
  let plan,installation
  const response=(config,data)=>({config,data:JSON.parse(JSON.stringify(data)),status:200,statusText:'OK',headers:{}})
  const reject=config=>{throw new AxiosError('官方流程夹具拒绝未登记请求','FIXTURE_REJECTED',config,undefined,{status:400,data:{message:'官方流程夹具拒绝未登记请求'}})}
  async function adapter(config){
    const method=String(config.method??'get').toLowerCase(),path=config.url
    if(method==='get'&&path==='/kubernetes/k3s/installations'&&config.params?.status==='failed'){
      calls.push({method,path,params:config.params,outcome:'fixture'})
      const last=installation?.status==='failed'?installation:failureLog?{id:'old-failed-task',action:'create',status:'failed',cluster_id:'removed-cluster',cluster:{id:'removed-cluster',name:'旧集群'},logs:'前端夹具：上一次失败日志，未连接真实主机。\n14:10:45 本地存储组件就绪校验超时。'}:undefined
      return response(config,{installations:last?[last]:[]})
    }
    if(method==='get'&&['/hosts','/kubernetes/k3s/options','/kubernetes/k3s/installations'].includes(path))return base.adapter(config)
    calls.push({method,path,outcome:'fixture'});
    const body=typeof config.data==='string'?JSON.parse(config.data):config.data
    if(method==='post'&&path==='/kubernetes/k3s/plans'&&body.mode==='native'&&body.action==='create'&&body.server_host_id===preflightPreviewHostID&&['official','official_cn'].includes(body.download_source)){
      const domestic=body.download_source==='official_cn'
      const sources={download_source:body.download_source,installer_url:domestic?'https://rancher-mirror.rancher.cn/k3s/k3s-install.sh':'https://get.k3s.io',artifact_url:domestic?'https://rancher-mirror.rancher.cn/k3s':'https://github.com/k3s-io/k3s/releases/download',...(domestic?{mirror:'cn'}:{}),system_image_registry:body.system_image_registry||''}
      const registryConfig=body.system_image_registry?'system-default-registry: '+JSON.stringify(body.system_image_registry)+'\n':''
      plan={id:planID,action:'create',mode:'native',name:body.name,version:'v1.37.1+k3s1',endpoint:'https://192.0.2.24:6443',expires_at:new Date(Date.now()+300000).toISOString(),hosts:[{id:preflightPreviewHostID,name:'前端测试主机',address:'192.0.2.24',role:'server'}],sources,server_config:body.datastore==='etcd'?'cluster-init: true':'node-label:\n  - edo.dev/fixture=true',commands:[`curl -sfL ${sources.installer_url} -o /tmp/install.sh\n${domestic?'INSTALL_K3S_MIRROR=cn ':''}sh /tmp/install.sh server`],warnings:[]}
      plan.server_config=registryConfig+plan.server_config
      if(review){
        const serverOptions={...body.server_options,...parseDocument(body.overrides||'').toJS({maxAliasCount:0})?.server}
        if(serverOptions['disable-agent']===true)plan.server_config+='\ndisable-agent: true\n'
        plan.hosts[0].name='前端验收控制平面主机-用于检查长名称与完整配置'
        plan.hosts[0].config=registryConfig+(body.datastore==='etcd'?'cluster-init: true\n':'')+(serverOptions['disable-agent']===true?'disable-agent: true\n':'')+'node-label:\n  - edo.dev/installation=fixture-installation\n  - edo.dev/host='+preflightPreviewHostID+'\ntls-san:\n  - 192.0.2.24\n'
        for(const id of body.agent_host_ids??[]){
          const worker=base.hosts.find(host=>host.id===id&&host.id!==preflightPreviewHostID)
          if(!worker)return reject(config)
          plan.hosts.push({id:worker.id,name:worker.name,address:worker.address,role:'agent',config:'node-label:\n  - edo.dev/role=worker\n'})
        }
      }
      return response(config,plan)
    }
    if(method==='post'&&path==='/kubernetes/k3s/installations'&&plan&&body.plan_id===planID){
      installation={id:planID,action:'create',mode:'native',name:plan.name,sources:plan.sources,status:'pending',hosts:plan.hosts,logs:'前端夹具：模拟任务受理，未执行任何主机操作。'}
      return response(config,installation)
    }
    if(method==='get'&&path===`/kubernetes/k3s/installations/${planID}`&&installation){
      if(progress){const now=Date.now();return response(config,{...installation,status:'running',started_at:new Date(now-180000).toISOString(),hosts:plan.hosts.map(host=>({...host,status:'installing',stage:'installer_download',stage_started_at:new Date(now-117000).toISOString(),progress_updated_at:new Date(now).toISOString(),message:'官方安装器下载文件仍在执行，已持续 117 秒'})),logs:'[前端测试主机] [INFO] Downloading hash https://rancher-mirror.rancher.cn/k3s/v1.37.1+k3s1/sha256sum-amd64.txt\n[前端测试主机] [INFO] Downloading binary https://rancher-mirror.rancher.cn/k3s/v1.37.1+k3s1/k3s\n[前端测试主机] [INFO] systemd: Starting k3s\n[前端测试主机] [k3s] time=2026-11-16T10:00:00Z level=info msg="Starting k3s v1.37.1+k3s1"'})}
      installation={...installation,status:'failed',error:'前端夹具：模拟官方安装脚本失败（退出码 17）。',logs:'前端夹具：未执行远端安装；模拟失败，不显示成功。'}
      return response(config,installation)
    }
    calls[calls.length-1].outcome='rejected';return reject(config)
  }
  return {...base,adapter}
}
