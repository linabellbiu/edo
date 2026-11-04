import assert from 'node:assert/strict'
import {test} from 'node:test'
import * as vue from 'vue'
import {loadFrontend} from './acceptanceHarness.mjs'

const forms=loadFrontend('../src/utils/composeDelivery.ts')
const settle=async()=>{await vue.nextTick();await new Promise(resolve=>setImmediate(resolve))}

function harness(post){
 const requests=[],events=[],notifications=[],unmounts=[]
 const storage={project:'project-a'}
 const auth=vue.reactive({user:{id:'operator',project_id:'project-a',permissions:['build_plan.create']},canAny(codes){return codes.some(code=>this.user.permissions.includes(code))}})
 const client={get:async(path,options)=>({data:{deployment_plans:[],workflow_templates:[],build_plans:[],applications:[],clusters:[]}}),post:async(path,body,options)=>{requests.push({path,body:JSON.parse(JSON.stringify(body)),options});return post?post():{data:{workflow:{id:'created'}}}}}
 const props=vue.reactive({open:true,applicationId:'application-a'})
 const component=loadFrontend('../src/components/DeliveryWorkflowDrawer.vue',{
  vue:{...vue,onMounted(){},onBeforeUnmount:fn=>unmounts.push(fn)},'vue-i18n':{useI18n:()=>({t:key=>key})},
  'ant-design-vue':{message:{error:value=>notifications.push(value),success:value=>notifications.push(value)}},
  '@/api/client':{__esModule:true,default:client},'@/api/resources':{apiErrorMessage:()=> '配置失败'},
  '@/api/delivery':{deliveryKinds:['docker','compose','kubernetes','script'],getDeliveryDefaults:async()=>[],oneDeliveryTask:()=>true},
  '@/api/infrastructure':{listEnvironments:async()=>[],listHosts:async()=>[],environmentIDsOf:host=>host.environment_ids||[]},
  '@/utils/composeDelivery':forms,'@/stores/auth':{useAuthStore:()=>auth},
 },{localStorage:{getItem:()=>storage.project},window:new EventTarget()}).default
 const scope=vue.effectScope();const state=scope.run(()=>component.setup(props,{expose(){},emit:(...args)=>events.push(args)}))
 return {state,requests,events,notifications,auth,storage,props,stop(){unmounts.forEach(fn=>fn());scope.stop()}}
}

function fill(state){
 Object.assign(state.form,{kind:'compose_existing',application:'application-a',environment:'env-a',host:'host-a',directory:'/srv/team-a',name:'团队发布'})
 Object.assign(state.composeForm.value,{project_name:'another-stack',compose_file:'deployment/app.yaml',env_file:'runtime.env',services:[{service:'web',build_plan_id:'build-one'},{service:'jobs',build_plan_id:'build-one'}]})
 state.composeFields.value={validate:()=>Object.keys(forms.composeDeliveryErrors(state.composeForm.value)).length===0}
}

test('已有 Compose 页面按用户配置提交，不强制单个镜像或特定仓库路径',async t=>{
 const h=harness();t.after(h.stop);await h.state.load();fill(h.state);await h.state.create()
 assert.equal(h.requests.length,1)
 const request=h.requests[0]
 assert.equal(request.path,'/applications/application-a/compose-delivery-workflows')
 assert.equal(request.body.project_name,'another-stack')
 assert.equal(request.body.compose_file,'deployment/app.yaml')
 assert.equal(request.body.target.working_directory,'/srv/team-a')
 assert.equal(request.body.services.length,2)
 assert.equal(request.options.headers['X-EDO-Project-ID'],'project-a')
 assert.equal(h.events.filter(event=>event[0]==='created').length,1)
})

test('重复服务和不安全健康地址在提交前逐字段提示',()=>{
 const value=forms.newComposeDeliveryForm();value.project_name='custom-stack';value.services=[{service:'web',build_plan_id:'one'},{service:'web',build_plan_id:'two'}];value.health_url='https://user:password@example.com/health'
 const errors=forms.composeDeliveryErrors(value)
 assert.equal(Boolean(errors.service_1),true);assert.equal(Boolean(errors.health_url),true)
 value.services[1].service='jobs';value.health_url='http://127.0.0.1:9000/ready'
 assert.deepEqual(forms.composeDeliveryErrors(value),{})
})

test('保存中切换项目会取消请求、清空草稿，不应用旧项目迟到成功',async t=>{
 let resolve;const pending=new Promise(yes=>{resolve=yes})
 const h=harness(()=>pending);t.after(h.stop);await h.state.load();fill(h.state)
 const saving=h.state.create();await settle();h.auth.user.project_id='project-b';h.storage.project='project-b';await settle()
 resolve({data:{workflow:{id:'old-project'}}});await saving
 assert.equal(h.requests[0].options.signal.aborted,true)
 assert.equal(h.events.some(event=>event[0]==='created'),false)
 assert.equal(h.state.composeForm.value.project_name,'')
 assert.deepEqual(h.notifications,[])
})

test('缺少构建方案创建权限不能通过向导创建隐式脚本方案',async t=>{
 const h=harness();t.after(h.stop);h.auth.user.permissions=[];await h.state.load();fill(h.state);await h.state.create()
 assert.deepEqual(h.requests,[])
})
