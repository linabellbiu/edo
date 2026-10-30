import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {test} from 'node:test'
import * as vue from 'vue'
import {renderToString} from '@vue/server-renderer'
import {compileScript,compileStyle,parse} from '@vue/compiler-sfc'
import ts from 'typescript'
import {loadFrontend} from './acceptanceHarness.mjs'

const read=path=>readFileSync(new URL(path,import.meta.url),'utf8')
function compile(source,modules={}){
  const code=ts.transpileModule(compileScript(parse(source).descriptor,{id:'k3s-preflight-ui',inlineTemplate:true}).content,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022,esModuleInterop:true}}).outputText,module={exports:{}}
  new Function('require','module','exports',code)(name=>name==='vue'?vue:modules[name]??{},module,module.exports)
  return module.exports.default
}
const Report=compile(read('../src/components/K3sPreflightReport.vue'),{'vue-i18n':{useI18n:()=>({t:key=>key==='k8s.installationHost'?'安装主机':key})},'@/utils/time':loadFrontend('../src/utils/time.ts')})
async function render(component,props){
  const app=vue.createSSRApp(component,props)
  for(const name of ['a-alert','a-tag','a-button'])app.component(name,{inheritAttrs:false,setup:(_p,{attrs,slots})=>()=>vue.h(name==='a-button'?'button':'div',attrs,[String(attrs.message??''),String(attrs.description??''),slots.default?.()])})
  app.component('ChevronLeft',()=>vue.h('svg'))
  return (await renderToString(app)).replace(/<!--[\s\S]*?-->/g,'')
}
const check=(id,status='passed')=>({id,name:`检查-${id}`,status,message:status==='failed'?'需要处理后重新检测':'检测结果已确认'})
const report=()=>({checked_at:'2026-10-29T00:30:00+08:00',passed:true,checks:[],hosts:[{host_id:'nas',host_name:'NAS测试主机',hostname:'nas-test',role:'host',passed:true,checks:[check('docker'),check('memory','warning')]}]})

test('真实检测报告只列不满足项，无阻断仅简短结果，保留唯一官方外链',async()=>{
  for(const mode of ['native','k3d']){
    const value=report(),html=await render(Report,{report:value,mode,standalone:true})
    assert.match(html,/未发现不满足项/);assert.doesNotMatch(html,/检查-docker|检查-memory|>通过<|>警告<|检测时间|台主机|项检测|<details|<summary|检测模式/)
    assert.match(html,/href="https:\/\/docs.k3s.io\/zh\/installation\/configuration" target="_blank" rel="noopener noreferrer"/);assert.equal((html.match(/<a /g)||[]).length,1)
    assert.match(html,/role="status"/);assert.match(html,/tabindex="-1"/)
    value.passed=false;value.hosts[0].passed=false;value.hosts[0].checks.push(check('filesystem','failed'))
    const failed=await render(Report,{report:value,mode,standalone:true});assert.match(failed,/NAS测试主机/);assert.match(failed,/检查-filesystem/);assert.match(failed,/>不满足</);assert.doesNotMatch(failed,/未发现不满足项|检查-docker|检查-memory/)
  }
})

test('报告没有检查项不标绿，后台passed矛盾时以真实不满足项阻断，额外凭据不渲染',async()=>{
  const value=report();value.hosts[0].checks=[];value.server_token='do-not-render-token';value.config={password:'do-not-render-password'}
  let html=await render(Report,{report:value,mode:'native',standalone:true});assert.match(html,/检测结果不完整，请重新检测/);assert.doesNotMatch(html,/color="green"|do-not-render|未发现不满足项/)
  value.hosts[0].checks=[check('blocked','failed')];html=await render(Report,{report:value,mode:'native',standalone:true});assert.match(html,/检测结果未确认通过，请重新检测/);assert.match(html,/检查-blocked/);assert.doesNotMatch(html,/未发现不满足项/)
  value.passed=false;value.hosts[0].passed=false;value.hosts[0].checks=[check('docker')];html=await render(Report,{report:value});assert.match(html,/检测结果未确认通过/);assert.doesNotMatch(html,/未发现不满足项/)
})

