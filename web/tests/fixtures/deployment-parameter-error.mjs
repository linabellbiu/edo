import 'ant-design-vue/dist/reset.css'
import '/src/styles/vben.css'
import {createApp,h,reactive,computed} from 'vue'
import {createPinia} from 'pinia'
import {ConfigProvider,Form,Button,theme} from 'ant-design-vue'
import client from '/src/api/client'
import i18n from '/src/locales'
import {useAuthStore} from '/src/stores/auth'
import {usePreferencesStore} from '/src/stores/preferences'
import PipelineDeployFields from '/src/components/PipelineDeployFields.vue'

// 仅使用合成配置演示真实表单，不连接或修改运行中的 EDO。
const trace=[];window.edoParameterRequests=trace
client.defaults.adapter=async config=>{
 trace.push({method:config.method,path:config.url})
 if(config.method!=='get')throw new Error('验收夹具不写入 API')
 const data=({'/environments':{environments:[]},'/hosts':{hosts:[]},'/docker/endpoints':{endpoints:[]},'/image-registries':{image_registries:[]},'/delivery-variables':{variables:[{id:'configuration',name:'生产环境配置',kind:'config_env',version:1,project_id:'parameter-preview',is_active:true}]},'/settings/builtin-variables':{variables:[]}})[config.url]
 if(!data)throw new Error('未登记的合成接口')
 return {data,status:200,statusText:'OK',headers:{},config}
}
window.fetch=()=>Promise.reject(new Error('验收夹具不访问真实 API'))
window.WebSocket=class{constructor(){throw new Error('验收夹具不访问真实 WebSocket')}}
const node=reactive({id:'parameter-preview',name:'Docker Compose',type:'deploy',config:{compose_source:{kind:'repository',path:'docker-compose.yaml'},inline_deployment_plan:{name:'Docker Compose',kind:'compose_stack',timeout_seconds:600,deployment_target:{platform:'ssh',host_ids:[]},execution_config:{deployment_input_version:1,deployment_inputs:[],deployment_parameters:'--env-file .env',compose:{tag_variable:'${EDO_IMAGE_TAG}'}}}}})
localStorage.setItem('edo.project','parameter-preview')
const pinia=createPinia(),auth=useAuthStore(pinia),preferences=usePreferencesStore(pinia)
auth.user={id:'preview',project_id:'parameter-preview',permissions:['workflow.read','workflow.update','delivery_variable.read']};auth.loaded=true;auth.authorizationState='ready'
createApp({setup(){const config=computed(()=>node.config.inline_deployment_plan.execution_config);return()=>h(ConfigProvider,{theme:{algorithm:preferences.theme==='dark'?theme.darkAlgorithm:theme.defaultAlgorithm}},{default:()=>h('main',{style:'padding:24px;max-width:900px;margin:auto'},[
 h('h1',{},'部署参数校验验收'),
 h(Button,{onClick:()=>preferences.toggleTheme()},()=> '切换主题'),
 h(Form,{layout:'vertical'},{default:()=>h(PipelineDeployFields,{node,disabled:false,buildNodes:[],applicationName:'演示应用',onChange:patch=>Object.assign(node.config,patch)})}),
 h(Button,{onClick:()=>{config.value.deployment_inputs=[{kind:'configuration',configuration_id:'configuration',path:'.env'}]}},()=> '补充 .env 文件'),
 h(Button,{onClick:()=>{config.value.deployment_inputs=[];config.value.deployment_parameters='--env-file .env'}},()=> '恢复缺失文件'),
 h(Button,{onClick:()=>{config.value.deployment_parameters=''}},()=> '移除参数')
 ])})}}).use(pinia).use(i18n).mount('#root')
