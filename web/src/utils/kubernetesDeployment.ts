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