test('多主机仅展示各自失败项，长文本按纯文本渲染且无需展开',async()=>{
  const value=report();value.passed=false;value.hosts[0].passed=false;value.hosts[0].host_name='<script>not-executed</script>';value.hosts[0].checks=[{...check('filesystem','failed'),message:'只读诊断 '.repeat(300)+'<img src=x onerror=alert(1)>'}]
  value.hosts.push({host_id:'worker',host_name:'工作主机',hostname:'worker',role:'agent',passed:false,checks:[check('swap','failed'),check('network')]})
  const html=await render(Report,{report:value,mode:'native',standalone:true});assert.match(html,/&lt;script&gt;not-executed&lt;\/script&gt;/);assert.match(html,/&lt;img src=x onerror=alert\(1\)&gt;/);assert.doesNotMatch(html,/<script>|<img|<details|<summary|检查-network/);assert.match(html,/工作主机/);assert.match(html,/检查-swap/);assert.equal((html.match(/class="preflight-group"/g)||[]).length,2)
})

test('抽屉与报告限制网格最小宽度，窄屏长文本不能撑宽焦点区域',()=>{
  for(const file of ['K3sProvisionDrawer.vue','K3sPreflightReport.vue']){
    const {descriptor}=parse(read(`../src/components/${file}`))
    const result=compileStyle({id:'k3s-responsive-regression',source:descriptor.styles[0].content,scoped:true})
    assert.deepEqual(result.errors,[])
    const root=file==='K3sProvisionDrawer.vue'?'provision-content':'preflight-report'
    assert.match(result.code,new RegExp(`\\.${root}[^}]*grid-template-columns:minmax\\(0,1fr\\)[^}]*min-width:0`))
    assert.match(result.code,new RegExp(`\\.${root}[^>]*>\\*[^}]*min-width:0`))
    assert.doesNotMatch(result.code,/overflow(?:-x)?:hidden|outline:none/)
    if(file==='K3sProvisionDrawer.vue'){
      assert.match(result.code,/grid-template-columns:repeat\(2,minmax\(0,1fr\)\)/)
      assert.match(result.code,/@media\(max-width:650px\)\{[^}]*\.form-columns[^}]*grid-template-columns:minmax\(0,1fr\)/)
    }
  }
})

const ast=parse(read('../src/components/K3sProvisionDrawer.vue')).descriptor.template.ast
test('检测中或已有报告不叠加长安装介绍，原生与k3d分支使用相同门控',()=>{
  const introductions=ast.children.filter(node=>node.type===1&&node.tag==='a-drawer').flatMap(node=>node.children).filter(node=>node.type===1&&node.props.some(prop=>prop.type===6&&prop.name==='class'&&prop.value?.content==='provision-content')).flatMap(node=>node.children).filter(node=>node.type===1&&node.tag==='a-alert'&&node.props.some(prop=>prop.type===7&&prop.name==='bind'&&prop.arg?.content==='description'&&/nativeInstallHint|k3dInstallHint/.test(prop.exp?.content??'')))
  assert.equal(introductions.length,2)
  for(const node of introductions){const condition=node.props.find(prop=>prop.type===7&&['if','else-if'].includes(prop.name))?.exp?.content??'';assert.match(condition,/!installation&&!visiblePreflight&&!checking/)}
})
let footer
function find(node){if(node.type===1&&node.tag==='template'&&node.props.some(p=>p.type===7&&p.name==='slot'&&p.arg?.content==='footer'))footer=node;for(const child of node.children??[])find(child)}
find(ast);assert.ok(footer)
const Footer=compile(`<script setup lang="ts">
import {toRefs} from 'vue'
const props=defineProps<{state:any}>()
const {busy,checking,running,plan,installation,displayMode,preflight,canCheck,canPreview,planExpired,canExecute,planPreflightPassed,planModeValid,planSourceValid,checkedInput,checkedContext}=toRefs(props.state)
const t=(key:string)=>key,close=()=>{},configureAgain=()=>{},checkPreflight=()=>{},preview=()=>{},execute=()=>{}
</script><template>${footer.children.map(n=>n.loc.source).join('')}</template>`)
const initial=()=>({busy:false,checking:false,running:false,plan:undefined,installation:undefined,displayMode:'native',preflight:undefined,canCheck:true,canPreview:false,planExpired:false,canExecute:true,planPreflightPassed:false,planModeValid:true,planSourceValid:true,checkedInput:'',checkedContext:''})

