import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { baseParse, compile } from '@vue/compiler-dom'
import { compileScript, parse } from '@vue/compiler-sfc'
import { Form as AntForm } from 'ant-design-vue'
import { renderToString } from '@vue/server-renderer'
import * as vue from 'vue'
import ts from 'typescript'
import { loadFrontend } from './acceptanceHarness.mjs'

const template = path => parse(readFileSync(new URL(path, import.meta.url), 'utf8')).descriptor.template.content
const editorTemplate = template('../src/views/PipelineEditorView.vue')
const composeTemplate = template('../src/components/ComposeTaskFields.vue')
const taskControlsTemplate = template('../src/components/PipelineTaskControls.vue')
const buildPlanFieldsTemplate = template('../src/components/BuildPlanFields.vue')
function findSection(node) {
  if (node.tag === 'section' && node.props.some(prop => prop.name === 'class' && prop.value?.content === 'node-notification-section')) return node
  return node.children?.map(findSection).find(Boolean)
}
const notificationTemplate = findSection(baseParse(editorTemplate)).loc.source
function inlineComponent(path, modules = {}) {
 const descriptor=parse(readFileSync(new URL(path,import.meta.url),'utf8')).descriptor
 const content=compileScript(descriptor,{id:'advanced-field-test',inlineTemplate:true}).content
 const output=ts.transpileModule(content,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022}}).outputText
 const module={exports:{}}
 new Function('require','module','exports',output)(name=>name==='vue'?vue:modules[name]||{},module,module.exports)
 return module.exports.default
}
const helpComponent=inlineComponent('../src/components/DeploymentFieldHelp.vue')
const advancedComponent=inlineComponent('../src/components/BuildPlanAdvancedFields.vue',{'@/components/DeploymentFieldHelp.vue':{default:helpComponent},'@/utils/buildTarget':loadFrontend('../src/utils/buildTarget.ts')})

