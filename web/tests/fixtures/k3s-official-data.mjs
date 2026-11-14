import {AxiosError} from 'axios'
import {createPreflightPreview,preflightPreviewHostID} from './k3s-preflight-data.mjs'

// 只模拟确认与失败展示，不执行远端安装，不透传任何请求。
export function createOfficialPreview({calls=[],progress=false}={}){
  const base=createPreflightPreview('passed',{calls}),planID='official-fixture-plan'
  let plan,installation
  const response=(config,data)=>({config,data:JSON.parse(JSON.stringify(data)),status:200,statusText:'OK',headers:{}})
  const reject=config=>{throw new AxiosError('官方流程夹具拒绝未登记请求','FIXTURE_REJECTED',config,undefined,{status:400,data:{message:'官方流程夹具拒绝未登记请求'}})}
  async function adapter(config){
    const method=String(config.method??'get').toLowerCase(),path=config.url
    if(method==='get'&&['/hosts','/kubernetes/k3s/options','/kubernetes/k3s/installations'].includes(path))return base.adapter(config)
    calls.push({method,path,outcome:'fixture'});
    const body=typeof config.data==='string'?JSON.parse(config.data):config.data
    if(method==='post'&&path==='/kubernetes/k3s/plans'&&body.mode==='native'&&body.action==='create'&&body.server_host_id===preflightPreviewHostID&&['official','official_cn'].includes(body.download_source)&&!body.system_image_registry){
      const domestic=body.download_source==='official_cn'
      const sources={download_source:body.download_source,installer_url:domestic?'https://rancher-mirror.rancher.cn/k3s/k3s-install.sh':'https://get.k3s.io',artifact_url:domestic?'https://rancher-mirror.rancher.cn/k3s':'https://github.com/k3s-io/k3s/releases/download',...(domestic?{mirror:'cn'}:{}),system_image_registry:''}
      plan={id:planID,action:'create',mode:'native',name:body.name,version:'v1.37.1+k3s1',endpoint:'https://192.0.2.24:6443',expires_at:new Date(Date.now()+300000).toISOString(),hosts:[{id:preflightPreviewHostID,name:'前端测试主机',address:'192.0.2.24',role:'server'}],sources,server_config:body.datastore==='etcd'?'cluster-init: true':'# SQLite 默认配置',commands:[`curl -sfL ${sources.installer_url} -o /tmp/install.sh\n${domestic?'INSTALL_K3S_MIRROR=cn ':''}sh /tmp/install.sh server`],warnings:[]}
      return response(config,plan)
    }
    if(method==='post'&&path==='/kubernetes/k3s/installations'&&plan&&body.plan_id===planID){
      installation={id:planID,action:'create',mode:'native',name:plan.name,sources:plan.sources,status:'pending',hosts:plan.hosts,logs:'前端夹具：模拟任务受理，未执行任何主机操作。'}
      return response(config,installation)
    }
    if(method==='get'&&path===`/kubernetes/k3s/installations/${planID}`&&installation){
      if(progress){const now=Date.now();return response(config,{...installation,status:'running',started_at:new Date(now-180000).toISOString(),hosts:plan.hosts.map(host=>({...host,status:'installing',stage:'installer_download',stage_started_at:new Date(now-117000).toISOString(),progress_updated_at:new Date(now).toISOString(),message:'官方安装器下载文件仍在执行，已持续 117 秒'})),logs:'前端夹具：模拟实际过程记录，未执行安装。\n14:00:00 vps：下载安装器\n14:00:02 vps：下载 K3s 校验文件\n14:00:04 vps：官方安装器下载文件\n14:02:01 vps：官方安装器下载文件仍在执行，已持续 117 秒'})}
      return response(config,{...installation,status:'failed',error:'前端夹具：模拟官方安装脚本失败（退出码 17）。',logs:'前端夹具：未执行远端安装；模拟失败，不显示成功。'})
    }
    calls[calls.length-1].outcome='rejected';return reject(config)
  }
  return {...base,adapter}
}
