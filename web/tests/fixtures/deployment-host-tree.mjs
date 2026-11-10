import 'ant-design-vue/dist/reset.css'
import '/src/styles/vben.css'
import '/src/styles/resource-list.css'
import {createApp,h,computed,ref} from 'vue'
import {createPinia} from 'pinia'
import {createRouter,createMemoryHistory} from 'vue-router'
import {ConfigProvider,Form,Button,theme} from 'ant-design-vue'
import client from '/src/api/client'
import i18n from '/src/locales'
import {useAuthStore} from '/src/stores/auth'
import {usePreferencesStore} from '/src/stores/preferences'
import DeploymentPlanView from '/src/views/DeploymentPlanView.vue'
import PipelineDeployFields from '/src/components/PipelineDeployFields.vue'
// 隔离合成接口，仅保存当前浏览器内的数据，不连接生产或执行部署。
const project='host-tree-preview'
const host=(id,name,environment_ids)=>({id,name,environment_ids,mode:'ssh',address:'192.0.2.'+({a:10,b:11,c:12,d:13}[id]),ssh_port:22,is_active:true,capabilities:[{kind:'docker',runtime_id:'runtime-'+id,status:'ready'},{kind:'ssh',status:'ready'}]})
const hosts=[host('a','API 主机',['prod','staging']),host('b','任务主机',['prod']),host('c','预发布主机',['staging']),host('d','未分组执行主机',[])]
const groups=[{id:'root',name:'研发',is_active:true,hosts:[]},{id:'prod',parent_id:'root',name:'生产',is_active:true,hosts:hosts.slice(0,2)},{id:'staging',parent_id:'root',name:'预发布',is_active:true,hosts:[hosts[0],hosts[2]]}]
if(sessionStorage.getItem('edo.host-tree-moved')==='yes'){hosts[0].environment_ids=['staging'];groups.find(group=>group.id==='prod').hosts=hosts.filter(host=>host.environment_ids.includes('prod'))}
const original={id:'plan',name:'树形主机选择验收',kind:'compose_stack',is_active:true,timeout_seconds:600,compose_yaml:'services:\n  api:\n    image: example/api:${EDO_IMAGE_TAG}\n',deployment_target_id:'target',deployment_target:{id:'target',platform:'ssh',environment_id:'prod',host_ids:['a','b'],rollout_timeout:600},execution_config:{deployment_registry_id:'registry',deployment_input_version:1,deployment_inputs:[],compose:{project_name:'preview',tag_variable:'${EDO_IMAGE_TAG}',services:[]}}}
const clone=value=>JSON.parse(JSON.stringify(value));let saved=JSON.parse(sessionStorage.getItem('edo.host-tree-plan')||'null')||clone(original)
const trace=[];window.edoHostTreeTrace=trace
client.defaults.adapter=async config=>{
 const path=config.url,method=config.method||'get';let data
 const body=typeof config.data==='string'?JSON.parse(config.data):config.data;trace.push({method,path,body})
 if(method==='get')data=({'/deployment-plans':{deployment_plans:[clone(saved)]},'/environments':{environments:groups},'/hosts':{hosts},'/docker/endpoints':{endpoints:hosts.map(host=>({id:'runtime-'+host.id,host_id:host.id,is_active:true}))},'/kubernetes/clusters':{clusters:[]},'/delivery-variables':{variables:[]},'/build-plans':{build_plans:[]},'/image-registries':{image_registries:[{id:'registry',name:'验收仓库',project_id:project,is_active:true}]}})[path]
 if(method==='put'&&path==='/deployment-plans/plan'){saved={...clone(body),id:'plan',is_active:true,deployment_target_id:'target',deployment_target:{...body.deployment_target,id:'target'}};sessionStorage.setItem('edo.host-tree-plan',JSON.stringify(saved));data={deployment_plan:clone(saved)}}
 if(method==='post'&&path.endsWith('/compose-services'))data={services:['api']}
 if(!data)throw new Error('未登记的隔离接口 '+method+' '+path)
 return {data,status:200,statusText:'OK',headers:{},config}
}
localStorage.setItem('edo.project',project)
const pinia=createPinia(),auth=useAuthStore(pinia),preferences=usePreferencesStore(pinia)
auth.user={id:'preview',project_id:project,department_id:'department',is_superuser:true,permissions:[]};auth.loaded=true;auth.authorizationState='ready'
const router=createRouter({history:createMemoryHistory(),routes:[{path:'/deployment-plans',component:DeploymentPlanView}]});await router.push('/deployment-plans?plan=plan&edit=1')
createApp({setup(){const inline=ref(false),node=ref({id:'deploy',name:'Docker Compose',type:'deploy',config:{inline_deployment_plan:clone(original)}});const tokens=computed(()=>({algorithm:preferences.theme==='dark'?theme.darkAlgorithm:theme.defaultAlgorithm}));return()=>h(ConfigProvider,{theme:tokens.value},{default:()=>[h(Button,{onClick:()=>preferences.toggleTheme()},()=> '切换主题'),h(Button,{onClick:()=>{sessionStorage.setItem('edo.host-tree-moved','yes');location.reload()}},()=> '模拟 API 主机换组'),h(Button,{onClick:()=>inline.value=!inline.value},()=>inline.value?'查看方案库':'查看流水线内配置'),inline.value?h(Form,{layout:'vertical',style:'max-width:680px;padding:16px;margin:auto'},()=>h(PipelineDeployFields,{node:node.value,disabled:false,buildNodes:[],onChange:patch=>Object.assign(node.value.config,patch)})):h(DeploymentPlanView)]})}}).use(pinia).use(i18n).use(router).mount('#root')
