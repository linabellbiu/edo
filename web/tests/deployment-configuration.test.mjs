import assert from 'node:assert/strict'
import { test } from 'node:test'
import { readFileSync } from 'node:fs'
import * as vue from 'vue'
import { loadFrontend } from './acceptanceHarness.mjs'
const previewModule=loadFrontend('../src/utils/deploymentCommandPreview.ts')

const settle=async()=>{await vue.nextTick();await new Promise(resolve=>setImmediate(resolve));await vue.nextTick()}
async function harness(get){
 const events=[],validations=[],requests=[],unmounts=[],storage={project:'project'},window=new EventTarget()
 const auth=vue.reactive({authorizationState:'ready',user:{id:'actor',project_id:'project',department_id:'department',permissions:['workflow.update','delivery_variable.read','image_registry.read','image_registry.execute']},canAny(codes){return codes.some(code=>this.user.permissions.includes(code))}})
 const props=vue.reactive({config:{configuration_files:[]},applicationID:'app',applicationName:'订单应用',environmentID:'prod',disabled:false})
 const row=(id,patch={})=>({id,name:`${id}.env`,kind:'config_env',version:1,is_active:true,project_id:'project',application_id:'app',environment_id:'prod',...patch})
 const client={get:async(path,options)=>{requests.push({path,options});return get?get(path):{data:path==='/image-registries'?{image_registries:[]}:{variables:[row('selected'),row('legacy',{kind:'env'}),row('other-project',{project_id:'another'}),row('other-app',{application_id:'another'}),row('other-env',{environment_id:'test'})]}}}}
 const component=loadFrontend('../src/components/DeploymentConfigurationFields.vue',{'vue':{...vue,onMounted(){},onBeforeUnmount:fn=>unmounts.push(fn)},'@/api/client':{__esModule:true,default:client},'@/api/resources':{apiErrorMessage:error=>error.message},'@/stores/auth':{useAuthStore:()=>auth},'@/utils/deploymentCommandPreview':previewModule},{localStorage:{getItem:()=>storage.project},window}).default
 const scope=vue.effectScope();let state
 scope.run(()=>{state=component.setup(props,{expose(){},emit:(name,patch)=>{if(name==='validation-change'){validations.push(patch);return}events.push([name,patch]);Object.assign(props.config,patch)}})});await settle()
 return {state,props,auth,storage,events,validations,requests,stop(){unmounts.forEach(fn=>fn());scope.stop()}}
}
test('通用字段校验按实际来源汇总任意错误，不依赖主机、任务名或错误关键词',async t=>{
 const h=await harness();t.after(h.stop)
 h.props.reportValidation=true
 h.props.config.deployment_input_version=1
 h.props.config.deployment_inputs=[{kind:'configuration',configuration_id:'missing',path:'../bad'},{kind:'upstream',artifact_reference:'plain',path:'output.tar.gz'}]
 h.state.error.value='配置服务暂不可用'
 h.state.optionsByRow.value={2:{images:[],tags:[],loading:false,error:'远端返回的可展示校验信息'}}
 await settle()
 const messages=h.validations.at(-1)
 assert.ok(messages.includes('配置服务暂不可用'))
 assert.ok(messages.includes('远端返回的可展示校验信息'))
 assert.ok(messages.some(value=>value.includes('原配置不可用')))
 assert.ok(messages.some(value=>value.includes('相对路径')))
 assert.ok(messages.some(value=>value.includes('变量引用')))
 assert.deepEqual(h.events,[],'汇报错误不能修改任务配置')
 h.state.error.value='';h.state.optionsByRow.value={}
 h.props.config.deployment_inputs=[]
 await settle()
 assert.deepEqual(h.validations.at(-1),[])
})

