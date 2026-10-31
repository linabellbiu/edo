import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {test} from 'node:test'
import {parse,compileScript,compileTemplate} from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'

const settle=async()=>{await vue.nextTick();await Promise.resolve();await vue.nextTick()}
const key=value=>({key:value,stops:0,prevented:0,stopPropagation(){this.stops++},preventDefault(){this.prevented++}})
function loadSelect(fake){
 const source=readFileSync(new URL('../src/components/KubeEditorSelect.vue',import.meta.url),'utf8')
 const script=compileScript(parse(source).descriptor,{id:'kube-editor-select'}).content
 const output=ts.transpileModule(script,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022,esModuleInterop:true}}).outputText,module={exports:{}}
 new Function('require','module','exports',output)(name=>name==='vue'?vue:name==='ant-design-vue'?{Select:fake}:{},module,module.exports)
 return module.exports.default
}
function loadFrontend(path,modules={},template=false){
 const raw=readFileSync(new URL(path,import.meta.url),'utf8'),descriptor=path.endsWith('.vue')?parse(raw).descriptor:undefined
 const script=descriptor?compileScript(descriptor,{id:'kube-select-integration'}):undefined
 const output=ts.transpileModule(script?.content??raw,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022,esModuleInterop:true}}).outputText,module={exports:{}}
 new Function('require','module','exports','window',output)(name=>modules[name]??{},module,module.exports,{addEventListener(){},removeEventListener(){}})
 if(template){
  const rendered=compileTemplate({source:descriptor.template.content,filename:path,id:'kube-select-integration',compilerOptions:{bindingMetadata:script.bindings,mode:'function'}})
  assert.deepEqual(rendered.errors,[])
  module.exports.default.render=new Function('Vue',ts.transpileModule(rendered.code,{compilerOptions:{target:ts.ScriptTarget.ES2022}}).outputText)(vue)
 }
 return module.exports
}
function harness(component,fakeState,{active=true,disabled=false}={}){
 const events=[],calls=[],slotArguments=[],value={value:7,label:'seven'},options=[{value:7,label:'seven'}]
 const changed=(...args)=>calls.push(['change',...args]),updated=(...args)=>calls.push(['update',...args])
 const parent=vue.reactive({active,disabled,value,options,searchValue:'find',mode:'multiple'})
 const remove=node=>{if(node.parent){const siblings=node.parent.children,index=siblings.indexOf(node);if(index>=0)siblings.splice(index,1);node.parent=undefined}}
 const renderer=vue.createRenderer({patchProp(){},insert(node,target,anchor){remove(node);node.parent=target;const children=target.children??=[],index=anchor?children.indexOf(anchor):-1;children.splice(index<0?children.length:index,0,node)},remove,createElement:()=>({children:[]}),createText:()=>({}),createComment:()=>({}),setText(){},setElementText(){},parentNode:node=>node.parent,nextSibling:node=>node.parent?.children[node.parent.children.indexOf(node)+1]??null})
 const app=renderer.createApp({render:()=>vue.h(component,{...parent,'aria-label':'镜像拉取策略',id:'select-test',onChange:changed,'onUpdate:value':updated,onDropdownVisibleChange:open=>events.push(['visible',open]),onInputKeyDown:event=>events.push(['input',event])},{option:payload=>{slotArguments.push(payload);return 'option'}})})
 app.mount({children:[]});let stopped=false
 return {parent,events,calls,slotArguments,get binding(){return fakeState.current.vnode.props},get slots(){return fakeState.current.slots},stop(){if(!stopped){stopped=true;app.unmount()}}}
}

