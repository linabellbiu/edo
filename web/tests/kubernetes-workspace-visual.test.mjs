import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {test} from 'node:test'
import postcss from 'postcss'
import {parse,compileScript,compileStyle,compileTemplate} from '@vue/compiler-sfc'
import {renderToString} from '@vue/server-renderer'
import {patchProp} from '@vue/runtime-dom'
import * as vue from 'vue'
import * as icons from 'lucide-vue-next'
import ts from 'typescript'
import {loadFrontend} from './acceptanceHarness.mjs'

const read=path=>readFileSync(new URL(path,import.meta.url),'utf8')
const tokens=postcss.parse(read('../src/styles/kubernetes-workspace.css'))
const shared=postcss.parse(read('../src/styles/kubernetes.css'))
const names=['KubeResourceDetails','KubeNodeInfo','KubeNamespaceTree','KubeOverview','KubeNodeUsage','KubeNodeMonitor','KubeResourceCell']
const components=Object.fromEntries(names.map(name=>{
 const descriptor=parse(read(`../src/components/${name}.vue`)).descriptor
 return [name,{descriptor,styles:descriptor.styles.map(style=>postcss.parse(style.content))}]
}))
function declarations(roots,selector){
 const values={}
 for(const root of roots)root.walkRules(rule=>{if(rule.selectors.includes(selector))rule.walkDecls(decl=>values[decl.prop]=decl.value)})
 return values
}
const palette=theme=>({...declarations([tokens],':root'),...(theme==='dark'?declarations([tokens],":root[data-theme='dark']"):{})})
function luminance(color){
 return [0,2,4].map(offset=>Number.parseInt(color.slice(offset+1,offset+3),16)/255)
  .map(value=>value<=.04045?value/12.92:((value+.055)/1.055)**2.4)
  .reduce((sum,value,index)=>sum+value*[.2126,.7152,.0722][index],0)
}
const contrast=(a,b)=>(Math.max(luminance(a),luminance(b))+.05)/(Math.min(luminance(a),luminance(b))+.05)
const api=loadFrontend('../src/api/kubernetes.ts')
const roles=loadFrontend('../src/utils/kubernetesRoles.ts')
const presentation=loadFrontend('../src/utils/kubernetesPresentation.ts',{'@/api/kubernetes':api,'@/utils/kubernetesRoles':roles})
const details=loadFrontend('../src/utils/kubernetesDetails.ts',{'@/api/kubernetes':api})
const translate=key=>key
function compiledComponent(name){
 const script=compileScript(components[name].descriptor,{id:'kube-workspace-render',inlineTemplate:true}).content
 const output=ts.transpileModule(script,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022,esModuleInterop:true}}).outputText
 const modules={
  vue,'vue-i18n':{useI18n:()=>({t:translate,locale:vue.ref('zh-CN')})},'lucide-vue-next':icons,
  '@/api/kubernetes':api,'@/utils/kubernetesPresentation':presentation,'@/utils/kubernetesDetails':details,
  '@/utils/kubernetesRoles':roles,'@/utils/kubernetesNode':{nodeConditionState:()=> 'default'},
  '@/utils/time':{formatDateTime:value=>value??'—'},
  './KubeResourceCell.vue':{__esModule:true,default:{props:['cell'],setup:props=>()=>vue.h('span',props.cell?.text??'—')}},
 }
 const module={exports:{}}
 new Function('require','module','exports',output)(name=>{
  if(!Object.hasOwn(modules,name))throw new Error(`未登记的渲染测试依赖 ${name}`)
  return modules[name]
 },module,module.exports)
 return module.exports.default
}
function renderComponent(name,props){
 const app=vue.createSSRApp(compiledComponent(name),props)
 for(const tag of ['a-tooltip','a-tag','a-empty','a-alert'])app.component(tag,{inheritAttrs:false,props:['description','message'],setup:(props,{slots,attrs})=>()=>vue.h('div',attrs,props.description??props.message??slots.default?.())})
 return renderToString(app)
}

