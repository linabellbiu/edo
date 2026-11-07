import { createApp,h,ref,computed } from 'vue'
import { createPinia } from 'pinia'
import { ConfigProvider,Button,Form,theme } from 'ant-design-vue'
import 'ant-design-vue/dist/reset.css'
import '/src/styles/vben.css'
import '/src/styles/resource-list.css'
import DeliveryVariablesView from '/src/views/DeliveryVariablesView.vue'
import DeploymentConfigurationFields from '/src/components/DeploymentConfigurationFields.vue'
import ComposeContainerControlModal from '/src/components/ComposeContainerControlModal.vue'
import client from '/src/api/client'
import i18n from '/src/locales'
import {useAuthStore} from '/src/stores/auth'
// 只使用合成数据，不连接实际业务 API。
const project='configuration-preview',pinia=createPinia(),auth=useAuthStore(pinia)
localStorage.setItem('edo.project',project)
auth.user={id:'preview-user',username:'preview',project_id:project,department_id:'preview-department',is_superuser:false,permissions:['delivery_variable.read','delivery_variable.create','delivery_variable.update','delivery_variable.delete','deployment.read','deployment.execute','deployment_plan.read','deployment_plan.update','application.read','workflow.read','workflow.update','deployment.container.restart','deployment.container.recreate','delivery_variable.execute']};auth.loaded=true;auth.authorizationState='ready'
const rows=[{id:'env-config',project_id:project,name:'.prod.env',kind:'config_env',usage:'deploy',is_active:true,is_secret:true,application_id:'app',environment_id:'prod',version:2}]
client.defaults.adapter=async config=>{let data;const path=config.url,body=typeof config.data==='string'?JSON.parse(config.data):config.data
 if(path.startsWith('/deployments/')){data=config.method==='get'?{preview:{kind:'compose',action:'restart',effective_action:'recreate',configuration_changed:true,configuration_files:[{path:'.prod.env',version:2}],container_id:'a'.repeat(64),digest:'f'.repeat(64),service:'api',run_to_completion:false,containers:[{id:'a'.repeat(64),name:'api-1',image:'example/api@sha256:sample'},{id:'b'.repeat(64),name:'api-2',image:'example/api@sha256:sample'}]}}:{runtime:{deployment_id:'sample',kind:'compose',state:'running'}}}
 else if(config.method==='get'){data=({'/delivery-variables':{variables:rows.map(item=>({...item}))},'/projects':{projects:[{id:project,name:'示例项目'}]},'/applications':{applications:[{id:'app',name:'订单应用'}]},'/environments':{environments:[{id:'test',name:'测试环境'},{id:'prod',name:'生产环境'}]},'/deployment-plans':{deployment_plans:[{id:'plan',kind:'compose_stack',deployment_target:{host_id:'host',environment_id:'prod'}}]}})[path]}
 else if(path.startsWith('/delivery-variables')){if(config.method==='post'){const row={...body,id:`config-${rows.length}`,project_id:project,version:1,value:body.is_secret?undefined:body.value};rows.push(row);data={variable:row}}else if(config.method==='put'){const row=rows.find(item=>path.endsWith(item.id));Object.assign(row,body,{version:row.version+1,value:body.is_secret?undefined:body.value});data={variable:row}}}
 if(!data)throw new Error('未登记的合成接口');return {data,status:200,statusText:'OK',headers:{},config}}
window.fetch=()=>Promise.reject(new Error('验收页不连接真实 API'))
createApp({setup(){const deploymentConfig=ref({configuration_files:[{id:'env-config',path:'.prod.env'}],compose:{project_name:'example',services:[],tag_variable:'IMAGE_TAG',use_sudo:false,adopt_existing:false}}),deploymentPlan=ref({id:'plan',kind:'compose_stack',timeout_seconds:600,service_name:'api',deployment_target:{working_directory:'/srv/example',workload_name:'example-api'}}),confirmOpen=ref(false),dark=ref(false),algorithm=computed(()=>dark.value?theme.darkAlgorithm:theme.defaultAlgorithm)
 return ()=>h(ConfigProvider,{theme:{algorithm:algorithm.value}},{default:()=>h('main',{style:'padding:20px;display:grid;gap:24px;min-width:0'},[h(Button,{onClick(){dark.value=!dark.value;document.documentElement.dataset.theme=dark.value?'dark':'light'}},()=> '切换主题'),h(DeliveryVariablesView,{configurationFiles:true,applicationId:'app',applicationName:'订单应用'}),h('h2','部署阶段'),h('div',{style:'display:flex;gap:8px;flex-wrap:wrap'},['compose_stack','compose','docker'].map(kind=>h(Button,{onClick(){deploymentPlan.value={...deploymentPlan.value,kind}}},()=>({'compose_stack':'多服务 Compose','compose':'单服务 Compose','docker':'Docker'})[kind]))),h(Form,{layout:'vertical'},{default:()=>h(DeploymentConfigurationFields,{config:deploymentConfig.value,deploymentPlan:deploymentPlan.value,applicationID:'app',environmentID:'prod',disabled:false,onChange:patch=>{deploymentConfig.value={...deploymentConfig.value,...patch}}})}),h(Button,{onClick(){confirmOpen.value=true}},()=> '打开重启确认'),h(ComposeContainerControlModal,{open:confirmOpen.value,deploymentID:'sample',containerID:'a'.repeat(64),action:'restart','onUpdate:open':value=>{confirmOpen.value=value}})])})}}).use(pinia).use(i18n).mount('#app')
