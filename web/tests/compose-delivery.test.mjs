import assert from 'node:assert/strict'
import {test} from 'node:test'
import * as vue from 'vue'
import * as yaml from 'yaml'
import {loadFrontend} from './acceptanceHarness.mjs'

const yamlModule=loadFrontend('../src/utils/kubernetesYaml.ts', {yaml})
const forms=loadFrontend('../src/utils/composeDelivery.ts', {'@/utils/kubernetesYaml':yamlModule,'@/utils/variableReference':loadFrontend('../src/utils/variableReference.ts')})
const settle=async()=>{await vue.nextTick();await new Promise(resolve=>setImmediate(resolve))}

function harness(post){
 const requests=[],events=[],notifications=[],unmounts=[]
 const storage={project:'project-a'}
 const auth=vue.reactive({user:{id:'operator',project_id:'project-a',permissions:['workflow.create','deployment_plan.create']},canAny(codes){return codes.some(code=>this.user.permissions.includes(code))}})
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
 Object.assign(state.composeForm.value,{compose_yaml:'name: another-stack\nservices:\n  web:\n    image: registry.example.com/engine:${EDO_IMAGE_TAG}\n  jobs:\n    image: registry.example.com/engine:${EDO_IMAGE_TAG}\n',services:[{service:'web',build_plan_id:'build-one',image_name:'engine'},{service:'jobs',build_plan_id:'build-one',image_name:'engine'}]})
 state.composeFields.value={validate:()=>Object.keys(forms.composeDeliveryErrors(state.composeForm.value)).length===0}
}

test('Compose 页面保存内联正文、Tag 和服务映射，不要求额外汇总阶段',async t=>{
 const h=harness();t.after(h.stop);await h.state.load();fill(h.state);await h.state.create()
 assert.equal(h.requests.length,1)
 const request=h.requests[0]
 assert.equal(request.path,'/applications/application-a/compose-delivery-workflows')
 assert.equal(request.body.project_name,undefined)
 assert.match(request.body.compose_yaml,/name: another-stack/)
 assert.match(request.body.compose_yaml,/engine:\$\{EDO_IMAGE_TAG\}/)
 assert.equal(request.body.image_tag,'${EDO_GIT_TAG}')
 assert.equal(request.body.compose_file,undefined)
 assert.equal(request.body.env_file,undefined)
 assert.equal(request.body.target.working_directory,'')
 assert.equal(request.body.services.length,2)
 assert.equal(request.options.headers['X-EDO-Project-ID'],'project-a')
 assert.equal(h.events.filter(event=>event[0]==='created').length,1)
})

test('重复服务和不安全健康地址在提交前逐字段提示',()=>{
 const value=forms.newComposeDeliveryForm();value.compose_yaml='services:\n  web:\n    image: registry.example.com/web:${EDO_IMAGE_TAG}\n  jobs:\n    image: registry.example.com/jobs:${EDO_IMAGE_TAG}\n';value.services=[{service:'web',build_plan_id:'one',image_name:'web'},{service:'web',build_plan_id:'two',image_name:'jobs'}];value.health_url='https://user:password@example.com/health'
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
 assert.equal(h.state.composeForm.value.project_name,undefined)
 assert.deepEqual(h.notifications,[])
})

test('缺少流水线与部署方案创建权限不能通过向导创建草稿',async t=>{
 const h=harness();t.after(h.stop);h.auth.user.permissions=[];await h.state.load();fill(h.state);await h.state.create()
 assert.deepEqual(h.requests,[])
})

 test('镜像 Tag 默认引用当前流水线产物，裸变量名不能通过输入校验',()=>{
 const form=forms.newComposeDeliveryForm();assert.equal(form.tag_variable,'${EDO_IMAGE_TAG}')
 form.tag_variable='EDO_IMAGE_TAG';assert.ok(forms.composeDeliveryErrors(form).tag_variable)
 for(const value of ['${EDO_COMMIT_SHA}','${EDO_APP_CONFIG_PATH}','${EDO_IMAGE}','${EDO_GOARCH}']){
  form.tag_variable=value;assert.ok(forms.composeDeliveryErrors(form).tag_variable)
 }
 for(const value of ['${EDO_IMAGE_TAG}','${IMAGE_TAG}','${RELEASE_TAG}']){
  form.tag_variable=value;assert.equal(forms.composeDeliveryErrors(form).tag_variable,undefined)
 }
 })
