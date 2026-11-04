export interface ComposeDeliveryForm {
  project_name: string
  compose_file: string
  env_file: string
  runtime_image: string
  health_url: string
  use_sudo: boolean
  timeout_seconds: number
  services: Array<{service:string;build_plan_id:string}>
}

export function newComposeDeliveryForm(): ComposeDeliveryForm {
  return {project_name:'',compose_file:'compose.yaml',env_file:'',runtime_image:'node:22-bookworm-slim',health_url:'',use_sudo:false,timeout_seconds:600,services:[{service:'',build_plan_id:''}]}
}

export function composeDeliveryErrors(form: ComposeDeliveryForm): Record<string,string> {
  const errors:Record<string,string> = {}
  if (!/^[a-z0-9][a-z0-9_-]{0,127}$/.test(form.project_name.trim())) errors.project_name='请输入原 Compose 项目名，使用小写字母、数字、下划线或短横线'
  for (const key of ['compose_file','env_file'] as const) {
    const value=form[key].trim()
    if ((!value&&key==='compose_file')||value&&(/\0|\r|\n/.test(value)||value.length>1024||value.split('/').includes('..')||['.','/'].includes(value))) errors[key]='请输入目标主机上有效的配置文件路径'
  }
  if (!form.runtime_image.trim()||/\s/.test(form.runtime_image)||!/(?::[^/:]+|@sha256:[a-f0-9]{64})$/.test(form.runtime_image)||form.runtime_image.endsWith(':latest')) errors.runtime_image='请填写带固定版本标签或摘要的 Node.js 运行镜像'
  if (form.timeout_seconds<60||form.timeout_seconds>3600) errors.timeout_seconds='部署超时应为 60–3600 秒'
  if (form.health_url.trim()) {
    try {const url=new URL(form.health_url);if(!['http:','https:'].includes(url.protocol)||url.username||url.password||url.search||url.hash)throw new Error()}catch{errors.health_url='请填写不含账号、密码和查询参数的 HTTP 或 HTTPS 健康检查地址'}
  }
  if (!form.services.length||form.services.length>32) errors.services='请选择 1–32 个已有服务'
  const seen=new Set<string>()
  form.services.forEach((binding,index)=>{
    const service=binding.service.trim()
    if(!/^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$/.test(service)) errors[`service_${index}`]='请输入 Compose 中的准确服务名'
    else if(seen.has(service)) errors[`service_${index}`]='同一服务不能重复配置'
    seen.add(service)
    if(!binding.build_plan_id)errors[`build_${index}`]='请选择带镜像仓库的 Dockerfile 构建方案'
  })
  return errors
}