test('真实抽屉Footer把检测与生成计划分成两个操作，未检测/失败阻断，检测中可取消',async()=>{
  const state=vue.reactive(initial());let html=await render(Footer,{state})
  assert.match(html,/<button[^>]*>[^]*?安装前检测<\/button>/);assert.match(html,/<button[^>]*disabled[^>]*>生成安装计划<\/button>/);assert.doesNotMatch(html,/开始安装/)
  state.preflight=report();state.canPreview=true;html=await render(Footer,{state});assert.match(html,/>重新检测<\/button>/);assert.match(html,/<button type="primary"[^>]*>生成安装计划<\/button>/)
  state.preflight.passed=false;state.canPreview=false;html=await render(Footer,{state});assert.match(html,/<button[^>]*disabled[^>]*>生成安装计划<\/button>/)
  state.checking=true;state.busy=true;state.canCheck=false;html=await render(Footer,{state});assert.match(html,/<button>取消检测<\/button>/);assert.match(html,/<button[^>]*disabled[^>]*>重新检测<\/button>/)
})

function descendants(node){return [node,...(node.children??[]).flatMap(descendants)]}
const recentSection=descendants(ast).find(node=>node.type===1&&node.tag==='section'&&node.props.some(prop=>prop.type===6&&prop.name==='class'&&prop.value?.content==='recent-operations'))
assert.ok(recentSection)

test('最近安装任务保留原生折叠控件，summary内不再嵌套刷新或其他交互',()=>{
  const details=descendants(recentSection).find(node=>node.type===1&&node.tag==='details')
  const summary=details.children.find(node=>node.type===1&&node.tag==='summary')
  assert.ok(summary)
  assert.equal(details.props.find(prop=>prop.type===7&&prop.name==='bind'&&prop.arg?.content==='open')?.exp?.content,'!installation')
  assert.deepEqual(descendants(summary).filter(node=>node!==summary&&node.type===1&&(['button','a','input','select','textarea','a-button'].includes(node.tag)||node.props.some(prop=>prop.type===6&&prop.name==='tabindex'))).map(node=>node.tag),[])
  const actions=details.children.find(node=>node.type===1&&node.props.some(prop=>prop.type===6&&prop.name==='class'&&prop.value?.content==='recent-actions'))
  assert.ok(actions,'刷新应位于原生折叠内容内的独立操作行')
  const refresh=descendants(actions).find(node=>node.type===1&&node.tag==='a-button')
  assert.equal(refresh.props.find(prop=>prop.type===7&&prop.name==='on'&&prop.arg?.content==='click')?.exp?.content,'loadRecent')
  assert.equal(refresh.props.find(prop=>prop.type===7&&prop.name==='bind'&&prop.arg?.content==='loading')?.exp?.content,'recentLoading')
  assert.equal(refresh.props.find(prop=>prop.type===7&&prop.name==='bind'&&prop.arg?.content==='disabled')?.exp?.content,'recentLoading')
  assert.ok(details.children.indexOf(summary)<details.children.indexOf(actions))
})

const Recent=compile(`<script setup lang="ts">
import {toRefs} from 'vue'
const props=defineProps<{state:any}>()
const {recent,recentError,recentLoading,installation,busy,loadRecent,viewInstallation}=toRefs(props.state)
const formatDateTime=(value:string)=>value,t=(key:string)=>key,modeOf=(item:any)=>item.mode||'native',k3sHostRoleLabel=(role:string)=>role,k3sInstallationLabel=(status:string)=>status
</script><template>${recentSection.loc.source}</template>`)