test('Kubernetes 编辑器局部 Select 使用公开开关状态与键盘事件，不依赖内部 DOM',async t=>{
 const fakeState={}
 const fake=vue.defineComponent({inheritAttrs:false,setup(){fakeState.current=vue.getCurrentInstance();return()=>null}})
 const component=loadSelect(fake)
 await t.test('首个 ESC 只关闭打开的下拉，关闭后的 ESC 不被拦截',async t=>{
  const h=harness(component,fakeState);t.after(h.stop)
  assert.equal(h.binding.open,false);h.binding.onDropdownVisibleChange(true);await settle();assert.equal(h.binding.open,true)
  const first=key('Escape');h.binding.onInputKeyDown(first);await settle()
  assert.equal(h.binding.open,false);assert.equal(first.stops,1)
  assert.deepEqual(h.events,[['visible',true],['visible',false],['input',first]])
  const second=key('Escape');h.binding.onInputKeyDown(second);await settle()
  assert.equal(second.stops,0);assert.deepEqual(h.events.at(-1),['input',second])
 })
 await t.test('Arrow、Enter、搜索和值事件仍由现有 Select 处理，透传保持原类型与次数',async t=>{
  const h=harness(component,fakeState);t.after(h.stop)
  assert.equal(h.binding.value,h.parent.value);assert.equal(h.binding.options,h.parent.options);assert.equal(h.binding.searchValue,'find');assert.equal(h.binding.mode,'multiple')
  assert.equal(h.binding['aria-label'],'镜像拉取策略');assert.equal(h.binding.id,'select-test')
  for(const name of ['ArrowDown','Enter','Tab']){const event=key(name);h.binding.onInputKeyDown(event);assert.equal(event.stops,0);assert.equal(event.prevented,0)}
  const newValue=[7,8],selection={value:8,label:'eight'};h.binding.onChange(newValue,selection);h.binding['onUpdate:value'](newValue)
  assert.equal(h.calls.length,2);assert.equal(h.calls[0][1],newValue);assert.equal(h.calls[0][2],selection);assert.equal(h.calls[1][1],newValue)
  const payload={label:'seven',value:7};h.slots.option(payload);assert.equal(h.slotArguments[0],payload)
 })
 await t.test('显式 disabled 或 inactive 变化立即关闭下拉，恢复后不会自行重开',async t=>{
  for(const property of ['active','disabled']){
   const h=harness(component,fakeState);t.after(h.stop);h.binding.onDropdownVisibleChange(true);await settle();assert.equal(h.binding.open,true)
   h.parent[property]=property==='active'?false:true;await settle();assert.equal(h.binding.open,false)
   assert.equal(h.binding.disabled,true)
   h.binding.onDropdownVisibleChange(true);await settle();assert.equal(h.binding.open,false)
   h.parent[property]=property==='active'?true:false;await settle();assert.equal(h.binding.open,false)
  }
 })
 await t.test('重复关闭通知不重复发事件，空闲 ESC 不改变值或发起选择',async t=>{
  const h=harness(component,fakeState);t.after(h.stop);h.binding.onDropdownVisibleChange(true);await settle()
  const event=key('Escape');h.binding.onInputKeyDown(event);h.binding.onDropdownVisibleChange(false);await settle()
  assert.deepEqual(h.events.filter(item=>item[0]==='visible'),[['visible',true],['visible',false]])
  assert.deepEqual(h.calls,[]);assert.equal(h.parent.value.value,7)
 })
 await t.test('活动时未显式设置 disabled 不覆盖现有 Form 禁用上下文',async t=>{
  const h=harness(component,fakeState);t.after(h.stop);h.parent.disabled=undefined;await settle();assert.equal(h.binding.disabled,undefined)
  h.parent.active=false;await settle();assert.equal(h.binding.disabled,true)
  h.parent.active=true;await settle();assert.equal(h.binding.disabled,undefined);assert.equal(h.binding.open,false)
 })
})