// 使用 Vue 自己的 DOM patchProp 验证 .prop 契约；实际浏览器和 axe 另行验收。
function nativeControlHarness(name,props){
 const events=[]
 const element=tag=>({nodeType:1,tagName:tag.toUpperCase(),className:'',type:'',value:'',children:[],attrs:{},listeners:{},checked:false,indeterminate:false,disabled:false,
  setAttribute(name,value){this.attrs[name]=String(value)},removeAttribute(name){delete this.attrs[name]},
  addEventListener(name,handler){this.listeners[name]=handler},removeEventListener(name){delete this.listeners[name]},
 })
 const renderer=vue.createRenderer({
  patchProp,createElement:element,createText:text=>({nodeType:3,text}),createComment:text=>({nodeType:8,text}),
  setText:(node,text)=>node.text=text,setElementText:(node,text)=>node.text=text,
  parentNode:node=>node.parent,nextSibling:node=>node.parent?.children[node.parent.children.indexOf(node)+1]??null,
  insert(node,parent,anchor){if(node.parent)node.parent.children.splice(node.parent.children.indexOf(node),1);node.parent=parent;const index=anchor?parent.children.indexOf(anchor):-1;parent.children.splice(index<0?parent.children.length:index,0,node)},
  remove(node){if(node.parent)node.parent.children.splice(node.parent.children.indexOf(node),1);node.parent=null},
 })
 const component=compiledComponent(name),root=element('div')
 const app=renderer.createApp({setup:()=>()=>vue.h(component,{...props,onSelection:value=>{events.push(['selection',value]);props.selected=value},onCluster:(...args)=>events.push(['cluster',...args]),onFollow:reference=>events.push(['follow',reference])})})
 for(const tag of ['a-input','a-checkbox','a-radio','a-spin','a-button','a-tooltip'])app.component(tag,{inheritAttrs:false,setup:(_props,{slots})=>()=>vue.h('div',slots.default?.())})
 app.mount(root)
 const find=(predicate,node=root)=>[...(predicate(node)?[node]:[]),...(node.children??[]).flatMap(child=>find(predicate,child))]
 return {events,find,stop:()=>app.unmount()}
}

test('Kubernetes 局部令牌不覆盖全站，浅深主题都具备独立可读的表面与状态色',()=>{
 const entry=read('../src/styles/kubernetes.css')
 assert.match(entry,/@import '\.\/kubernetes-workspace\.css'/)
 assert.match(entry,/@import '\.\/kubernetes-workload\.css'/)
 tokens.walkDecls(decl=>{if(decl.prop.startsWith('--'))assert.ok(decl.prop.startsWith('--kube-'),decl.prop)})
 tokens.walkRules(rule=>{
  if(rule.selectors.some(selector=>selector.startsWith(':root')))return
  for(const selector of rule.selectors)assert.match(selector,/^\.kube-(workspace|detail-drawer)/,selector)
 })
 for(const theme of ['light','dark']){
  const colors=palette(theme)
  for(const surface of ['surface','surface-soft','heading','stripe','hover','selected']){
   for(const text of ['text','muted','link'])assert.ok(contrast(colors[`--kube-${text}`],colors[`--kube-${surface}`])>=4.5,`${theme}/${text}/${surface}`)
   assert.ok(contrast(colors['--kube-focus'],colors[`--kube-${surface}`])>=3,`${theme}/focus/${surface}`)
  }
  for(const tone of ['success','warning','danger'])assert.ok(contrast(colors[`--kube-${tone}`],colors[`--kube-${tone}-bg`])>=4.5,`${theme}/${tone}`)
  for(const meter of ['cpu','memory','pods','warning','danger','unknown'])assert.ok(contrast(colors['--kube-meter-label'],colors[`--kube-meter-${meter}`])>=4.5,`${theme}/meter/${meter}`)
  assert.notEqual(colors['--kube-hover'],colors['--kube-selected'])
  assert.notEqual(colors['--kube-selected'],colors['--kube-success-bg'])
  assert.notEqual(colors['--kube-warning-bg'],colors['--kube-danger-bg'])
  assert.ok(theme==='light'?luminance(colors['--kube-heading'])>.8:luminance(colors['--kube-heading'])<.08)
 }
})

