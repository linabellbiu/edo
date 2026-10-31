import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {test} from 'node:test'
import {parse,compileScript,compileTemplate,compileStyle} from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'

const source=readFileSync(new URL('../src/components/KubeResourceEditor.vue',import.meta.url),'utf8')
const descriptor=parse(source).descriptor
function load(path,modules={}){
 const raw=readFileSync(new URL(path,import.meta.url),'utf8')
 const compiled=path.endsWith('.vue')?compileScript(parse(raw).descriptor,{id:'kube-modal-shell'}).content:raw
 const output=ts.transpileModule(compiled,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022,esModuleInterop:true}}).outputText,module={exports:{}}
 new Function('require','module','exports','window',output)(name=>modules[name]??{},module,module.exports,{addEventListener(){},removeEventListener(){}})
 return module.exports
}
const api=load('../src/api/kubernetes.ts')
const antMotion=load('../node_modules/ant-design-vue/es/_util/transition.js',{
 vue,'./type':{tuple:(...items)=>items},'@babel/runtime/helpers/esm/extends':{__esModule:true,default:Object.assign},
})
const object=()=>({apiVersion:'v1',kind:'ConfigMap',metadata:{name:'settings',namespace:'team',uid:'settings-uid',resourceVersion:'12'},data:{setting:'FIRST'}})
const settle=async()=>{await vue.nextTick();await Promise.resolve();await vue.nextTick()}

function editor(){
 const events=[],dialogs=[],calls=[],props=vue.reactive({open:true,cluster:'local-test',namespace:'team',type:api.kubeTypes.find(item=>item.resource==='configmaps'),object:object()})
 const component=load('../src/components/KubeResourceEditor.vue',{
  vue,'vue-i18n':{useI18n:()=>({t:key=>key})},
  'ant-design-vue':{Modal:{confirm:options=>{const dialog={...options,destroy(){},update(){}};dialogs.push(dialog);return dialog}},message:{success:value=>events.push(['success',value])}},
  '@/api/kubernetes':api,'@/api/client':{__esModule:true,default:Object.fromEntries(['get','post','request'].map(method=>[method,async()=>{calls.push(method);throw new Error('外壳测试不应发起请求')}]))},
  '@/utils/kubernetesWorkloadForm':load('../src/utils/kubernetesWorkloadForm.ts',{'@/api/kubernetes':api}),
 }) .default
 const script=compileScript(descriptor,{id:'kube-modal-shell'})
 const template=compileTemplate({source:descriptor.template.content,filename:'KubeResourceEditor.vue',id:'kube-modal-shell',compilerOptions:{bindingMetadata:script.bindings,mode:'function'}})
 assert.deepEqual(template.errors,[])
 let state,shell
 const shellComponent=kind=>vue.defineComponent({inheritAttrs:false,props:['open','title','width','wrapClassName','focusTriggerAfterClose','maskClosable','closable','keyboard','transitionName','maskTransitionName'],setup(_props,context){shell={kind,instance:vue.getCurrentInstance(),context};return()=>null}})
 const renderCode=ts.transpileModule(template.code,{compilerOptions:{target:ts.ScriptTarget.ES2022}}).outputText
 const bridge={...component,setup(current,context){state=component.setup(current,{...context,emit:(...args)=>events.push(args)});return state},render:new Function('Vue',renderCode)(vue)}
 const remove=node=>{if(node.parent){const siblings=node.parent.children,index=siblings.indexOf(node);if(index>=0)siblings.splice(index,1);node.parent=undefined}}
 const renderer=vue.createRenderer({patchProp(){},insert(node,parent,anchor){remove(node);node.parent=parent;const children=parent.children??=[],index=anchor?children.indexOf(anchor):-1;children.splice(index<0?children.length:index,0,node)},remove,createElement:()=>({children:[]}),createText:()=>({}),createComment:()=>({}),setText(){},setElementText(){},parentNode:node=>node.parent,nextSibling:node=>node.parent?.children[node.parent.children.indexOf(node)+1]??null})
 const app=renderer.createApp({setup:()=>()=>vue.h(bridge,props)})
 for(const tag of new Set([...descriptor.template.content.matchAll(/<(a-[\w-]+)/g)].map(match=>match[1])))app.component(tag,tag==='a-modal'?shellComponent('modal'):tag==='a-drawer'?shellComponent('drawer'):{render:()=>null})
 app.mount({children:[]});let stopped=false
 return {state,props,events,dialogs,calls,get shell(){return shell},get binding(){return {...shell.instance.vnode.props,...shell.instance.props}},stop(){if(!stopped){stopped=true;app.unmount()}}}
}

test('Kubernetes 编辑器委托现有 Modal 焦点机制，保留右侧外壳与单向关闭',async t=>{
 const h=editor();t.after(h.stop)
 assert.equal(h.shell.kind,'modal')
 assert.equal(h.binding.open,true);assert.equal(h.binding.title,'k8s.editk8s.type_configmaps');assert.equal(h.binding.width,'min(800px,94vw)')
 assert.equal(h.binding.wrapClassName,'kube-resource-editor-modal');assert.equal(h.binding.focusTriggerAfterClose,true)
 assert.equal(h.binding['onUpdate:open'],undefined);assert.equal(typeof h.binding.onCancel,'function');assert.equal(h.binding.onClose,undefined)
 assert.equal(h.binding.transitionName,'kube-resource-editor-slide');assert.equal(h.binding.maskTransitionName,'')
 await h.binding.onCancel();assert.deepEqual(h.events,[['update:open',false]]);assert.deepEqual(h.calls,[])
})

