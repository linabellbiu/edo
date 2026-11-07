import { createApp,h,ref,computed } from 'vue'
import { createPinia } from 'pinia'
import { ConfigProvider,Button,theme } from 'ant-design-vue'
import 'ant-design-vue/dist/reset.css'
import '/src/styles/vben.css'
import '/src/styles/resource-list.css'
import DeliveryVariablesView from '/src/views/DeliveryVariablesView.vue'
import client from '/src/api/client'
import i18n from '/src/locales'
import {useAuthStore} from '/src/stores/auth'
// 只使用合成数据，不连接实际业务 API。
const project='configuration-preview',pinia=createPinia(),auth=useAuthStore(pinia)
localStorage.setItem('edo.project',project)
auth.user={id:'preview-user',username:'preview',project_id:project,department_id:'preview-department',is_superuser:false,permissions:['delivery_variable.read','delivery_variable.create','delivery_variable.update','delivery_variable.delete','deployment.read','deployment.execute','deployment_plan.read','deployment_plan.update','application.read','workflow.read','workflow.update']};auth.loaded=true;auth.authorizationState='ready'
const rows=[]
client.defaults.adapter=async config=>{let data;const path=config.url,body=typeof config.data==='string'?JSON.parse(config.data):config.data
 if(config.method==='get'){data=({'/delivery-variables':{variables:rows.map(item=>({...item}))},'/projects':{projects:[{id:project,name:'示例项目'}]},'/applications':{applications:[{id:'app',name:'订单应用'}]},'/environments':{environments:[{id:'test',name:'测试环境'},{id:'prod',name:'生产环境'}]},'/deployment-plans':{deployment_plans:[{id:'plan',kind:'compose_stack',deployment_target:{host_id:'host',environment_id:'prod'}}]}})[path]}
 else if(path.startsWith('/delivery-variables')){if(config.method==='post'){const row={...body,id:`config-${rows.length}`,project_id:project,version:1,value:body.is_secret?undefined:body.value};rows.push(row);data={variable:row}}else if(config.method==='put'){const row=rows.find(item=>path.endsWith(item.id));Object.assign(row,body,{version:row.version+1,value:body.is_secret?undefined:body.value});data={variable:row}}}
 if(!data)throw new Error('未登记的合成接口');return {data,status:200,statusText:'OK',headers:{},config}}
window.fetch=()=>Promise.reject(new Error('验收页不连接真实 API'))
createApp({setup(){const dark=ref(false),algorithm=computed(()=>dark.value?theme.darkAlgorithm:theme.defaultAlgorithm)
 return ()=>h(ConfigProvider,{theme:{algorithm:algorithm.value}},{default:()=>h('main',{style:'padding:20px;display:grid;gap:24px;min-width:0'},[h(Button,{onClick(){dark.value=!dark.value;document.documentElement.dataset.theme=dark.value?'dark':'light'}},()=> '切换主题'),h(DeliveryVariablesView,{configurationFiles:true,applicationId:'app',applicationName:'订单应用'})])})}}).use(pinia).use(i18n).mount('#app')