test('最近任务展开后刷新保留加载与禁用状态，任务选择和原生开合互不干扰',async t=>{
  const node=(tag,text='')=>({tag,text,props:{},children:[],parent:null})
  const renderer=vue.createRenderer({
    createElement:tag=>node(tag),createText:text=>node('#text',text),createComment:()=>node('#comment'),setText:(n,text)=>{n.text=text},setElementText:(n,text)=>{n.text=text;n.children=[]},patchProp:(n,key,_old,value)=>{n.props[key]=value},parentNode:n=>n.parent,nextSibling:n=>n.parent?.children[n.parent.children.indexOf(n)+1]??null,
    remove(n){if(n.parent)n.parent.children.splice(n.parent.children.indexOf(n),1);n.parent=null},
    insert(n,parent,anchor=null){if(n.parent)n.parent.children.splice(n.parent.children.indexOf(n),1);const index=anchor?parent.children.indexOf(anchor):-1;parent.children.splice(index<0?parent.children.length:index,0,n);n.parent=parent},
  })
  const calls=[],item={id:'test-installation',name:'安装任务',mode:'k3d',status:'running'},state=vue.reactive({recent:[item],recentError:'',recentLoading:false,installation:undefined,busy:false,loadRecent:()=>{calls.push('refresh');state.recentLoading=true},viewInstallation:value=>{calls.push(value.id);state.installation=value}})
  const root=node('root'),app=renderer.createApp({setup:()=>()=>vue.h(Recent,{state})})
  for(const name of ['a-button','a-tag','a-alert'])app.component(name,{inheritAttrs:false,setup:(_p,{attrs,slots})=>()=>vue.h(name==='a-button'?'button':'div',attrs,slots.default?.())})
  app.mount(root);t.after(()=>app.unmount())
  const text=n=>n.text+n.children.map(text).join(''),all=()=>descendants(root),findButton=label=>all().find(n=>n.tag==='button'&&text(n)===label),details=all().find(n=>n.tag==='details'),summary=all().find(n=>n.tag==='summary')
  assert.equal(details.props.open,true);assert.equal(summary.props.tabindex,undefined);assert.equal(summary.props.role,undefined);assert.equal(summary.props.onClick,undefined)
  findButton('刷新').props.onClick({stopPropagation(){},preventDefault(){}});await vue.nextTick()
  assert.deepEqual(calls,['refresh']);assert.equal(details.props.open,true);assert.equal(findButton('刷新').props.loading,true);assert.equal(findButton('刷新').props.disabled,true)
  state.recentLoading=false;await vue.nextTick();assert.equal(findButton('刷新').props.disabled,false)
  findButton('查看进度').props.onClick();await vue.nextTick();assert.deepEqual(calls,['refresh','test-installation']);assert.equal(details.props.open,false)
})

test('最近任务刷新局部链接色在明暗主题及正常悬停聚焦下均满足正文对比度',()=>{
  const {descriptor}=parse(read('../src/components/K3sProvisionDrawer.vue')),css=compileStyle({id:'k3s-recent-a11y',source:descriptor.styles[0].content,scoped:true})
  assert.deepEqual(css.errors,[])
  assert.match(css.code,/\.recent-actions[^}]*background:var\(--edo-bg\)/)
  assert.match(css.code,/\.recent-refresh\.ant-btn-link:not\(:disabled\)[^{]*\{color:var\(--edo-nav-active-color\)/)
  assert.match(css.code,/\.recent-refresh\.ant-btn-link:not\(:disabled\):hover[^{]*\{color:var\(--edo-text\)/)
  assert.match(css.code,/\.recent-refresh\.ant-btn-link:focus-visible[^{]*\{outline:2px solid var\(--edo-primary\);outline-offset:2px/)
  const theme=read('../src/styles/vben.css'),light=theme.match(/:root\s*\{([^]*?)\}/)?.[1],dark=theme.match(/:root\[data-theme="dark"\]\s*\{([^]*?)\}/)?.[1]
  const token=(block,name)=>block.match(new RegExp(`--edo-${name}:\\s*(#[0-9a-f]{6})`,'i'))?.[1]
  const rgb=value=>value.slice(1).match(/../g).map(part=>parseInt(part,16)/255),luminance=value=>value.map(channel=>channel<=.04045?channel/12.92:((channel+.055)/1.055)**2.4).reduce((sum,channel,index)=>sum+channel*[.2126,.7152,.0722][index],0),contrast=(a,b)=>(Math.max(luminance(a),luminance(b))+.05)/(Math.min(luminance(a),luminance(b))+.05)
  assert.match(light,/--edo-nav-active-color:\s*color-mix\(in srgb, var\(--edo-primary\) 84%, var\(--edo-text\)\)/)
  for(const [name,block] of [['light',light],['dark',dark]]){
    const primary=rgb(token(block,'primary')),text=rgb(token(block,'text')),background=rgb(token(block,'bg')),link=primary.map((channel,index)=>channel*.84+text[index]*.16)
    assert.ok(contrast(link,background)>=4.5,`${name}正常/聚焦文字须达4.5:1`)
    assert.ok(contrast(text,background)>=4.5,`${name}悬停文字须达4.5:1`)
    assert.ok(contrast(primary,background)>=3,`${name}焦点轮廓须达3:1`)
  }
})