test('首次 Portal 显示保留 appear 生命周期及供组件库捕获焦点的首次进入钩子',async t=>{
 const h=editor();t.after(h.stop)
 const motion=antMotion.getTransitionProps(h.binding.transitionName)
 assert.equal(motion.appear,true)
 let entered=0
 const renderer=vue.createRenderer({patchProp(){},insert(){},remove(){},createElement:()=>({}),createText:()=>({}),createComment:()=>({}),setText(){},setElementText(){},parentNode:()=>null,nextSibling:()=>null})
 const app=renderer.createApp({render:()=>vue.h(vue.BaseTransition,{appear:motion.appear,onAfterEnter:()=>entered++},()=>vue.h('section'))})
 t.after(()=>app.unmount());app.mount({});await vue.nextTick()
 assert.equal(entered,1)
})

test('同一右侧外壳保留工作负载宽度且身份回填不变',async t=>{
 const h=editor();t.after(h.stop)
 h.props.type=api.kubeTypes.find(item=>item.resource==='deployments')
 h.props.object={apiVersion:'apps/v1',kind:'Deployment',metadata:{name:'web',namespace:'team',uid:'web-uid',resourceVersion:'14'},spec:{replicas:1,template:{spec:{containers:[{name:'app',image:'web:1'}]}}}}
 await settle()
 assert.equal(h.shell.kind,'modal');assert.equal(h.binding.width,'min(1120px,96vw)');assert.equal(h.state.form.name,'web');assert.equal(h.state.dirty.value,false)
})

test('保存或确认中禁止所有外壳关闭路径，不额外关闭或发请求',async t=>{
 const h=editor();t.after(h.stop)
 for(const phase of ['saving','confirming']){
  h.state[phase].value=true;await settle()
  assert.equal(h.binding.maskClosable,false,phase);assert.equal(h.binding.closable,false,phase);assert.equal(h.binding.keyboard,false,phase)
  await h.binding.onCancel();assert.deepEqual(h.events,[]);assert.deepEqual(h.dialogs,[]);assert.deepEqual(h.calls,[])
  h.state[phase].value=false;await settle()
 }
 assert.equal(h.binding.maskClosable,true);assert.equal(h.binding.closable,true);assert.equal(h.binding.keyboard,true)
})

test('外壳取消仍经原 dirty 确认，继续编辑保留草稿，明确弃稿才关闭',async t=>{
 const h=editor();t.after(h.stop);h.state.form.data[0].value='DRAFT_B'
 const keeping=h.binding.onCancel();assert.equal(h.dialogs.length,1);assert.equal(h.state.confirming.value,true)
 h.dialogs[0].onCancel();h.dialogs[0].afterClose();await keeping
 assert.deepEqual(h.events,[]);assert.equal(h.state.form.data[0].value,'DRAFT_B');assert.equal(h.state.dirty.value,true)
 const discarding=h.binding.onCancel();assert.equal(h.dialogs.length,2)
 h.dialogs[1].onOk();h.dialogs[1].afterClose();await discarding
 assert.deepEqual(h.events,[['update:open',false]]);assert.deepEqual(h.calls,[])
})

test('右侧满高和滚动规则只作用于本编辑器命名空间，不改全站 Modal',()=>{
 const styles=descriptor.styles.filter(item=>item.content.includes('kube-resource-editor-modal'))
 assert.equal(styles.length,1)
 const css=compileStyle({source:styles[0].content,filename:'KubeResourceEditor.vue',id:'data-v-kube-modal-shell',scoped:styles[0].scoped})
 assert.deepEqual(css.errors,[])
 for(const selector of css.code.matchAll(/([^{}]+)\{/g)){
  const value=selector[1].trim()
  if(value.startsWith('@'))continue
  assert.ok(value.split(',').every(part=>part.trim().startsWith('.kube-resource-editor-modal')),value)
 }
 assert.match(css.code,/\.ant-modal-content\s*\{[^}]*height:\s*100dvh/);assert.match(css.code,/overflow:\s*auto/);assert.match(css.code,/margin-inline-start:\s*auto/)
 assert.match(css.code,/transition:\s*transform 160ms ease-out/)
 assert.match(css.code,/kube-resource-editor-slide-enter-start[^}]*transform:\s*translateX\(100%\)/)
 assert.match(css.code,/kube-resource-editor-slide-leave-active[^}]*transform:\s*translateX\(100%\)/)
 assert.match(css.code,/@media\s*\(prefers-reduced-motion:\s*reduce\)[\s\S]*transition-duration:\s*1ms/)
 assert.doesNotMatch(css.code,/scale\(|zoom/)
})

test('窄屏面板内边距与吸顶偏移一致，内容不从导航上方露出',()=>{
 const shell=descriptor.styles.find(item=>item.content.includes('kube-resource-editor-modal'))
 const tabs=descriptor.styles.find(item=>item.content.includes('.workload-editor-tabs > :deep(.ant-tabs-nav)'))
 const shellCSS=compileStyle({source:shell.content,filename:'KubeResourceEditor.vue',id:'data-v-kube-modal-shell',scoped:shell.scoped})
 const tabsCSS=compileStyle({source:tabs.content,filename:'KubeResourceEditor.vue',id:'data-v-kube-modal-shell',scoped:tabs.scoped})
 assert.deepEqual(shellCSS.errors,[]);assert.deepEqual(tabsCSS.errors,[])
 assert.match(shellCSS.code,/@media\s*\(max-width:\s*760px\)\s*\{\s*\.kube-resource-editor-modal \.ant-modal-body\s*\{\s*padding:\s*16px/)
 assert.match(tabsCSS.code,/@media\s*\(max-width:\s*760px\)[\s\S]*\.workload-editor-tabs[^}]*top:\s*-16px/)
})