test('部署显式选择配置 UUID 与配置文件路径，配置选择不混入旧变量或其他范围',async t=>{
 const h=await harness();t.after(h.stop)
 assert.deepEqual(h.state.configurationOptions(0).map(item=>item.value),['selected'])
 h.props.config.configuration_files=[{id:'',path:''}]
 h.state.chooseConfiguration(0,'selected')
 assert.deepEqual(h.events.at(-1)[1].configuration_files,[{id:'selected',path:'selected.env'}]);assert.equal(h.props.config.deployment_input_version,1)
 assert.equal(h.requests[0].options.headers['X-EDO-Project-ID'],'project')
 assert.ok(previewModule.validConfigurationPath('config/application.yaml'));assert.ok(!previewModule.validConfigurationPath('../escape'))
 assert.ok(!previewModule.validConfigurationPath('.edo/lock'));assert.ok(!previewModule.validConfigurationPath('/srv/app/config'))
 assert.ok(!previewModule.validConfigurationPath('~/config/.prod.env'));assert.ok(!previewModule.validConfigurationPath('~/../escape'))
 assert.ok(!previewModule.validConfigurationPath('//srv/config'));assert.ok(!previewModule.validConfigurationPath('~//config'))
})
test('切项目、撤权或加载失败后不能提交保留的配置草稿',async t=>{
 for(const mutation of ['project','permission']){
  const h=await harness(mutation==='error'?()=>Promise.reject(new Error('配置读取失败')):undefined);t.after(h.stop)
  if(mutation==='project')h.storage.project='another'
  if(mutation==='permission')h.auth.user.permissions=[]
  await settle();h.state.change({configuration_files:[{id:'selected',path:'.prod.env'}]})
  assert.equal(h.events.length,0,mutation)
 }
})
test('旧范围的迟到配置响应不回填新范围候选项',async t=>{
 const responses=[];const h=await harness(()=>new Promise(yes=>{responses.push(yes)}));t.after(h.stop)
 h.storage.project='another';h.auth.user.project_id='another';await settle()
 responses[0]({data:{variables:[{id:'old',name:'old.env',kind:'config_env',version:1,is_active:true,project_id:'project'}]}});await settle()
 assert.equal(h.state.configurations.value.length,0);assert.equal(h.events.length,0)
})
test('文件来源与脚本统一保存，切换来源清除旧身份',async t=>{
 const h=await harness();t.after(h.stop)
 h.state.change({deployment_inputs:[{kind:'configuration',configuration_id:'selected',path:'runtime.env'}]})
 h.state.chooseKind(0,'registry');h.state.update(0,{registry_id:'registry',image:'team/app',tag:'v1'})
 assert.deepEqual(h.props.config.configuration_files,[])
 assert.equal(h.props.config.deployment_inputs[0].configuration_id,undefined)
 h.state.change({deployment_script:'docker run -d "${EDO_INPUT_IMAGE_1}"'})
 assert.equal(h.props.config.deployment_script,'docker run -d "${EDO_INPUT_IMAGE_1}"')
 h.state.chooseKind(0,'upstream');h.state.update(0,{artifact_node_id:'files',path:'output.tar.gz'})
 assert.equal(h.props.config.deployment_inputs[0].registry_id,undefined)
 assert.equal(h.props.config.deployment_inputs[0].artifact_node_id,'files')
})
test('脚本编辑不要求配置读取权限，删除变量输入不清空已有变量',async t=>{
 const h=await harness();t.after(h.stop);h.auth.user.permissions=['workflow.update'];await settle()
 h.state.change({deployment_script:'echo "${EDO_IMAGE_TAG}"'});assert.equal(h.props.config.deployment_script,'echo "${EDO_IMAGE_TAG}"')
 h.props.config.environment_variables={APP_ENV:'production',PORT:'8080'}
 h.state.change({deployment_script:'echo ok'})
 assert.deepEqual(h.props.config.environment_variables,{APP_ENV:'production',PORT:'8080'})
})
test('删除额外参数与预览，使用真实脚本编辑器和统一路径',()=>{
 const source=readFileSync(new URL('../src/components/DeploymentConfigurationFields.vue',import.meta.url),'utf8')
 assert.equal(source.includes('label="额外部署参数"'),false)
 assert.equal(source.includes('aria-label="部署命令预览"'),false)
 assert.ok(source.includes('部署前下载上游产出文件或制品到主机'))
 assert.ok(source.includes('DeploymentScriptEditor'))
 assert.ok(source.includes('${EDO_APP_CONFIG_PATH}/相对路径'))
})


test('上游产物输入变量引用，切换时清除旧构建身份，不请求运行产物',async t=>{
 const h=await harness();t.after(h.stop)
 h.props.config.deployment_input_version=1;h.props.config.deployment_inputs=[{kind:'upstream',artifact_node_id:'old',artifact_name:'OLD',path:'output.tar.gz'}]
 h.state.chooseUpstream(0,'${BUILD_ARTIFACT}')
 const input=h.props.config.deployment_inputs[0]
 assert.equal(input.artifact_reference,'${BUILD_ARTIFACT}');assert.equal(input.artifact_node_id,undefined);assert.equal(input.artifact_name,undefined)
 assert.equal(h.state.invalidUpstreamReference('BUILD_ARTIFACT'),true)
 assert.equal(h.state.invalidUpstreamReference('${BUILD_ARTIFACT}'),false)
 assert.ok(h.requests.every(item=>!item.path.includes('/artifacts')))
})

