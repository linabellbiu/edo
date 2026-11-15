export interface KubernetesEnvironmentOption {
 name:string
 kind:'value'|'configmap'|'secret'
 value?:string
 source_name?:string
 source_key?:string
}
export interface KubernetesVolumeOption {
 name:string
 kind:'pvc'|'configmap'|'secret'|'emptyDir'
 source_name?:string
 mount_path:string
 sub_path?:string
 read_only?:boolean
}
export interface KubernetesTolerationOption {
 key?:string
 operator:''|'Equal'|'Exists'
 value?:string
 effect?:'NoSchedule'|'PreferNoSchedule'|'NoExecute'
 toleration_seconds?:number
}
export interface KubernetesCommonOptions {
 labels?:Record<string,string>
 annotations?:Record<string,string>
 environment?:KubernetesEnvironmentOption[]
 volumes?:KubernetesVolumeOption[]
 node_selector?:Record<string,string>
 tolerations?:KubernetesTolerationOption[]
 image_pull_secrets?:string[]
 min_ready_seconds?:number
 revision_history_limit?:number
 progress_deadline_seconds?:number
}
export interface KubernetesCommonOptionsIssue {
 field:keyof KubernetesCommonOptions
 index?:number
 message:string
}
export type KubernetesOptionMapField='labels'|'annotations'|'node_selector'
export interface KubernetesOptionMapRow {key:string;value:string}

export const kubernetesCommonOptionsLimits={map:64,environment:64,volumes:32,tolerations:32,image_pull_secrets:16,bytes:64*1024,value_bytes:4096,rollout_seconds:86400,history:100,toleration_seconds:31536000} as const
const labelPart=/^[a-zA-Z0-9]([-a-zA-Z0-9_.]*[a-zA-Z0-9])?$/
const dnsSubdomain=/^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/
const dnsLabel=/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/
const bytes=(value:string)=>new TextEncoder().encode(value).length
const validLabelValue=(value:string)=>typeof value==='string'&&value.length<=63&&(value===''||labelPart.test(value))
export function validKubernetesOptionName(value:string):boolean {return typeof value==='string'&&value.length<=253&&dnsSubdomain.test(value)}
export function validKubernetesOptionKey(value:string):boolean {
 if(typeof value!=='string')return false
 const parts=value.split('/'),name=parts.at(-1)??''
 return parts.length<=2&&name.length<=63&&labelPart.test(name)&&(parts.length===1||validKubernetesOptionName(parts[0]!))
}
function validSourceKey(value:string):boolean {return typeof value==='string'&&value.length<=253&&/^[-._a-zA-Z0-9]+$/.test(value)&&value!=='.'&&value!=='..'&&!value.startsWith('..')}
function validEnvironmentName(value:string):boolean {return typeof value==='string'&&value.length<=253&&/^[-._a-zA-Z][-._a-zA-Z0-9]*$/.test(value)&&value!=='.'&&value!=='..'&&!value.startsWith('..')}
function validPath(value:string,absolute:boolean):boolean {
 if(typeof value!=='string'||!value||bytes(value)>4096||/[\0\r\n]/.test(value)||value.startsWith('/')!==absolute||value==='/')return false
 return !value.split('/').slice(absolute?1:0).some(part=>!part||part==='.'||part==='..')
}
const mapLabels:Record<KubernetesOptionMapField,string>={labels:'标签',annotations:'注解',node_selector:'节点标签'}

// 命名规则对应 Kubernetes 官方校验；资源存在性、权限和最终配置由服务端核验。
export function kubernetesOptionMapRowError(field:KubernetesOptionMapField,rows:KubernetesOptionMapRow[],index:number):string {
 const row=rows[index],label=mapLabels[field]
 if(!row||!validKubernetesOptionKey(row.key))return `${label}键格式无效，例如 app.kubernetes.io/name`
 if(field!=='node_selector'&&(row.key.startsWith('edo.io/')||row.key.startsWith('edo.dev/')||row.key==='app.kubernetes.io/managed-by'))return `${label}不能修改 EDO 管理的资源身份`
 if(rows.some((other,i)=>i!==index&&other.key===row.key))return `${label}键不能重复`
 if(typeof row.value!=='string')return `${label}值必须是文字`
 if(field!=='annotations'&&!validLabelValue(row.value))return `${label}值只能包含字母、数字、短横线、下划线或点，最长 63 字符`
 if(field==='annotations'&&(row.value.includes('\0')||bytes(row.value)>kubernetesCommonOptionsLimits.value_bytes))return '注解不能包含空字符，单项不能超过 4 KiB'
 return ''
}

