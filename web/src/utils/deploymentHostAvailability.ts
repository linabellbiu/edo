import type {InfrastructureHost} from '@/api/infrastructure'

// 候选列表展示不可用原因；是否可保存及执行仍由原目标校验决定。
export function deploymentHostUnavailableReason(host:InfrastructureHost,kind:string,requireExecution=false):string {
 if(!host.is_active)return '主机已停用'
 if(requireExecution||kind==='script'||kind==='compose_stack'){
  const capability=host.capabilities.find(item=>item.kind===(host.mode==='local'?'local_exec':'ssh'))
  if(!capability||capability.status!=='ready')return host.mode==='local'?'本地执行能力未就绪':'SSH 连接未就绪'
 }
 if(['docker','compose','compose_stack'].includes(kind)){
  const capability=host.capabilities.find(item=>item.kind==='docker')
  if(!capability)return '未配置 Docker 能力'
  if(capability.status!=='ready'||!capability.runtime_id)return 'Docker 连接未就绪'
 }
 return '所需运行连接不可用'
}