test('部署镜像仓库始终可见，上游仓库不再自动关联，显式选择继续保留',async t=>{
 const h=await harness();t.after(h.stop)
 h.props.deploymentPlan={kind:'compose_stack'};h.props.buildNodes=[{id:'image',type:'build',config:{inline_build:{kind:'dockerfile',image_registry_id:'upstream'}}}]
 await settle();assert.equal(h.state.managesRegistry.value,true);assert.equal(h.state.deploymentRegistry.value,undefined);assert.deepEqual(h.events,[])
 h.props.config.deployment_input_version=1;h.props.config.deployment_inputs=[{kind:'registry',registry_id:'explicit',image:'app',tag:'v1'}]
 assert.equal(h.state.deploymentRegistry.value,'explicit')
 h.props.buildNodes=[];await settle();assert.equal(h.state.deploymentRegistry.value,'explicit')
})

test('无上游构建也能选择仓库及镜像 Tag，撤权后不能修改',async t=>{
 const h=await harness(path=>({data:path==='/image-registries'?{image_registries:[{id:'registry',name:'仓库',is_active:true,project_id:'project'}]}:path.includes('/deployment-options')?{options:['app']}:{variables:[]}}));t.after(h.stop)
 h.props.deploymentPlan={kind:'docker'};h.props.buildNodes=[]
 await h.state.setDeploymentRegistry('registry')
 h.state.update(0,{image:'app',tag:'v1'})
 assert.deepEqual(h.props.config.deployment_inputs,[{kind:'registry',registry_id:'registry',image:'app',tag:'v1'}])
 h.auth.user.permissions=['workflow.update'];await settle();const count=h.events.length
 await h.state.setDeploymentRegistry('forbidden');await h.state.chooseImage(0,'forbidden');h.state.update(0,{tag:'forbidden'});assert.equal(h.events.length,count)
})

test('部署仓库必须由用户选择，保存往返不随上游变化；只读配置不补写',async t=>{
 for(const kind of ['docker','compose','compose_stack']){
  const h=await harness(path=>({data:path==='/image-registries'?{image_registries:[{id:'upstream',name:'上游仓库',is_active:true,project_id:'project'}]}:{variables:[]}}));t.after(h.stop)
  h.props.deploymentPlan={kind};h.props.buildNodes=[{id:'build',type:'build',config:{inline_build:{image_registry_id:'upstream'}}}]
  const persisted={};await settle()
  assert.equal(h.state.deploymentRegistry.value,undefined)
  assert.equal(h.props.config.deployment_registry_id,undefined)
  assert.deepEqual(h.events,[])
  assert.ok(h.requests.every(item=>['/delivery-variables','/image-registries'].includes(item.path)))
  await h.state.setDeploymentRegistry('upstream')
  assert.equal(h.props.config.deployment_registry_id,'upstream')
  assert.deepEqual(persisted,{},'页面打开不能修改已保存记录')
  if(kind!=='docker')assert.equal(h.props.config.deployment_inputs?.length||0,0,'Compose 仓库选择不应生成多余镜像来源')
  Object.assign(persisted,JSON.parse(JSON.stringify(h.props.config)))
  h.props.config=persisted;h.props.buildNodes[0].config.inline_build.image_registry_id='another';await settle()
  assert.equal(h.state.deploymentRegistry.value,'upstream')
  h.props.disabled=true;h.props.config={};h.events.length=0;await settle()
  assert.equal(h.state.deploymentRegistry.value,undefined);assert.deepEqual(h.events,[])
 }
})

test('仓库不可用或没有仓库权限时不预填；显式已存选择不被上游覆盖',async t=>{
 const h=await harness();t.after(h.stop)
 h.props.deploymentPlan={kind:'docker'};h.props.buildNodes=[{type:'build',config:{inline_build:{image_registry_id:'unavailable'}}}];await settle()
 assert.equal(h.props.config.deployment_registry_id,undefined);assert.deepEqual(h.events,[])
 h.props.config.deployment_registry_id='saved';await settle()
 assert.equal(h.state.deploymentRegistry.value,'saved');assert.deepEqual(h.events,[])
 h.auth.user.permissions=['workflow.update'];h.props.config={};await settle()
 assert.equal(h.props.config.deployment_registry_id,undefined);assert.deepEqual(h.events,[])
})

