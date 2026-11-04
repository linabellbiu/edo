import {readKubeYaml} from '@/utils/kubernetesYaml'
export interface ComposeDeliveryForm {
 project_name:string
 compose_yaml:string
 env_file:string
 image_tag:string
 tag_variable:string
 health_url:string
 use_sudo:boolean
 adopt_existing:boolean
 timeout_seconds:number
 services:Array<{service:string;build_plan_id:string;image_name:string}>
}
export function newComposeDeliveryForm():ComposeDeliveryForm{return {project_name:'',compose_yaml:'services:\n  app:\n    image: registry.example.com/my-app:${IMAGE_TAG}\n    restart: unless-stopped\n',env_file:'',image_tag:'${EDO_GIT_TAG}',tag_variable:'IMAGE_TAG',health_url:'',use_sudo:false,adopt_existing:true,timeout_seconds:600,services:[{service:'app',build_plan_id:'',image_name:'my-app'}]}}
export function composeDeliveryErrors(form:ComposeDeliveryForm):Record<string,string>{
 const errors:Record<string,string>={}
 if(!/^[a-z0-9][a-z0-9_-]{0,127}$/.test(form.project_name.trim()))errors.project_name='请输入 Compose 项目名，使用小写字母、数字、下划线或短横线'
 if(form.env_file&&(/\0|\r|\n/.test(form.env_file)||form.env_file.length>1024||form.env_file.split('/').includes('..')||['.','/'].includes(form.env_file)))errors.env_file='请输入有效的环境文件路径'
 if(!form.image_tag.trim()||form.image_tag.length>512)errors.image_tag='请配置构建镜像 Tag，例如 ${EDO_GIT_TAG}'
 if(!/^[A-Za-z_][A-Za-z0-9_]{0,127}$/.test(form.tag_variable)||form.tag_variable.startsWith('EDO_'))errors.tag_variable='请使用普通变量名，例如 IMAGE_TAG；不能覆盖 EDO 内置变量'
 if(form.timeout_seconds<60||form.timeout_seconds>3600)errors.timeout_seconds='部署超时应为 60–3600 秒'
 if(form.health_url.trim()){try{const url=new URL(form.health_url);if(!['http:','https:'].includes(url.protocol)||url.username||url.password||url.search||url.hash)throw new Error()}catch{errors.health_url='请填写不含账号、密码和查询参数的 HTTP 或 HTTPS 健康检查地址'}}
 const yaml=readKubeYaml(form.compose_yaml)
 if(new TextEncoder().encode(form.compose_yaml).length>128*1024)errors.compose_yaml='Compose 正文不能超过 128 KiB'
 else if(yaml.issues.length)errors.compose_yaml=`Compose YAML 无效，请检查第 ${yaml.issues[0]!.line} 行、第 ${yaml.issues[0]!.column} 列`
 const services=yaml.object?.services as Record<string,{image?:unknown;build?:unknown;extends?:unknown}>|undefined
 if(!errors.compose_yaml&&(!services||typeof services!=='object'||Array.isArray(services)))errors.compose_yaml='Compose 必须包含 services'
 if(yaml.object?.include)errors.compose_yaml='请把 Compose 正文直接保存在 EDO，不使用 include 读取主机文件'
 if(services&&Object.values(services).some(value=>value?.build!==undefined||value?.extends!==undefined))errors.compose_yaml='构建由前序任务完成，请移除 Compose 中的 build 和 extends'
 if(!form.services.length||form.services.length>32)errors.services='请配置 1–32 个服务的镜像来源'
 const seen=new Set<string>()
 form.services.forEach((binding,index)=>{
  const name=binding.service.trim()
  if(!/^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$/.test(name))errors[`service_${index}`]='请输入 Compose 中的准确服务名'
  else if(seen.has(name))errors[`service_${index}`]='同一服务不能重复配置'
  else if(services&&!services[name])errors[`service_${index}`]='服务不在当前 Compose 中'
  seen.add(name)
  if(!binding.build_plan_id)errors[`build_${index}`]='请选择带镜像仓库的 Dockerfile 构建方案'
  if(!binding.image_name.trim())errors[`image_${index}`]='请输入构建输出的镜像名称'
  if(services?.[name]&&typeof services[name]!.image==='string'&&!(services[name]!.image as string).includes('${'+form.tag_variable+'}'))errors[`service_${index}`]='对应 image 必须引用 ${'+form.tag_variable+'}'
 })
 return errors
}