test('真实 Editor 与容器子表单都接入局部 Select，忙碌与关窗清理，下次 ESC 仍经原 dirty 确认',async t=>{
 const selects=[],dialogs=[],events=[],calls=[]
 const fakeSelect=vue.defineComponent({inheritAttrs:false,setup(){selects.push(vue.getCurrentInstance());return()=>null}})
 const select=loadSelect(fakeSelect),empty={render:()=>null},modules={vue,'vue-i18n':{useI18n:()=>({t:key=>key})},'./KubeEditorSelect.vue':{__esModule:true,default:select},'./KubeKeyValues.vue':{__esModule:true,default:empty}}
 const api=loadFrontend('../src/api/kubernetes.ts')
 const fields=loadFrontend('../src/components/KubeContainerFields.vue',modules,true).default
 const client=Object.fromEntries(['get','post','request'].map(method=>[method,async()=>{calls.push(method);throw new Error('Select 交互不应发起请求')}]))
 const component=loadFrontend('../src/components/KubeResourceEditor.vue',{
  ...modules,'./KubeContainerFields.vue':{__esModule:true,default:fields},'./KubeConfigEntries.vue':{__esModule:true,default:empty},
  'ant-design-vue':{Modal:{confirm:options=>{const dialog={...options,destroy(){},update(){}};dialogs.push(dialog);return dialog}},message:{success(){}}},
  '@/api/kubernetes':api,'@/api/client':{__esModule:true,default:client},'@/utils/kubernetesWorkloadForm':loadFrontend('../src/utils/kubernetesWorkloadForm.ts',{'@/api/kubernetes':api}),
 },true).default
 const props=vue.reactive({open:true,cluster:'local-test',namespace:'team',type:api.kubeTypes.find(type=>type.resource==='deployments'),object:{apiVersion:'apps/v1',kind:'Deployment',metadata:{name:'web',namespace:'team',uid:'web-uid',resourceVersion:'12'},spec:{replicas:1,template:{spec:{containers:[{name:'web',image:'demo:1'}]}}}}})
 let state,modal
 const bridge={...component,setup(current,context){state=component.setup(current,{...context,emit:(...args)=>events.push(args)});return state}}
 const remove=node=>{if(node.parent){const siblings=node.parent.children,index=siblings.indexOf(node);if(index>=0)siblings.splice(index,1);node.parent=undefined}}
 const renderer=vue.createRenderer({patchProp(){},insert(node,target,anchor){remove(node);node.parent=target;const children=target.children??=[],index=anchor?children.indexOf(anchor):-1;children.splice(index<0?children.length:index,0,node)},remove,createElement:()=>({children:[]}),createText:()=>({}),createComment:()=>({}),setText(){},setElementText(){},parentNode:node=>node.parent,nextSibling:node=>node.parent?.children[node.parent.children.indexOf(node)+1]??null})
 const app=renderer.createApp({render:()=>vue.h(bridge,props)})
 for(const path of ['../src/components/KubeResourceEditor.vue','../src/components/KubeContainerFields.vue']){
  const source=readFileSync(new URL(path,import.meta.url),'utf8')
  for(const tag of new Set([...source.matchAll(/<(a-[\w-]+)/g)].map(match=>match[1]))){
   if(app.component(tag))continue
   app.component(tag,{inheritAttrs:false,setup(_props,{slots}){if(tag==='a-modal')modal=vue.getCurrentInstance();return()=>vue.h('section',[slots.default?.(),slots.footer?.()])}})
  }
 }
 app.mount({children:[]});t.after(()=>app.unmount());await settle()
 const binding=label=>{const instance=selects.find(item=>!item.isUnmounted&&item.vnode.props['aria-label']===label);assert.ok(instance,label);return instance.vnode.props}
 for(const label of ['k8s.deploymentStrategy','k8s.imagePullPolicy']){
  assert.equal(binding(label).open,false);binding(label).onDropdownVisibleChange(true);await settle();assert.equal(binding(label).open,true,label)
 }
 for(const phase of ['saving','confirming']){
  state[phase].value=true;await settle()
  for(const label of ['k8s.deploymentStrategy','k8s.imagePullPolicy']){assert.equal(binding(label).open,false,label);assert.equal(binding(label).disabled,true,label)}
  state[phase].value=false;await settle()
  for(const label of ['k8s.deploymentStrategy','k8s.imagePullPolicy']){assert.equal(binding(label).open,false,label);binding(label).onDropdownVisibleChange(true)}
  await settle()
 }
 props.open=false;await settle()
 for(const label of ['k8s.deploymentStrategy','k8s.imagePullPolicy']){assert.equal(binding(label).open,false,label);assert.equal(binding(label).disabled,true,label)}
 props.open=true;await settle()
 for(const label of ['k8s.deploymentStrategy','k8s.imagePullPolicy'])assert.equal(binding(label).open,false,label)
 state.form.volumes.push({name:'kept',kind:'preserve',source:'',medium:'',sizeLimit:''});await settle()
 assert.equal(binding('k8s.volumeType').disabled,true);binding('k8s.volumeType').onDropdownVisibleChange(true);await settle();assert.equal(binding('k8s.volumeType').open,false)
 state.form.containers[0].image='DRAFT_B';assert.equal(state.dirty.value,true)
 binding('k8s.imagePullPolicy').onDropdownVisibleChange(true);await settle()
 const first=key('Escape');binding('k8s.imagePullPolicy').onInputKeyDown(first);await settle();assert.equal(first.stops,1);assert.equal(dialogs.length,0)
 // 这里只校验公开事件与编辑器关闭契约；原生 DOM 冒泡顺序仍由独立页面验收。
 const second=key('Escape');binding('k8s.imagePullPolicy').onInputKeyDown(second);assert.equal(second.stops,0)
 const closing=modal.vnode.props.onCancel();assert.equal(dialogs.length,1);dialogs[0].onCancel();dialogs[0].afterClose();await closing
 assert.equal(props.open,true);assert.equal(state.form.containers[0].image,'DRAFT_B');assert.deepEqual(events,[]);assert.deepEqual(calls,[])
})