test('共用表格保留清晰表头、列线、末行线和不透明固定操作列，不靠粗框分区',()=>{
 const table='.kube-resource-table table.resource-table'
 assert.equal(declarations([shared],table)['border-collapse'],'separate')
 assert.equal(declarations([shared],`${table} th`).background,'var(--kube-heading)')
 assert.equal(declarations([shared],'.kube-resource-table .resource-table td')['border-right'],'1px solid var(--kube-border)')
 assert.equal(declarations([shared],'.kube-resource-table .resource-table tbody tr:last-child td')['border-bottom'],'1px solid var(--kube-border)')
 for(const [state,color] of [['','surface'],[':nth-child(even)','stripe'],[':hover','hover'],['.active','selected']])assert.equal(declarations([shared],`${table} tbody > tr${state} > td`).background,`var(--kube-${color})`)
 for(const css of [shared,tokens])css.walkDecls(decl=>{
  if(/^border(?:-(?:left|right|top|bottom))?$/.test(decl.prop)&&/^\d+px/.test(decl.value))assert.ok(Number.parseFloat(decl.value)<=1,decl.toString())
 })
})

test('查看、修改和执行按钮各自保留语义，hover 不影响禁用态，键盘焦点独立可见',()=>{
 for(const [kind,tone] of [['success','success'],['warning','warning']]){
  const base=declarations([shared],`.kube-action-${kind}.ant-btn:not(:disabled)`)
  const hover=declarations([shared],`.kube-action-${kind}.ant-btn:not(:disabled):hover`)
  assert.equal(base.color,`var(--kube-${tone})`);assert.equal(hover.color,base.color)
  assert.equal(hover['border-color'],base.color)
 }
 assert.equal(declarations([shared],'.kube-action.ant-btn:disabled').cursor,'not-allowed')
 assert.equal(declarations([shared],'.kube-action.ant-btn:focus-visible').outline,'2px solid var(--kube-focus)')
 assert.equal(declarations([tokens],'.kube-workspace .partial-badge').font,'inherit')
 assert.equal(declarations([tokens],'.kube-workspace .partial-badge:focus-visible').outline,'2px solid var(--kube-focus)')
 assert.doesNotMatch(read('../src/styles/kubernetes.css'),/filter:\s*brightness/)
})

test('K8s 页签激活和 processing 标签使用局部可读配对，不覆盖全站 Ant 主题',()=>{
 for(const root of ['.kube-workspace','.kube-detail-drawer']){
  const tag=declarations([tokens],`${root} .ant-tag-processing`)
  assert.equal(tag.color,'var(--kube-link)');assert.equal(tag.background,'var(--kube-selected)')
  assert.equal(declarations([tokens],`${root} .ant-tabs .ant-tabs-tab.ant-tabs-tab-active .ant-tabs-tab-btn`).color,'var(--kube-link)')
  for(const theme of ['light','dark']){
   const colors=palette(theme)
   assert.ok(contrast(colors['--kube-link'],colors['--kube-selected'])>=4.5,`${theme}/processing`)
   assert.ok(contrast(colors['--kube-link'],colors['--kube-surface-soft'])>=4.5,`${theme}/tab`)
  }
 }
 assert.equal(declarations([tokens],'.ant-tag-processing').color,undefined)
 assert.equal(declarations([tokens],'.ant-tabs-tab-active .ant-tabs-tab-btn').color,undefined)
})