export function kubernetesCommonOptionsIssues(options?:KubernetesCommonOptions):KubernetesCommonOptionsIssue[] {
 const issues:KubernetesCommonOptionsIssue[]=[]
 if(options===undefined)return issues
 if(!options||typeof options!=='object'||Array.isArray(options))return [{field:'labels',message:'通用部署参数格式无效'}]
 const add=(field:keyof KubernetesCommonOptions,message:string,index?:number)=>issues.push({field,...(index===undefined?{}:{index}),message})
 for(const field of ['labels','annotations','node_selector'] as const){
  const value=options[field]
  if(value===undefined)continue
  if(!value||typeof value!=='object'||Array.isArray(value)){add(field,`${mapLabels[field]}格式无效`);continue}
  const rows=Object.entries(value).map(([key,value])=>({key,value}))
  if(rows.length>kubernetesCommonOptionsLimits.map)add(field,`${mapLabels[field]}最多 ${kubernetesCommonOptionsLimits.map} 项`)
  if(rows.reduce((size,row)=>size+bytes(row.key)+bytes(String(row.value)),0)>kubernetesCommonOptionsLimits.bytes)add(field,`${mapLabels[field]}总大小不能超过 64 KiB`)
  for(let index=0;index<rows.length;index++){const message=kubernetesOptionMapRowError(field,rows,index);if(message)add(field,message,index)}
 }
 if(options.environment!==undefined){
  if(!Array.isArray(options.environment))add('environment','环境变量格式无效')
  else{
   if(options.environment.length>kubernetesCommonOptionsLimits.environment)add('environment',`环境变量最多 ${kubernetesCommonOptionsLimits.environment} 项`)
   if(options.environment.reduce((size,item)=>size+bytes(String(item?.name??''))+bytes(String(item?.value??'')),0)>kubernetesCommonOptionsLimits.bytes)add('environment','环境变量总大小不能超过 64 KiB')
   const names=new Set<string>()
   for(const [index,item]of options.environment.entries()){
    if(!item||!validEnvironmentName(item.name))add('environment','环境变量名格式无效，例如 APP_ENV',index)
    else if(names.has(item.name))add('environment','环境变量名不能重复',index)
    if(item?.name)names.add(item.name)
    if(!item||!['value','configmap','secret'].includes(item.kind)){add('environment','请选择有效的环境变量来源',index);continue}
    if(item.kind==='value'){
     if(item.value!==undefined&&(typeof item.value!=='string'||item.value.includes('\0')||bytes(item.value)>kubernetesCommonOptionsLimits.value_bytes))add('environment','环境变量值不能包含空字符，且不能超过 4 KiB',index)
     if(item.source_name||item.source_key)add('environment','直接填写的环境变量不能同时引用其他资源',index)
    }else if(!validKubernetesOptionName(item.source_name??'')||!validSourceKey(item.source_key??'')||item.value){add('environment','请填写有效的 ConfigMap / Secret 名称和键',index)}
   }
  }
 }
 if(options.volumes!==undefined){
  if(!Array.isArray(options.volumes))add('volumes','存储挂载格式无效')
  else{
   if(options.volumes.length>kubernetesCommonOptionsLimits.volumes)add('volumes',`存储挂载最多 ${kubernetesCommonOptionsLimits.volumes} 项`)
   const names=new Set<string>(),paths=new Set<string>()
   for(const [index,item]of options.volumes.entries()){
    if(!item||typeof item.name!=='string'||item.name.length>63||!dnsLabel.test(item.name))add('volumes','卷名称须为小写字母、数字或短横线，最长 63 字符',index)
    else if(names.has(item.name))add('volumes','卷名称不能重复',index)
    if(item?.name)names.add(item.name)
    if(!item||!['pvc','configmap','secret','emptyDir'].includes(item.kind)){add('volumes','请选择有效的存储来源',index);continue}
    if(item.kind==='emptyDir'&&item.source_name||item.kind!=='emptyDir'&&!validKubernetesOptionName(item.source_name??''))add('volumes','请填写有效的同命名空间资源名称',index)
    if(!validPath(item.mount_path,true))add('volumes','挂载路径必须是规范的绝对路径',index)
    else if(paths.has(item.mount_path))add('volumes','挂载路径不能重复',index)
    if(item.mount_path)paths.add(item.mount_path)
    if(item.sub_path!==undefined&&item.sub_path!==''&&!validPath(item.sub_path,false))add('volumes','subPath 必须是相对路径，不能包含 . 或 ..',index)
    if(item.read_only!==undefined&&typeof item.read_only!=='boolean')add('volumes','请选择有效的挂载读写方式',index)
   }
  }
 }
 if(options.tolerations!==undefined){
  if(!Array.isArray(options.tolerations))add('tolerations','污点容忍格式无效')
  else{
   if(options.tolerations.length>kubernetesCommonOptionsLimits.tolerations)add('tolerations',`污点容忍最多 ${kubernetesCommonOptionsLimits.tolerations} 项`)
   for(const [index,item]of options.tolerations.entries()){
    if(!item||!['','Equal','Exists'].includes(item.operator)){add('tolerations','请选择有效的容忍运算符',index);continue}
    if(item.key&&!validKubernetesOptionKey(item.key)||!item.key&&item.operator!=='Exists')add('tolerations','容忍键格式无效，空键仅支持 Exists',index)
    if(item.operator==='Exists'&&item.value||item.operator!=='Exists'&&!validLabelValue(item.value??''))add('tolerations','Exists 不填写值；Equal 使用合法标签值',index)
    if(item.effect&&!['NoSchedule','PreferNoSchedule','NoExecute'].includes(item.effect))add('tolerations','请选择有效的容忍效果',index)
    if(item.toleration_seconds!==undefined&&(item.effect!=='NoExecute'||!Number.isSafeInteger(item.toleration_seconds)||item.toleration_seconds<0||item.toleration_seconds>kubernetesCommonOptionsLimits.toleration_seconds))add('tolerations','容忍秒数仅适用于 NoExecute，范围为 0–31536000',index)
   }
  }
 }
 if(options.image_pull_secrets!==undefined){
  if(!Array.isArray(options.image_pull_secrets))add('image_pull_secrets','镜像拉取 Secret 格式无效')
  else{
   if(options.image_pull_secrets.length>kubernetesCommonOptionsLimits.image_pull_secrets)add('image_pull_secrets',`镜像拉取 Secret 最多 ${kubernetesCommonOptionsLimits.image_pull_secrets} 项`)
   const names=new Set<string>()
   for(const [index,name]of options.image_pull_secrets.entries()){
    if(!validKubernetesOptionName(name))add('image_pull_secrets','镜像拉取 Secret 名称格式无效',index)
    else if(names.has(name))add('image_pull_secrets','镜像拉取 Secret 不能重复',index)
    names.add(name)
   }
  }
 }
 for(const field of ['min_ready_seconds','revision_history_limit','progress_deadline_seconds'] as const){
  const value=options[field],min=field==='progress_deadline_seconds'?1:0,max=field==='revision_history_limit'?kubernetesCommonOptionsLimits.history:kubernetesCommonOptionsLimits.rollout_seconds
  if(value!==undefined&&(!Number.isInteger(value)||value<min||value>max))add(field,`${field==='min_ready_seconds'?'最短就绪时间':field==='revision_history_limit'?'历史版本保留数':'发布进度截止时间'}应为 ${min}–${max} 的整数`)
 }
 if((options.progress_deadline_seconds??600)<=(options.min_ready_seconds??0))add('progress_deadline_seconds','发布进度截止时间必须大于最短就绪时间，未填写时默认为 600 秒')
 return issues
}
export function kubernetesCommonOptionsError(options?:KubernetesCommonOptions):string {return kubernetesCommonOptionsIssues(options)[0]?.message??''}
