import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {test} from 'node:test'
import {parse,compileScript,compileStyle,compileTemplate} from '@vue/compiler-sfc'
import postcss from 'postcss'
import ts from 'typescript'
import * as vue from 'vue'
import {renderToString} from '@vue/server-renderer'
import {loadFrontend} from './acceptanceHarness.mjs'

const sharedSource=readFileSync(new URL('../src/styles/kubernetes-workload.css',import.meta.url),'utf8')
const shared=postcss.parse(sharedSource)
const files=['KubeWorkloadExplorer','KubeWorkloadPodRuntime','KubeWorkloadSummary','KubeWorkloadEvents']
const components=Object.fromEntries(files.map(name=>{
 const source=readFileSync(new URL(`../src/components/${name}.vue`,import.meta.url),'utf8'),descriptor=parse(source).descriptor
 return [name,{source,descriptor,template:descriptor.template.content,styles:descriptor.styles.map(style=>postcss.parse(style.content))}]
}))
function rules(roots){
 const result=[]
 for(const root of roots)root.walkRules(rule=>{
  const declarations=Object.fromEntries(rule.nodes.filter(node=>node.type==='decl').map(decl=>[decl.prop,decl.value]))
  result.push({selectors:rule.selectors,declarations})
 })
 return result
}
function declarationsFor(roots,selector){
 return Object.assign({},...rules(roots).filter(rule=>rule.selectors.includes(selector)).map(rule=>rule.declarations))
}
function palette(theme){
 return {...declarationsFor([shared],':root'),...(theme==='dark'?declarationsFor([shared],":root[data-theme='dark']"):{})}
}
function channels(hex){
 assert.match(hex,/^#[\da-f]{3}(?:[\da-f]{3})?$/i)
 const value=hex.length===4?[...hex.slice(1)].map(char=>char+char).join(''):hex.slice(1)
 return [0,2,4].map(offset=>Number.parseInt(value.slice(offset,offset+2),16)/255).map(value=>value<=.04045?value/12.92:((value+.055)/1.055)**2.4)
}
function contrast(first,second){
 const luminance=hex=>channels(hex).reduce((sum,value,index)=>sum+value*[.2126,.7152,.0722][index],0)
 const values=[luminance(first),luminance(second)].sort((left,right)=>right-left)
 return (values[0]+.05)/(values[1]+.05)
}
function templateNodes(node,predicate){
 const result=[]
 if(node.type===1&&predicate(node))result.push(node)
 for(const child of node.children??[])result.push(...templateNodes(child,predicate))
 return result
}
function attribute(node,name){return node.props.find(prop=>prop.type===6&&prop.name===name)?.value?.content}
function directive(node,name,arg){return node.props.find(prop=>prop.type===7&&prop.name===name&&(arg===undefined||prop.arg?.content===arg))?.exp?.content}
function hasClass(node,name){return (attribute(node,'class')??'').split(/\s+/).includes(name)}

test('工作负载配色由自足入口管理，中性分区、浅蓝选中与低亮度暗色面板分别定义',()=>{
 const entry=readFileSync(new URL('../src/styles/kubernetes.css',import.meta.url),'utf8')
 const entryRules=postcss.parse(entry).nodes,importIndex=entryRules.findIndex(node=>node.type==='atrule'&&node.name==='import'&&/['"]\.\/kubernetes-workload\.css['"]/.test(node.params))
 assert.ok(importIndex>=0);assert.ok(importIndex<entryRules.findIndex(node=>node.type==='rule'))
 const expected={
  light:{accent:'#4673c4','on-accent':'#fff',selected:'#edf3fc','on-selected':'#315fae','selected-border':'#8aa7d0',idle:'#fff','idle-border':'#d3dbe6',text:'#27364b',hover:'#f3f6fa','on-hover':'#27364b','hover-border':'#aebed1',heading:'#e6ecf3',title:'#1d2d44',column:'#fff',border:'#ccd5e1',divider:'#c4cfdd',stripe:'#f8f9fb',link:'#315fae',muted:'#58677b','chart-cpu':'#258870'},
  dark:{accent:'#5077b6','on-accent':'#fff',selected:'#263d5d','on-selected':'#c6dbff','selected-border':'#6887b0',idle:'#1b2532',text:'#e1e8f2',hover:'#263448','on-hover':'#e1e8f2','hover-border':'#61748d',heading:'#293548',title:'#f1f5fb',column:'#1b2532',border:'#42526b',divider:'#42526b',stripe:'#202a36',link:'#a8c7ff',muted:'#abb9ca','chart-cpu':'#5bbba1'},
 }
 for(const [theme,tokens] of Object.entries(expected))for(const [name,value] of Object.entries(tokens))assert.equal(palette(theme)[`--workload-${name}`],value,`${theme}/${name}`)
 const light=palette('light'),dark=palette('dark')
 assert.deepEqual(Object.keys(dark).sort(),Object.keys(light).sort())
 for(const name of Object.keys(light))assert.ok(Object.hasOwn(declarationsFor([shared],":root[data-theme='dark']"),name),`暗色需要显式覆盖 ${name}`)
 const references=[...Object.values(components).flatMap(component=>component.styles.map(style=>style.toString())),readFileSync(new URL('../src/components/KubeResourceDiagnostics.vue',import.meta.url),'utf8')].join('\n')
 for(const name of new Set(references.match(/--workload-[a-z-]+/g)))assert.ok(Object.hasOwn(light,name),`配色引用 ${name} 缺少定义`)
})

test('选中、悬停、普通内容与中性表头正文对比度充足，不把实底主色当文字色',()=>{
 for(const theme of ['light','dark']){
  const colors=palette(theme),pairs=[['on-accent','accent'],['on-selected','selected'],['text','selected'],['text','hover'],['title','column'],['title','heading'],['text','idle'],['text','column'],['text','heading'],['muted','heading'],['muted','column'],['muted','selected'],['muted','hover'],['link','column']]
  for(const state of ['ready','warning','error','neutral','progress'])pairs.push([`${state}-text`,`${state}-bg`])
  for(const [foreground,background] of pairs)assert.ok(contrast(colors[`--workload-${foreground}`],colors[`--workload-${background}`])>=4.5,`${theme}/${foreground}/${background} 对比度不足`)
  if(theme==='light')assert.ok(contrast('#fff',colors['--workload-hover'])<4.5,'中性浅色悬停必须使用深色正文')
 }
})

test('大面积表面保持浅色或低亮度，不把选中、悬停和健康色块混为一谈',()=>{
 const luminance=hex=>channels(hex).reduce((sum,value,index)=>sum+value*[.2126,.7152,.0722][index],0)
 for(const theme of ['light','dark']){
  const colors=palette(theme)
  for(const surface of ['selected','hover','heading','column','idle']){
   const value=luminance(colors[`--workload-${surface}`])
   assert.ok(theme==='light'?value>(surface==='heading'?.8:.85):value<.08,`${theme}/${surface} 表面过深或过亮`)
  }
  assert.notEqual(colors['--workload-selected'],colors['--workload-hover'])
  assert.notEqual(colors['--workload-selected'],colors['--workload-ready-bg'])
  assert.notEqual(colors['--workload-hover'],colors['--workload-ready-bg'])
  assert.notEqual(colors['--workload-on-hover'],colors['--workload-ready-text'])
  assert.equal(colors['--workload-on-hover'],colors['--workload-text'])
  const [red,green,blue]=[0,2,4].map(offset=>Number.parseInt(colors['--workload-hover'].slice(1+offset,3+offset),16))
  assert.ok(blue>=green&&green>=red,`${theme} 悬停保持中性冷灰而非健康绿色`)
 }
})

test('版本与 Pod 只强调选中名称，正文保持深灰，悬停不改写选中、禁用或健康状态',()=>{
 const roots=components.KubeWorkloadExplorer.styles,entries=rules(roots)
 for(const name of ['revision-item','pod-item']){
  const selected=declarationsFor(roots,`.${name}.selected`)
  assert.match(selected.background,/var\(--workload-selected\)/,name)
  assert.equal(selected.color,'var(--workload-text)',name)
  assert.equal(selected['box-shadow'],'inset 2px 0 var(--workload-accent)',name)
  const titleSelector=name==='revision-item'?'.revision-item.selected .revision-heading strong':'.pod-item.selected .pod-heading strong'
  assert.equal(declarationsFor(roots,titleSelector).color,'var(--workload-on-selected)')
  const hover=entries.filter(rule=>rule.selectors.some(selector=>selector.includes(`.${name}`)&&selector.includes(':hover')))
  assert.ok(hover.length,`${name} 缺少悬停规则`)
  for(const rule of hover)for(const selector of rule.selectors.filter(selector=>selector.includes(`.${name}`)&&selector.includes(':hover'))){
   assert.ok(selector.includes(':not(.selected)'),`${selector} 不能覆盖选中状态`)
   assert.ok(selector.includes(':not(:disabled)'),`${selector} 不能高亮禁用项`)
   const active=declarationsFor(roots,selector)
   assert.match(active.background,/var\(--workload-hover\)/)
   assert.equal(active.color,'var(--workload-text)')
   assert.match(active['border-color'],/var\(--workload-hover-border\)/)
  }
 }
 const focus=entries.filter(rule=>rule.selectors.some(selector=>selector.includes(':focus-visible')))
 assert.ok(focus.some(rule=>/2px solid var\(--workload-focus\)/.test(rule.declarations.outline??'')&&Number.parseFloat(rule.declarations['outline-offset'])>0),'键盘焦点必须有独立外圈，不借用选中或状态色')
 assert.equal(declarationsFor(roots,'.replica-counts').color,'var(--workload-muted)')
 assert.equal(declarationsFor(roots,'.replica-counts b').color,'var(--workload-text)')
 assert.equal(declarationsFor(roots,'.pod-placement').color,'var(--workload-muted)')
 assert.equal(declarationsFor(roots,'.pod-placement code').color,'var(--workload-text)')
 assert.equal(declarationsFor(roots,'.pod-reason').color,'var(--workload-muted)')
 const disabled=entries.filter(rule=>rule.selectors.some(selector=>/:disabled/.test(selector)&&!selector.includes(':not(:disabled)')))
 assert.ok(disabled.some(rule=>rule.declarations.cursor==='not-allowed'))
 for(const rule of disabled)assert.doesNotMatch(JSON.stringify(rule.declarations),/--workload-hover|--workload-ready/)
})

test('事件、运行标题与分列使用共同中性层次，容器操作保持描边链接而非整块蓝绿底',()=>{
 const event=declarationsFor(components.KubeWorkloadEvents.styles,'.event-table th')
 assert.match(event.background,/var\(--workload-heading\)/);assert.match(event.color,/var\(--workload-title\)/)
 const runtime=declarationsFor(components.KubeWorkloadPodRuntime.styles,'.runtime-pod-title')
 assert.match(runtime.background,/var\(--workload-heading\)/);assert.match(runtime.color,/var\(--workload-title\)/)
 const container=declarationsFor(components.KubeWorkloadPodRuntime.styles,'.runtime-container.selected')
 assert.equal(container.background,'var(--workload-column)');assert.equal(container['box-shadow'],'none')
 assert.equal(declarationsFor(components.KubeWorkloadPodRuntime.styles,'.runtime-container.selected .container-heading>strong').color,'var(--workload-on-selected)')
 const action=declarationsFor(components.KubeWorkloadPodRuntime.styles,'.container-action-bar :deep(.ant-btn:not(:disabled))')
 assert.equal(action.background,'var(--workload-column)');assert.equal(action.color,'var(--workload-link)')
 const actionHover=declarationsFor(components.KubeWorkloadPodRuntime.styles,'.container-action-bar :deep(.ant-btn:not(:disabled):hover)')
 assert.equal(actionHover.background,'var(--workload-selected)');assert.equal(actionHover.color,'var(--workload-link)')
 const explorer=components.KubeWorkloadExplorer
 assert.equal(declarationsFor(explorer.styles,'.workload-explorer').background,'var(--workload-column)')
 assert.equal(declarationsFor(explorer.styles,'.explorer-column').background,'var(--workload-column)')
 assert.equal(declarationsFor(explorer.styles,'.explorer-column>header').background,'var(--workload-heading)')
 assert.equal(declarationsFor(explorer.styles,'.pod-detail-column').background,'var(--workload-column)')
 const boundaries=rules(explorer.styles)
 assert.ok(boundaries.some(rule=>rule.selectors.includes('.pod-column')&&rule.declarations['border-left']==='1px solid var(--workload-divider)'))
 assert.ok(boundaries.some(rule=>rule.selectors.includes('.pod-detail-column')&&rule.declarations['border-left']==='1px solid var(--workload-divider)'))
 assert.ok(boundaries.some(rule=>rule.selectors.includes('.pod-column')&&rule.declarations['border-top']==='1px solid var(--workload-divider)'))
 for(const rule of boundaries.filter(rule=>rule.selectors.some(selector=>['.pod-column','.pod-detail-column','.explorer-column'].includes(selector))))for(const side of ['border-left','border-right','border-top','border-bottom'])if(rule.declarations[side])assert.ok(Number.parseFloat(rule.declarations[side])<=1,`${rule.selectors.join(',')} 不得使用粗分隔条`)
 assert.equal(declarationsFor(explorer.styles,'.pod-placement svg').color,'var(--workload-muted)')
})

test('四个工作负载 SFC 模板与局部样式可编译，标题、数据和辅助信息有稳定字体层次',()=>{
 for(const [name,component] of Object.entries(components)){
  const script=compileScript(component.descriptor,{id:'workload-density-test'})
  assert.deepEqual(compileTemplate({source:component.template,filename:`${name}.vue`,id:'workload-density-test',compilerOptions:{bindingMetadata:script.bindings}}).errors,[],name)
  for(const style of component.descriptor.styles)assert.deepEqual(compileStyle({source:style.content,filename:`${name}.vue`,id:'workload-density-test',scoped:style.scoped}).errors,[],name)
 }
 for(const [name,selector] of [['KubeWorkloadExplorer','.explorer-column h4'],['KubeWorkloadExplorer','.revision-heading strong'],['KubeWorkloadExplorer','.pod-heading strong'],['KubeWorkloadEvents','.events-heading h4'],['KubeWorkloadPodRuntime','.container-heading>strong']])assert.equal(declarationsFor(components[name].styles,selector)['font-size'],'13px',`${name}/${selector}`)
 for(const [name,selector] of [['KubeWorkloadExplorer','.replica-counts b'],['KubeWorkloadEvents','.event-table'],['KubeWorkloadPodRuntime','.runtime-pod-facts dd']])assert.equal(declarationsFor(components[name].styles,selector)['font-size'],'12px',`${name}/${selector}`)
 assert.equal(declarationsFor(components.KubeWorkloadSummary.styles,'dd')['font-size'],'13px')
 for(const [name,selector] of [['KubeWorkloadExplorer','.revision-created'],['KubeWorkloadExplorer','.pod-placement'],['KubeWorkloadExplorer','.pod-created'],['KubeWorkloadEvents','.event-time'],['KubeWorkloadSummary','dt']])assert.equal(declarationsFor(components[name].styles,selector)['font-size'],'12px',`${name}/${selector}`)
 assert.equal(declarationsFor(components.KubeWorkloadExplorer.styles,'.revision-entry').margin,'0 0 12px')
 assert.equal(declarationsFor(components.KubeWorkloadExplorer.styles,'.pod-list').gap,'8px')
 assert.equal(declarationsFor(components.KubeWorkloadExplorer.styles,'.replica-counts').display,'grid')
 assert.equal(declarationsFor(components.KubeWorkloadExplorer.styles,'.replica-counts')['grid-template-columns'],'repeat(3,minmax(0,1fr))')
 const ast=components.KubeWorkloadExplorer.descriptor.template.ast,heading=templateNodes(ast,node=>hasClass(node,'revision-heading'))[0],counts=templateNodes(ast,node=>hasClass(node,'replica-counts'))[0]
 assert.equal(templateNodes(counts,node=>hasClass(node,'revision-current')).length,0)
 const current=templateNodes(heading,node=>hasClass(node,'revision-current'))
 assert.equal(current.length,1);assert.equal(directive(current[0],'if'),'item.current')
 assert.equal(counts.children.filter(node=>node.type===1&&node.tag==='span').length,3)
 for(const value of ['desired','replicas','ready'])assert.ok(counts.loc.source.includes(`count(item.${value})`))
})

test('Pod 位置使用可访问的中性 Network/Server 图标，创建时间并入同一信息行',()=>{
 const component=components.KubeWorkloadExplorer,ast=component.descriptor.template.ast
 const placement=templateNodes(ast,node=>hasClass(node,'pod-placement'))
 assert.equal(placement.length,1)
 for(const [tag,value] of [['Network',"pod.status?.podIP || '—'"],['Server',"pod.spec?.nodeName || '未调度'"]]){
  const icons=templateNodes(placement[0],node=>node.tag===tag)
  assert.equal(icons.length,1,tag);assert.equal(attribute(icons[0],'aria-hidden'),'true')
  const groups=templateNodes(placement[0],node=>node.tag==='span'&&node.children.includes(icons[0]))
  assert.equal(groups.length,1,tag);assert.equal(attribute(groups[0],'aria-label'),undefined)
  assert.ok(groups[0].loc.source.includes(`{{ ${value} }}`),`${tag} 保留真实值供屏幕阅读器读取`)
  if(tag==='Network')assert.equal(attribute(groups[0],'title'),'Pod IP')
  else assert.equal(directive(groups[0],'bind','title'),'pod.spec?.nodeName')
 }
 const dates=templateNodes(ast,node=>hasClass(node,'pod-created'))
 assert.equal(dates.length,1);assert.equal(dates[0].tag,'time')
 assert.equal(templateNodes(placement[0],node=>hasClass(node,'pod-created')).length,1)
 assert.equal(directive(dates[0],'bind','title'),'formatDateTime(pod.metadata.creationTimestamp)')
 assert.doesNotMatch(component.template,/placement-container|placement-host|<i[^>]*>[CH]<\/i>/)
 assert.equal(templateNodes(placement[0],node=>node.tag==='i').length,0)
 assert.equal(declarationsFor(component.styles,'.pod-placement').color,'var(--workload-muted)')
 assert.equal(declarationsFor(component.styles,'.pod-placement svg').color,'var(--workload-muted)')
})

test('事件表按内容自适应并有最大高度，紧凑空状态不再制造固定空白或挤出无效滚动',()=>{
 const event=components.KubeWorkloadEvents
 for(const selector of ['.event-table-scroll','.compact .event-table-scroll']){
  const values=declarationsFor(event.styles,selector)
  assert.equal(values.height,'auto',selector)
  assert.ok(Number.parseFloat(values['max-height'])>0,selector)
  assert.equal(values['min-height'],undefined,selector)
 }
 for(const rule of rules(event.styles).filter(rule=>rule.selectors.some(selector=>selector.includes('.event-table-scroll'))))if(rule.declarations.height)assert.equal(rule.declarations.height,'auto',rule.selectors.join(','))
 assert.equal(declarationsFor(event.styles,'.event-table td.events-empty').height,'72px')
 assert.equal(declarationsFor(event.styles,'.compact .event-table td.events-empty').height,'56px')
 assert.doesNotMatch(components.KubeWorkloadSummary.styles.map(style=>style.toString()).join('\n'),/event-table-scroll[^{}]*\{[^}]*height\s*:\s*\d+px/)
 const ast=event.descriptor.template.ast,count=templateNodes(ast,node=>hasClass(node,'events-count'))[0],footer=templateNodes(ast,node=>node.tag==='footer')[0]
 assert.ok(count);assert.equal(directive(count,'if'),'!loading && !error')
 assert.match(count.loc.source,/\{\{ rows.length \}\}/)
 assert.ok(footer);assert.equal(directive(footer,'if'),'loading || error')
 assert.match(footer.loc.source,/事件条数暂不可用/);assert.match(footer.loc.source,/上次读取的事件/);assert.match(footer.loc.source,/更新中/)
 const alert=templateNodes(ast,node=>node.tag==='a-alert')[0]
 assert.equal(directive(alert,'if'),'error');assert.equal(attribute(alert,'type'),'warning')
})

test('真实渲染的事件计数仅显示已知条数，加载与失败保留风险提示及旧事件而不伪装成功',async()=>{
 const script=compileScript(components.KubeWorkloadEvents.descriptor,{id:'workload-events-render-test',inlineTemplate:true}).content
 const output=ts.transpileModule(script,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022,esModuleInterop:true}}).outputText,module={exports:{}}
 const time=loadFrontend('../src/utils/time.ts')
 new Function('require','module','exports',output)(name=>{if(name==='vue')return vue;if(name==='@/utils/time')return time;throw new Error(`意外的事件测试依赖：${name}`)},module,module.exports)
 const render=async props=>{
  const app=vue.createSSRApp(module.exports.default,props)
  app.component('a-spin',{setup:(_props,{slots})=>()=>vue.h('div',slots.default?.())})
  app.component('a-alert',{props:['message'],setup:props=>()=>vue.h('div',{role:'alert'},props.message)})
  return renderToString(app)
 }
 const events=[{metadata:{uid:'known-1'},count:0,reason:'Started',message:'已启动'},{metadata:{uid:'known-2'},series:{count:9},type:'Warning',reason:'Failed',message:'重试中'}]
 const loaded=await render({events,compact:true})
 assert.match(loaded,/class="events-count">2<\/span>/);assert.doesNotMatch(loaded,/class="events-footer"/)
 assert.match(loaded,/>0<\/td>/);assert.match(loaded,/>9<\/td>/);assert.match(loaded,/warning-event/)
 const empty=await render({events:[],compact:true})
 assert.match(empty,/class="events-count">0<\/span>/);assert.match(empty,/暂无事件/);assert.doesNotMatch(empty,/class="events-footer"/)
 for(const props of [{loading:true},{error:true,errorText:'事件读取失败'}]){
  const retained=await render({events,...props}),unavailable=await render({events:[],...props})
  for(const html of [retained,unavailable]){assert.doesNotMatch(html,/class="events-count"/);assert.match(html,/class="events-footer"/)}
  assert.match(retained,/上次读取的事件 2 条/);assert.match(retained,/已启动/);assert.match(retained,/重试中/)
  if(props.loading){assert.match(retained,/更新中/);assert.match(unavailable,/正在读取事件/)}
  else {assert.match(retained,/role="alert"[^>]*>事件读取失败/);assert.match(unavailable,/事件条数暂不可用/);assert.match(unavailable,/事件暂不可用/)}
 }
})

test('真实状态色与选中和悬停分开，未知条件、失败条件及零副本不伪装为健康',()=>{
 const entries=rules([shared]),states={success:'ready',warning:'warning',error:'error',default:'neutral',processing:'progress'}
 for(const [tone,state] of Object.entries(states)){
  const rule=entries.find(entry=>entry.selectors.some(selector=>selector.endsWith(`.ant-tag-${tone}`)))
  assert.ok(rule,`缺少 ${tone} 状态样式`)
  assert.equal(rule.declarations.background,`var(--workload-${state}-bg)`)
  assert.equal(rule.declarations.color,`var(--workload-${state}-text)`)
  for(const selector of rule.selectors)assert.doesNotMatch(selector,/\.selected|:hover|:focus/)
 }
 assert.match(components.KubeWorkloadExplorer.template,/:color="podState\(pod\)\.tone"/)
 assert.match(components.KubeWorkloadPodRuntime.template,/:color="containerState\(item\)\.tone"/)
 const helpers=loadFrontend('../src/utils/kubernetesWorkloadExplorer.ts')
 assert.equal(helpers.workloadConditionTone({type:'Ready',status:'False'}),'warning')
 assert.equal(helpers.workloadConditionTone({type:'Ready',status:'Unknown'}),'default')
 assert.equal(helpers.workloadConditionTone({type:'FutureCondition',status:'True'}),'default')
 assert.equal(helpers.workloadConditionTone({type:'Failed',status:'True'}),'warning')
 const api=loadFrontend('../src/api/kubernetes.ts'),roles=loadFrontend('../src/utils/kubernetesRoles.ts')
 const presentation=loadFrontend('../src/utils/kubernetesPresentation.ts',{'@/api/kubernetes':api,'@/utils/kubernetesRoles':roles})
 const scaledDown={kind:'Deployment',metadata:{uid:'scale-zero'},spec:{replicas:0},status:{replicas:0,readyReplicas:0,updatedReplicas:0}}
 assert.equal(presentation.resourceCells(scaledDown,key=>key).replicas.badge,'neutral')
 const unknown={kind:'Pod',metadata:{uid:'unknown'},spec:{containers:[{name:'app'}]},status:{phase:'Unknown'}}
 assert.notEqual(presentation.resourceStatus(unknown,key=>key).tone,'success')
})

test('CPU 趋势主线用青绿、内存主线用蓝且明暗主题图线对比度不低于3，默认柱图仍用原主题色',async()=>{
 const api=loadFrontend('../src/api/kubernetes.ts'),diagnostics=loadFrontend('../src/utils/kubernetesDiagnostics.ts',{'@/api/kubernetes':api})
 const base=postcss.parse(readFileSync(new URL('../src/styles/vben.css',import.meta.url),'utf8'))
 for(const theme of ['light','dark']){
  const colors={...declarationsFor([base],':root'),...(theme==='dark'?declarationsFor([base],':root[data-theme="dark"]'):{}),...palette(theme)},options=new Map()
  const component=loadFrontend('../src/components/KubeResourceDiagnostics.vue',{
   vue:{...vue,onMounted(){},onBeforeUnmount(){},watch(){}},
   'vue-i18n':{useI18n:()=>({t:key=>key,te:()=>false,locale:vue.ref('zh-CN')})},
   'echarts/core':{use(){},init:element=>({setOption:option=>options.set(element.kind,option),resize(){}})},
   '@/stores/preferences':{usePreferencesStore:()=>({theme})},
   '@/api/kubernetes':api,'@/utils/kubernetesDiagnostics':diagnostics,
  },{getComputedStyle:()=>({getPropertyValue:name=>colors[name]??''})}).default
  const object={kind:'Pod',metadata:{uid:'palette-test',name:'web',namespace:'preview'},spec:{containers:[{name:'app',resources:{requests:{cpu:'100m',memory:'16Mi'},limits:{cpu:'1',memory:'64Mi'}}}]}},props=vue.reactive({cluster:'fixture',object,type:api.kubeTypes.find(type=>type.kind==='Pod'),autoRefresh:false,canFollow:()=>false,trend:true})
  const scope=vue.effectScope();let state
  scope.run(()=>{state=component.setup(props,{expose(){},emit(){}})})
  try{
   const now=Date.now(),sample={uid:object.metadata.uid,state:'ok',metrics_at:new Date(now).toISOString(),containers:[{name:'app',cpu:.01,memory:1024**2}]}
   state.now.value=now;state.data.value=sample;state.history.value=diagnostics.appendPodMetricSample(null,object,sample,now)
   state.root.value={querySelector:selector=>({kind:selector.includes('cpu')?'cpu':'memory',clientWidth:480})}
   await state.render()
   for(const kind of ['cpu','memory']){
    const color=colors[kind==='cpu'?'--workload-chart-cpu':'--workload-accent'],option=options.get(kind)
    assert.equal(option.series[0].type,'line');assert.equal(option.series[0].itemStyle.color,color,`${theme}/${kind}`)
    for(const surface of ['--edo-surface','--workload-column'])assert.ok(contrast(color,colors[surface])>=3,`${theme}/${kind}/${surface} 图线对比度不足`)
   }
   props.trend=false;await state.render()
   for(const kind of ['cpu','memory']){
    const series=options.get(kind).series[0]
    assert.equal(series.type,'bar');assert.equal(series.data[0].itemStyle.color,colors['--edo-primary'])
    assert.equal(series.data[1].itemStyle.color,theme==='dark'?'#788ba9':'#93a6c3')
    assert.equal(series.data[2].itemStyle.color,theme==='dark'?'#4b5970':'#cdd5e1')
   }
  }finally{scope.stop()}
 }
})