test('Kubernetes 直接保存仓库镜像和 Tag，不读取配置文件或依赖上游构建',async t=>{
 const h=await harness(path=>({data:path==='/image-registries'?{image_registries:[{id:'registry',name:'部署仓库',is_active:true,project_id:'project'},{id:'other',name:'另一仓库',is_active:true,project_id:'project'}]}:path.includes('/deployment-options')?{options:['api']}:{variables:[]}}));t.after(h.stop)
 h.props.deploymentPlan={kind:'kubernetes'};h.props.buildNodes=[];await settle()
 const reads=h.requests.length
 await h.state.setDeploymentRegistry('registry')
 assert.equal(h.state.kubernetesImage.value,true)
 h.props.config.artifact_node_id='legacy-build'
 assert.equal(h.props.config.deployment_registry_id,'registry')
 assert.deepEqual(h.props.config.deployment_inputs,[{kind:'registry',registry_id:'registry',image:'',tag:'${EDO_IMAGE_TAG}'}])
 await h.state.chooseImage(0,'api')
 assert.equal(h.props.config.deployment_inputs[0].tag,'${EDO_IMAGE_TAG}')
 h.state.update(0,{tag:'release-2'})
 assert.equal(h.props.config.deployment_inputs[0].tag,'release-2')
 assert.equal(h.props.config.artifact_node_id,undefined)
 assert.ok(h.requests.slice(reads).every(row=>!row.path.includes('build-plans')&&!row.path.includes('delivery-variables')))
 h.props.config.deployment_inputs[0].image='api';await h.state.setDeploymentRegistry('other')
 assert.equal(h.props.config.deployment_inputs[0].image,'')
 h.auth.user.permissions=['workflow.update'];await settle();const count=h.events.length
 await h.state.setDeploymentRegistry('forbidden');await h.state.chooseImage(0,'forbidden');h.state.update(0,{tag:'forbidden'});assert.equal(h.events.length,count)
})

test('Kubernetes 已保存仓库缺少镜像行时，用户输入会生成准确来源；未知仓库不提交',async t=>{
 const h=await harness(path=>({data:path==='/image-registries'?{image_registries:[{id:'registry',name:'部署仓库',is_active:true,project_id:'project'}]}:{variables:[]}}));t.after(h.stop)
 h.props.deploymentPlan={kind:'kubernetes'};h.props.config={deployment_registry_id:'registry'};await settle()
 assert.deepEqual(h.events,[])
 await h.state.chooseImage(-1,'api')
 assert.deepEqual(h.props.config.deployment_inputs,[{kind:'registry',registry_id:'registry',image:'api',tag:'${EDO_IMAGE_TAG}'}])
 const count=h.events.length;await h.state.setDeploymentRegistry('unknown');assert.equal(h.events.length,count)
})

test('Kubernetes方案仅保存默认仓库，阶段继承后再填写镜像与Tag',async t=>{
 const get=path=>({data:path==='/image-registries'?{image_registries:[{id:'registry',name:'默认仓库',is_active:true,project_id:'project'},{id:'other',name:'另一仓库',is_active:true,project_id:'project'}]}:path.includes('/deployment-options')?{options:['api']}:{variables:[]}})
 const template=await harness(get);t.after(template.stop)
 template.props.deploymentPlan={kind:'kubernetes'};template.props.registryOnly=true
 template.props.config={deployment_input_version:1,deployment_inputs:[]};await settle()
 await template.state.setDeploymentRegistry('registry')
 assert.equal(template.props.config.deployment_registry_id,'registry')
 assert.deepEqual(template.props.config.deployment_inputs,[])
 assert.ok(template.requests.every(row=>!row.path.includes('/deployment-options')))
 const saved=JSON.parse(JSON.stringify(template.props.config))
 const stage=await harness(get);t.after(stage.stop)
 stage.props.deploymentPlan={kind:'kubernetes'};stage.props.config=JSON.parse(JSON.stringify(saved));await settle()
 await stage.state.registryOptions(-1,false)
 assert.deepEqual(stage.state.optionsByRow.value[0].images,['api'])
 await stage.state.chooseImage(0,'api');stage.state.update(0,{tag:'v2'})
 assert.equal(stage.props.config.deployment_inputs[0].registry_id,'registry')
 assert.deepEqual(saved.deployment_inputs,[])
 template.props.config.deployment_inputs=[{kind:'registry',registry_id:'registry',image:'old-api',tag:'old-tag'}]
 await template.state.setDeploymentRegistry('other')
 assert.equal(template.props.config.deployment_registry_id,'other');assert.deepEqual(template.props.config.deployment_inputs,[])
})