const passthrough = {
  inheritAttrs: false,
  setup(_, { attrs, slots }) {
    return () => vue.h('div', attrs, [attrs.message, attrs.description, slots.label?.(), slots.default?.()])
  },
}
async function render(source, state, components = {}) {
  const { code } = compile(source, { mode: 'function', prefixIdentifiers: true, expressionPlugins: ['typescript'] })
  const output = ts.transpileModule(code, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText
  const render = new Function('Vue', output)(vue)
  const app = vue.createSSRApp({ setup: () => state, render })
  for (const name of ['a-alert', 'a-button', 'a-select', 'a-form-item', 'a-input', 'a-textarea', 'a-checkbox', 'a-tooltip', 'Bell', 'Plus', 'Trash2']) app.component(name, passthrough)
  for (const [name, component] of Object.entries(components)) app.component(name, component)
  return renderToString(app)
}
function notificationState(patch = {}) {
  return {
    selectedTask: { config: { notifications: [] } }, canReadNotificationChannels: true,
    canCreateNotificationChannels: true, canEdit: true, activeNotificationChannels: [],
    openNotificationChannelModal() {}, addNotificationRule() {}, ...patch,
  }
}

test('没有通知渠道时删除空渠道提示，保留创建入口与禁用的添加按钮', async () => {
  for (const notifications of [[], [{ id: 'saved-rule', channel_id: 'inactive-channel' }]]) {
    const html = await render(notificationTemplate, notificationState({ selectedTask: { config: { notifications } } }))
    assert.doesNotMatch(html, /还没有可用通知渠道|当前任务尚未配置通知|node-notification-list/)
    assert.match(html, /新建渠道/)
    assert.match(html, /disabled[^>]*>.*?添加/s)
  }
})
test('通知渠道读取权限提醒和已有渠道下的未配置通知提醒继续保留', async () => {
  const denied = await render(notificationTemplate, notificationState({ canReadNotificationChannels: false }))
  assert.match(denied, /当前账号没有读取通知渠道的权限/)
  const available = await render(notificationTemplate, notificationState({ activeNotificationChannels: [{ id: 'channel' }] }))
  assert.match(available, /当前任务尚未配置通知/)
})
test('部署任务自动管理项目名，保留 Docker Compose 名称和服务提示', async () => {
  const html = await render(composeTemplate, {
    error: '', kind: 'compose_stack', disabled: false,
    node: { config: { deployment_plan_id: 'plan' } }, tagReference:'${EDO_IMAGE_TAG}', validTagReference:()=>true,
    settings: { project_name: 'team', tag_variable: 'EDO_IMAGE_TAG', services: [] }, services: [], updateSettings() {},
  })
  assert.doesNotMatch(html, /使用 EDO 保存的|上游构建镜像自动匹配并固定摘要/)
  assert.match(html, /Docker Compose 部署配置/)
  assert.doesNotMatch(html, /查看和编辑 Docker Compose 部署方案/)
  assert.doesNotMatch(html, /Docker Compose 项目名/)
  assert.match(html, /镜像 Tag/)
  assert.match(html, /留空执行整个 compose.yaml 项目/)
})
test('部署方案读取失败或方案类型不匹配时仍显示必要警告', async () => {
  const html = await render(composeTemplate, { error: '读取失败', kind: '', node: { config: { compose: {} } } })
  assert.match(html, /读取失败/)
  assert.match(html, /请选择 Docker Compose 部署方案/)
})

test('部署任务通知分区只渲染通知，不再渲染部署参数编辑器',async()=>{
 const tree=baseParse(editorTemplate)
 const find=node=>node.tag==='a-form'&&node.children?.some(child=>child.tag==='section'&&child.props?.some(prop=>prop.name==='class'&&prop.value?.content==='node-notification-section'))?node:node.children?.map(find).find(Boolean)
 const form=find(tree)
 const html=await render(form.loc.source.replace('v-else',''),{...notificationState(),selectedNode:{id:'deploy',type:'deploy',config:{}},canUpdate:true,canEdit:true})
 assert.match(html,/node-notification-section/)
 assert.doesNotMatch(html,/task-controls|构建制品来源|部署脚本|额外引用共享变量|部署前下载/)
})

test('内联与库镜像构建的 Tag 均位于常规配置，自定义构建不显示镜像 Tag',async()=>{
 const find=node=>node.tag==='a-form'&&node.props?.some(prop=>prop.name==='if'&&prop.exp?.content==="propertyTab === 'common'")?node:node.children?.map(find).find(Boolean)
 const common=find(baseParse(editorTemplate)).loc.source
 for(const inline of [false,true])for(const kind of ['dockerfile','script']){
  const config=inline?{inline_build:{kind},image_tag:'${EDO_GIT_TAG}'}:{build_plan_id:'plan',image_tag:'release-v2'}
  const html=await render(common,{propertyTab:'common',editorApplication:{name:'demo_app'},selectedNode:{id:'build',type:'build',name:'构建',config},selectedBuildPlan:{id:'plan',kind},selectedLibraryBuildPlan:inline?undefined:{id:'plan',name:'通用构建',kind,is_active:true},buildSource:()=>inline?'inline':'plan',canReadBuildPlan:true,canModifyBuildPlan:false,changingBuildPlanNodeID:'',selectedBuildPlanOptions:[],canUpdate:true,canEdit:true,canCreateBuildPlan:false,buildPlanOptions:[],imageTagHelp:'服务端变量说明',updateSelectedNode(){}})
  assert.equal(html.includes('label="镜像 Tag"'),kind==='dockerfile')
  assert.doesNotMatch(html,/label="镜像名称"/)
  assert.doesNotMatch(html,/Docker target/)
  assert.match(html,/配置来源/);assert.match(html,/当前任务配置/);assert.match(html,/选择构建方案/)
  assert.doesNotMatch(html,/value="inline"[^>]*>自定义构建/)
  if(kind==='dockerfile')assert.match(html,inline?/value="\$\{EDO_GIT_TAG\}"/:/value="release-v2"/)
 }
})

test('构建高级配置不重复展示 Tag 或项目变量选择，检查任务继续提供原引用入口',async()=>{
 for(const scriptBuild of [false,true]){
  const html=await render(taskControlsTemplate,{node:{id:'build',type:'build',config:{variable_names:['KEEP_ME'],variable_environment_id:'saved-group'}},scriptBuild,canEdit:true,error:'',buildNodes:[],updateCache(){},change(){}})
  assert.doesNotMatch(html,/镜像名称|镜像 Tag|引用当前项目变量与密钥|变量适用主机组/)
 }
 const shell=await render(taskControlsTemplate,{node:{type:'shell',config:{}},canEdit:true,error:'',tool:'script',variableOptions:[],environments:[],change(){}})
 assert.match(shell,/引用当前项目变量与密钥/)
 assert.match(shell,/变量适用主机组/)
})


test('共享构建参数表单将 target 收进高级配置，继续保留 Dockerfile 和 Context',async()=>{
 const html=await render(buildPlanFieldsTemplate,{plan:{kind:'dockerfile',dockerfile_path:'Dockerfile',context_path:'.',timeout_seconds:1800},disabled:false,hideKind:false,registries:[],imageDestination:'registry',saveArtifact:false,buildArgsText:'',environmentText:'',editPermission:'workflow.update',emit(){}})
 assert.match(html,/Dockerfile 路径/)
 assert.match(html,/Context/)
 assert.match(html,/header="高级配置"/)
 assert.match(html,/BuildPlanAdvancedFields/i)
 assert.doesNotMatch(html,/例如 web,api/)
})

test('流水线隐藏重复构建方式，独立方案编辑保留类型选择及对应参数',async()=>{
 for(const kind of ['dockerfile','script'])for(const hideKind of [false,true])for(const disabled of [false,true]){
  const html=await render(buildPlanFieldsTemplate,{plan:{kind,dockerfile_path:'Dockerfile',context_path:'.',script:'echo ok',timeout_seconds:1800,image_registry_id:'registry'},disabled,hideKind,registries:[],imageDestination:'registry',saveArtifact:false,buildArgsText:'',environmentText:'',editPermission:'workflow.update',emit(){}})
  assert.equal(html.includes('label="构建方式"'),!hideKind)
  if(kind==='dockerfile'){
   assert.match(html,/Dockerfile 路径/)
   assert.match(html,/Context/)
   assert.match(html,/关联镜像仓库/)
  }else{
   assert.match(html,/构建脚本/)
   assert.doesNotMatch(html,/Dockerfile 路径|关联镜像仓库/)
  }
 }
})

test('任务操作区在常规和高级配置外提供保存入口，只读隐藏，已启用状态仍明确更新',async()=>{
 const find=node=>node.tag==='footer'&&node.props?.some(prop=>prop.name==='class'&&prop.value?.content==='panel-property-actions')?node:node.children?.map(find).find(Boolean)
 const actions=find(baseParse(editorTemplate)).loc.source
 for(const active of [false,true]){
  const html=await render(actions,{canUpdate:true,workflow:{is_active:active},saving:false,saveState:'有未保存更改',requestSave(){},closePanel(){}})
  assert.match(html,/aria-label="任务配置操作"/)
  assert.match(html,active?/保存并更新/:/保存/)
  assert.match(html,/role="status"/)
 }
 const readonly=await render(actions,{canUpdate:false})
 assert.doesNotMatch(readonly,/保存|任务配置操作/)
})

test('真实 Form 标签保留镜像 Tag 问号按钮及目录说明，不能被 label 属性覆盖',async()=>{
 const find=node=>node.tag==='a-form-item'&&node.children?.some(child=>child.tag==='a-input'&&child.props?.some(prop=>prop.name==='aria-label'&&prop.value?.content==='镜像 Tag'))?node:node.children?.map(find).find(Boolean)
 const field=find(baseParse(editorTemplate)).loc.source
 const descriptor=parse(readFileSync(new URL('../src/components/DeploymentFieldHelp.vue',import.meta.url),'utf8')).descriptor
 const script=compileScript(descriptor,{id:'tag-help',inlineTemplate:true}).content
 const output=ts.transpileModule(script,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022}}).outputText
 const module={exports:{}}
 new Function('require','module','exports',output)(name=>name==='vue'?vue:{},module,module.exports)
 const html=await render(`<a-form>${field}</a-form>`,{selectedBuildPlan:{kind:'dockerfile'},selectedNode:{config:{image_tag:'${EDO_GIT_TAG}'}},canEdit:true,imageTagHelp:'服务端目录：没有 Tag 时使用完整 Commit。',updateSelectedNode(){}},{'a-form':AntForm,'a-form-item':AntForm.Item,DeploymentFieldHelp:module.exports.default})
 assert.match(html,/<button[^>]*aria-label="镜像 Tag 说明"[^>]*>\?<\/button>/)
 assert.match(html,/服务端目录：没有 Tag 时使用完整 Commit/)
})


