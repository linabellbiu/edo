import type {KubernetesDeploymentSettings,WorkflowNode} from '@/types/pipeline'

export function defaultKubernetesDeployment():KubernetesDeploymentSettings {
 return {template:'web',replicas:1,cpu_request:'200m',memory_request:'300Mi',cpu_limit:'1',memory_limit:'1Gi',node_port:false,ports:[{name:'http',enabled:false,port:8080,target_port:8080},{name:'tcp',enabled:false,port:8888,target_port:8888}]}
}
export function kubernetesResourceName(value:string){return value.toLowerCase().replace(/[^a-z0-9-]+/g,'-').replace(/^-+|-+$/g,'').slice(0,63).replace(/-+$/g,'')}
export function kubernetesContainerDefault(applicationName:string,build?:WorkflowNode){
 const image=(build?.config.image_name||applicationName).replaceAll('${EDO_APPLICATION_NAME}',applicationName)
 if(image.includes('${'))return ''
 return kubernetesResourceName((image.split('/').at(-1)||'').split(/[:@]/)[0]||'')
}

export function kubernetesDeploymentRuntimeError(config:KubernetesDeploymentSettings):string {
 const validArguments=(values?:string[])=>!values||(values.length<=64&&values.reduce((total,value)=>total+new TextEncoder().encode(value).length,0)<=32768&&values.every(value=>new TextEncoder().encode(value).length<=4096&&!/[\0\r\n]/.test(value)))
 if(!validArguments(config.command)||!validArguments(config.args))return '启动命令或参数无效，每行填写一个参数，最多 64 项'
 if(config.working_dir&&(new TextEncoder().encode(config.working_dir).length>4096||!config.working_dir.startsWith('/')||/[\0\r\n]/.test(config.working_dir)||config.working_dir!=='/'&&(/\/$/.test(config.working_dir)||config.working_dir.split('/').slice(1).some(value=>!value||value==='.'||value==='..'))))return '容器工作目录必须是规范的绝对路径'
 if(config.image_pull_policy&&!['Always','IfNotPresent','Never'].includes(config.image_pull_policy))return '请选择有效的镜像拉取策略'
 if(config.termination_grace_period_seconds!==undefined&&(!Number.isInteger(config.termination_grace_period_seconds)||config.termination_grace_period_seconds<0||config.termination_grace_period_seconds>3600))return '终止宽限期应为 0–3600 秒'
 if(config.strategy&&!['RollingUpdate','Recreate'].includes(config.strategy))return '请选择有效的发布策略'
 const validRollout=(value?:string)=>!value||/^\d{1,4}%?$/.test(value)&&Number(value.replace('%',''))<=(value.endsWith('%')?100:1000)
 if(!validRollout(config.max_surge)||!validRollout(config.max_unavailable)||config.strategy==='Recreate'&&(config.max_surge||config.max_unavailable)||config.max_surge&&config.max_unavailable&&Number(config.max_surge.replace('%',''))===0&&Number(config.max_unavailable.replace('%',''))===0)return '滚动发布数量或比例无效，额外副本和不可用副本不能同时为零'
 for(const probe of [config.readiness_probe,config.liveness_probe,config.startup_probe]){
  if(!probe)continue
  if(!['http','tcp'].includes(probe.kind)||!Number.isInteger(probe.port)||probe.port<1||probe.port>65535)return '健康探针端口应为 1–65535'
  if(probe.kind==='http'&&(probe.path&&(new TextEncoder().encode(probe.path).length>2048||!probe.path.startsWith('/')||/[\0\r\n]/.test(probe.path))||probe.scheme&&!['HTTP','HTTPS'].includes(probe.scheme)))return 'HTTP 探针路径必须以 / 开头，并选择 HTTP 或 HTTPS'
  if(probe.kind==='tcp'&&(probe.path||probe.scheme))return 'TCP 探针不使用 HTTP 路径或协议'
  for(const [value,max] of [[probe.initial_delay_seconds,3600],[probe.period_seconds,3600],[probe.timeout_seconds,3600],[probe.failure_threshold,100]] as Array<[number|undefined,number]>)if(value!==undefined&&(!Number.isInteger(value)||value<0||value>max))return '健康探针等待、间隔及超时应在 0–3600 秒内，失败次数应在 0–100 内'
 }
 return ''
}