test('K8s 真实组件模板与 scoped 样式可编译，辅助文字不再低于12px',()=>{
 for(const [name,{descriptor,styles}] of Object.entries(components)){
  const script=compileScript(descriptor,{id:'kube-workspace-compile'})
  assert.deepEqual(compileTemplate({source:descriptor.template.content,filename:`${name}.vue`,id:'kube-workspace-compile',compilerOptions:{bindingMetadata:script.bindings}}).errors,[],name)
  for(const style of descriptor.styles)assert.deepEqual(compileStyle({source:style.content,filename:`${name}.vue`,id:'kube-workspace-compile',scoped:true}).errors,[],name)
  for(const style of styles)style.walkDecls('font-size',decl=>{if(/^\d+px$/.test(decl.value))assert.ok(Number.parseFloat(decl.value)>=12,`${name}/${decl.toString()}`)})
  assert.doesNotMatch(descriptor.template.content,/v-html/,name)
 }
})

test('配置、存储、网络和节点详情横向表都可聚焦、有名称且列头具有语义',()=>{
 for(const [name,count] of [['KubeResourceDetails',9],['KubeNodeInfo',2]]){
  const template=components[name].descriptor.template.content
  const wrappers=[...template.matchAll(/<div\b[^>]*class="detail-table-scroll"[^>]*>/g)].map(match=>match[0])
  assert.equal(wrappers.length,count,name)
  for(const wrapper of wrappers){assert.match(wrapper,/tabindex="0"/);assert.match(wrapper,/role="region"/);assert.match(wrapper,/:aria-label="t\('k8s\.[A-Za-z]+'\)"/)}
  assert.equal(new Set(wrappers.map(wrapper=>wrapper.match(/:aria-label="([^"]+)"/)[1])).size,count)
  assert.doesNotMatch(template,/<th>/)
  assert.equal(declarations(components[name].styles,'.detail-table-scroll:focus-visible').outline,'2px solid var(--kube-focus)')
  assert.equal(declarations(components[name].styles,'.kube-detail-table').width,'100%')
  assert.equal(declarations(components[name].styles,'.detail-table-scroll')['overflow-x'],'auto')
 }
})

test('网络详情真实SSR保留关联权限和安全文本，滚动区域不成为修改入口',async()=>{
 const literal='<img src=x onerror=alert(1)>',object={kind:'Ingress',metadata:{uid:'ingress-test',name:'public',namespace:'preview'},spec:{rules:[{host:literal,http:{paths:[{path:'/',pathType:'Prefix',backend:{service:{name:'gateway',port:{number:8080}}}}]}}]}}
 const base={object,type:api.kubeTypes.find(type=>type.kind==='Ingress')}
 const denied=await renderComponent('KubeResourceDetails',{...base,canFollow:()=>false})
 assert.match(denied,/tabindex="0" role="region" aria-label="k8s.routes"/)
 assert.match(denied,/&lt;img src=x onerror=alert\(1\)&gt;/);assert.doesNotMatch(denied,/<img|class="detail-link"/)
 const allowed=await renderComponent('KubeResourceDetails',{...base,canFollow:reference=>reference?.resource==='services'})
 assert.match(allowed,/<button type="button" class="detail-link">gateway<\/button>/)
 assert.match(allowed,/<th scope="col">k8s.hostname<\/th>/)
 assert.doesNotMatch(components.KubeResourceDetails.descriptor.scriptSetup.content,/client\.(post|patch|delete)|fetch\(/)
})

test('节点详情真实SSR只转换展示单位，保留原始精度、未知值和不可信文本',async()=>{
 const node={kind:'Node',metadata:{uid:'node-test',name:'worker',labels:{}},spec:{taints:[{key:'example.test/role',value:'<script>unsafe</script>',effect:'NoSchedule'}]},status:{nodeInfo:{architecture:'arm64'},capacity:{cpu:'4',memory:'1024Mi',pods:'110'},allocatable:{cpu:'3950m',memory:'1000Mi'}}}
 const before=structuredClone(node),html=await renderComponent('KubeNodeInfo',{node})
 assert.match(html,/aria-label="k8s.nodeCapacity"/);assert.match(html,/aria-label="k8s.taints"/)
 assert.match(html,/title="1024Mi">1 GiB/);assert.match(html,/title="1000Mi">1000 MiB/)
 assert.match(html,/<td title(?:="")?>—<\/td>/);assert.doesNotMatch(html,/<script>unsafe/)
 assert.match(html,/&lt;script&gt;unsafe&lt;\/script&gt;/);assert.deepEqual(node,before)
})

test('范围树保留选择边界，禁用不发事件，集群级页面不操作命名空间',async()=>{
 const component=loadFrontend('../src/components/KubeNamespaceTree.vue',{vue,'vue-i18n':{useI18n:()=>({t:translate})}}).default
 const props=vue.reactive({clusters:[{id:'a',name:'preview',is_active:true},{id:'b',name:'offline',is_active:false}],cluster:'a',namespaces:['preview','prod'],selected:['preview'],disabled:true}),events=[],scope=vue.effectScope()
 const state=scope.run(()=>component.setup(props,{expose(){},emit:(...args)=>events.push(args)}))
 try{
  state.chooseCluster('b');state.toggleAll(props.clusters[0]);state.toggleNamespace('prod',true);assert.deepEqual(events,[])
  props.disabled=false;props.namespaced=false;await vue.nextTick()
  state.toggleAll(props.clusters[0]);state.toggleNamespace('prod',true);assert.deepEqual(events,[])
  assert.deepEqual(props.selected,['preview']);assert.deepEqual(state.visibleNamespaces.value,[])
  props.namespaced=true;await vue.nextTick();state.toggleNamespace('prod',true)
  assert.deepEqual(events,[['selection',['preview','prod']]])
 }finally{scope.stop()}
 const style=components.KubeNamespaceTree.styles
 assert.ok(Number.parseFloat(declarations(style,'.namespace-expander').width)>=24)
 assert.ok(Number.parseFloat(declarations(style,'.namespace-expander')['min-height'])>=24)
 assert.equal(declarations(style,'.namespace-tree-item:not(.selected):not(.is-disabled):hover').background,'var(--kube-hover)')
 assert.equal(declarations(style,'.namespace-tree-item.selected').background,'var(--kube-selected)')
 assert.equal(declarations(style,'.namespace-cluster-name:disabled').cursor,'not-allowed')
 assert.match(components.KubeNamespaceTree.descriptor.template.content,/<Search aria-hidden="true"/)
})

test('集群全选用 Vue 原生属性同步未选、半选和全选，保留 mixed ARIA 和禁用守卫',async()=>{
 const props=vue.reactive({clusters:[{id:'a',name:'preview',is_active:true}],cluster:'a',namespaces:['preview','prod'],selected:['preview'],namespaced:true,disabled:false}),h=nativeControlHarness('KubeNamespaceTree',props)
 const input=()=>h.find(node=>node.tagName==='INPUT')[0]
 try{
  let checkbox=input()
  assert.ok(checkbox,JSON.stringify(h.find(node=>node.nodeType===1).map(node=>({tag:node.tagName,class:node.className,text:node.text}))));assert.equal(checkbox.indeterminate,true);assert.equal(checkbox.checked,false)
  assert.equal(checkbox.attrs['aria-checked'],'mixed');assert.equal(checkbox.attrs.indeterminate,undefined)
  assert.ok(checkbox.attrs['aria-label']);assert.equal(checkbox.disabled,false)
  props.selected=['*'];await vue.nextTick();assert.equal(checkbox.indeterminate,false);assert.equal(checkbox.checked,true);assert.equal(checkbox.attrs['aria-checked'],undefined)
  props.selected=[];await vue.nextTick();assert.equal(checkbox.indeterminate,false);assert.equal(checkbox.checked,false)
  props.selected=['prod'];await vue.nextTick();assert.equal(checkbox.indeterminate,true);assert.equal(checkbox.attrs['aria-checked'],'mixed')
  checkbox.listeners.change({target:checkbox,_vts:Date.now()+1});await vue.nextTick()
  assert.deepEqual(h.events,[['selection',['*']]]);assert.equal(checkbox.indeterminate,false);assert.equal(checkbox.checked,true)
  checkbox.listeners.change({target:checkbox,_vts:Date.now()+1});await vue.nextTick()
  assert.deepEqual(h.events.at(-1),['selection',[]]);assert.equal(checkbox.checked,false)
  props.disabled=true;await vue.nextTick();assert.equal(checkbox.disabled,true)
  const before=h.events.length;checkbox.listeners.change({target:checkbox,_vts:Date.now()+1});assert.equal(h.events.length,before)
  props.disabled=false;props.clusters[0].is_active=false;await vue.nextTick();assert.equal(checkbox.disabled,true)
  checkbox.listeners.change({target:checkbox,_vts:Date.now()+1});assert.equal(h.events.length,before)
  props.namespaced=false;await vue.nextTick();assert.equal(input(),undefined)
 }finally{h.stop()}
 const css=components.KubeNamespaceTree.styles
 assert.equal(declarations(css,'.namespace-cluster-checkbox').width,'24px')
 assert.ok(Number.parseFloat(declarations(css,'.namespace-cluster-checkbox')['min-height'])>=24)
 assert.equal(declarations(css,'.namespace-cluster-checkbox input:focus-visible').outline,'2px solid var(--kube-focus)')
 assert.doesNotMatch(components.KubeNamespaceTree.descriptor.scriptSetup.content,/querySelector|document\./)
})

test('K8s 资源关联链接有局部可读颜色和焦点，撤销查看权限后恢复静态文本',async()=>{
 const reference={resource:'services',namespace:'preview',name:'gateway'},props=vue.reactive({cell:{text:'gateway',reference},canFollow:true}),h=nativeControlHarness('KubeResourceCell',props)
 try{
  const button=h.find(node=>node.tagName==='BUTTON'&&node.className==='cell-link')[0]
  assert.ok(button);assert.equal(button.type,'button')
  button.listeners.click({target:button,_vts:Date.now()+1});assert.deepEqual(h.events,[['follow',reference]])
  props.canFollow=false;await vue.nextTick();assert.equal(h.find(node=>node.tagName==='BUTTON').length,0)
  assert.ok(h.find(node=>node.className==='cell-primary').length)
 }finally{h.stop()}
 const css=components.KubeResourceCell.styles
 assert.equal(declarations(css,'.cell-link').color,'var(--kube-link)')
 assert.equal(declarations(css,'.cell-link:focus-visible').outline,'2px solid var(--kube-focus)')
 for(const theme of ['light','dark']){
  const colors=palette(theme)
  for(const surface of ['surface','stripe','hover','selected'])assert.ok(contrast(colors['--kube-link'],colors[`--kube-${surface}`])>=4.5,`${theme}/cell-link/${surface}`)
 }
})

test('节点用量真实SSR保留超配百分比和完整数值，缺测不渲染为零或健康色',async()=>{
 const node={kind:'Node',metadata:{uid:'node-usage-test',name:'worker'}},at=new Date().toISOString()
 const overview={collected_at:at,counts:{nodes:{count:1,state:'ok'}},nodes:[{uid:node.metadata.uid,name:'worker',cpu:4,memory:1024**3,pods:12,pod_capacity:10,pod_state:'ok'}],usage:{nodes:{worker:{cpu:5,memory:2*1024**3,timestamp:at}}},metrics_state:{nodes:'ok'}}
 const html=await renderComponent('KubeNodeUsage',{node,overview})
 assert.match(html,/aria-valuemax="125" aria-valuenow="125"/)
 assert.match(html,/aria-valuemax="200" aria-valuenow="200"/)
 assert.match(html,/5 CPU \/ 4 CPU/);assert.match(html,/2 GiB \/ 1 GiB/)
 const unknown=await renderComponent('KubeNodeUsage',{node,overview:null})
 assert.doesNotMatch(unknown,/role="meter"|aria-valuenow|>0(?:\.0)?%/)
 assert.equal((unknown.match(/tone-unknown/g)??[]).length,3)
 assert.equal(declarations(components.KubeNodeUsage.styles,'.node-usage:focus-visible').outline,'2px solid var(--kube-focus)')
 assert.equal(declarations(components.KubeNodeUsage.styles,'.usage-meter').height,'24px')
})

test('节点监控真实图表配置使用12px轴文字、足够内边距和浅深主题配对',async()=>{
 const helpers=loadFrontend('../src/utils/kubernetesNode.ts')
 for(const theme of ['light','dark']){
  const colors=palette(theme),options=new Map(),scope=vue.effectScope()
  const component=loadFrontend('../src/components/KubeNodeMonitor.vue',{
   vue:{...vue,onMounted(){},onBeforeUnmount(){}},'vue-i18n':{useI18n:()=>({t:translate,locale:vue.ref('zh-CN')})},
   '@/api/kubernetes':api,'@/utils/kubernetesNode':helpers,'@/stores/preferences':{usePreferencesStore:()=>({theme})},
   'echarts/core':{use(){},init:element=>({setOption:option=>options.set(element.selector,option),resize(){}})},
  },{getComputedStyle:()=>({getPropertyValue:name=>colors[name]??''})}).default
  const state=scope.run(()=>component.setup({cluster:'test',node:{metadata:{name:'worker',uid:'worker-test'}},autoRefresh:false,workloads:true},{expose(){}}))
  try{
   const now=Date.now(),at=new Date(now).toISOString()
   state.now.value=now;state.history.value={from:new Date(now-900_000).toISOString(),to:at,interval_seconds:30,points:[{collected_at:at,metrics_at:at,cpu:5,cpu_allocatable:4,memory:1024**3,memory_allocatable:2*1024**3,pods:3,pod_capacity:10,metrics_state:'ok',pods_state:'ok',allocation:{cpu_requests:2,cpu_limits:8,memory_requests:512*1024**2,memory_limits:4*1024**3}}]}
   state.root.value={querySelector:selector=>({selector,clientWidth:420})}
   await state.render()
   assert.equal(options.size,6)
   for(const key of ['cpu','memory','pods']){
    const comparison=options.get(`[data-comparison="${key}"]`),trend=options.get(`[data-chart="${key}"]`)
    for(const option of [comparison,trend]){
     assert.equal(option.xAxis.axisLabel.fontSize,12);assert.equal(option.yAxis.axisLabel.fontSize,12)
     assert.equal(option.tooltip.backgroundColor,colors['--kube-surface'])
     assert.equal(option.tooltip.textStyle.fontSize,12)
     assert.ok(contrast(option.tooltip.textStyle.color,option.tooltip.backgroundColor)>=4.5)
    }
    assert.equal(comparison.series[0].label.fontSize,12)
    assert.ok(comparison.grid.left>=90);assert.ok(comparison.grid.right>=80)
    for(const bar of comparison.series[0].data)assert.ok(contrast(bar.itemStyle.color,colors['--kube-surface'])>=3,`${theme}/${key}/bar`)
   }
   assert.deepEqual(options.get('[data-comparison="cpu"]').series[0].data.map(row=>row.value),[5,4,2,8])
   assert.equal(state.cards.value[0].percent,125)
  }finally{scope.stop()}
 }
 const css=components.KubeNodeMonitor.styles
 assert.equal(declarations(css,'.monitor-card').background,'var(--kube-surface)')
 assert.equal(declarations(css,'.monitor-card')['border-radius'],'6px')
 assert.ok(css.some(root=>root.nodes.some(node=>node.type==='atrule'&&node.name==='container'&&node.params.includes('960px'))))
})