test('镜像名称与单 target 位于高级配置，库方案 target 可按任务编辑且不重复显示修改入口',async()=>{
 const find=node=>node.tag==='section'&&node.props?.some(prop=>prop.name==='class'&&prop.value?.content==='build-advanced-fields')?node:node.children?.map(find).find(Boolean)
 const section=find(baseParse(editorTemplate)).loc.source
 for(const inline of [false,true])for(const kind of ['dockerfile','script'])for(const canEdit of [false,true]){
  const html=await render(section,{selectedTask:{},selectedNode:{type:'build',config:{image_name:'custom',...(inline?{inline_build:{kind,target_stage:'runtime'}}:{build_plan_id:'shared'})}},selectedBuildPlan:{kind,target_stage:'runtime'},selectedBuildTarget:'runtime',selectedBuildPlatform:'linux/amd64',selectedLibraryBuildPlan:inline?undefined:{id:'shared'},canEdit,canModifyBuildPlan:!inline&&canEdit,editorApplication:{name:'demo_app'},resourceViewHref:()=>'/build-plans?plan=shared',updateSelectedBuildTarget(){},updateSelectedNode(){},modifyLinkedBuildPlan(){}},{BuildPlanAdvancedFields:advancedComponent})
  if(kind==='script'){assert.doesNotMatch(html,/镜像名称|BuildPlanAdvancedFields/i);continue}
  assert.match(html,/label="镜像名称"/);assert.match(html,/value="custom"/)
  assert.match(html,/value="runtime"/)
  assert.equal(/disabled[^>]*aria-label="Docker target"/.test(html),!canEdit)
  assert.doesNotMatch(html,/修改构建方案/)
  assert.doesNotMatch(html,/镜像 Tag/)
 }
})


test('Docker target 使用真实表单显示问号说明和单值诊断',async()=>{
 for(const target of ['','runtime','web,api','web api','web\napi','a'.repeat(129)]){
  const error=loadFrontend('../src/utils/buildTarget.ts').dockerTargetError(target)
  const html=await render('<a-form><BuildPlanAdvancedFields :target="target" :disabled="false" /></a-form>',{target},{'a-form':AntForm,'a-form-item':AntForm.Item,BuildPlanAdvancedFields:advancedComponent})
  assert.match(html,/<button[^>]*aria-label="Docker target 说明"[^>]*>\?<\/button>/)
  assert.match(html,/docker build --target runtime/)
  assert.match(html,/留空时构建 Dockerfile 的最终阶段/)
  assert.equal(html.includes('ant-form-item-has-error'),!!error)
  if(error)assert.match(html,/只能填写一个阶段名/)
 }
})
