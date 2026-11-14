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
  for(const name of ['a-alert','a-tag','a-button'])app.component(name,{inheritAttrs:false,setup:(_p,{attrs,slots})=>()=>vue.h(name==='a-button'?'button':'div',attrs,[String(attrs.message??''),String(attrs.description??''),slots.default?.(),slots.action?.()])})
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
test('仅k3d保留安装介绍，检测中或已有报告不叠加说明',()=>{
  const introductions=ast.children.filter(node=>node.type===1&&node.tag==='a-drawer').flatMap(node=>node.children).filter(node=>node.type===1&&node.props.some(prop=>prop.type===6&&prop.name==='class'&&prop.value?.content==='provision-content')).flatMap(node=>node.children).filter(node=>node.type===1&&node.tag==='a-alert'&&node.props.some(prop=>prop.type===7&&prop.name==='bind'&&prop.arg?.content==='description'&&/nativeInstallHint|k3dInstallHint/.test(prop.exp?.content??'')))
  assert.equal(introductions.length,1)
  for(const node of introductions){const condition=node.props.find(prop=>prop.type===7&&['if','else-if'].includes(prop.name))?.exp?.content??'';assert.match(condition,/!installation&&!visiblePreflight&&!checking&&displayMode==='k3d'/)}
})
let footer
function find(node){if(node.type===1&&node.tag==='template'&&node.props.some(p=>p.type===7&&p.name==='slot'&&p.arg?.content==='footer'))footer=node;for(const child of node.children??[])find(child)}
find(ast);assert.ok(footer)
const Footer=compile(`<script setup lang="ts">
import {toRefs} from 'vue'
const props=defineProps<{state:any}>()
const {busy,checking,running,plan,installation,displayMode,isK3d,preflight,canCheck,canPreview,planExpired,canExecute,planPreflightPassed,planModeValid,planSourceValid,checkedInput,checkedContext}=toRefs(props.state)
const t=(key:string)=>key,close=()=>{},configureAgain=()=>{},checkPreflight=()=>{},preview=()=>{},execute=()=>{}
</script><template>${footer.children.map(n=>n.loc.source).join('')}</template>`)
const initial=()=>({busy:false,checking:false,running:false,plan:undefined,installation:undefined,displayMode:'k3d',isK3d:true,preflight:undefined,canCheck:true,canPreview:false,planExpired:false,canExecute:true,planPreflightPassed:false,planModeValid:true,planSourceValid:true,checkedInput:'',checkedContext:''})

test('原生Footer直接生成计划，确认后才出现开始安装，不显示安装前检测',async()=>{
  const state=vue.reactive({...initial(),displayMode:'native',isK3d:false,canPreview:true})
  let html=await render(Footer,{state});assert.doesNotMatch(html,/安装前检测|重新检测|开始安装/);assert.match(html,/<button type="primary"[^>]*>生成安装计划<\/button>/)
  state.plan={id:'official-plan'};state.planPreflightPassed=true
  html=await render(Footer,{state});assert.match(html,/开始安装/);assert.doesNotMatch(html,/安装前检测|生成安装计划/)
})

test('真实抽屉Footer把检测与生成计划分成两个操作，未检测/失败阻断，检测中可取消',async()=>{
  const state=vue.reactive(initial());let html=await render(Footer,{state})
  assert.match(html,/<button[^>]*>[^]*?安装前检测<\/button>/);assert.match(html,/<button[^>]*disabled[^>]*>生成安装计划<\/button>/);assert.doesNotMatch(html,/开始安装/)
  state.preflight=report();state.canPreview=true;html=await render(Footer,{state});assert.match(html,/>重新检测<\/button>/);assert.match(html,/<button type="primary"[^>]*>生成安装计划<\/button>/)
  state.preflight.passed=false;state.canPreview=false;html=await render(Footer,{state});assert.match(html,/<button[^>]*disabled[^>]*>生成安装计划<\/button>/)
  state.checking=true;state.busy=true;state.canCheck=false;html=await render(Footer,{state});assert.match(html,/<button>取消检测<\/button>/);assert.match(html,/<button[^>]*disabled[^>]*>重新检测<\/button>/)
})

function descendants(node){return [node,...(node.children??[]).flatMap(descendants)]}
const failureSection=descendants(ast).find(node=>node.type===1&&node.tag==='details'&&node.props.some(prop=>prop.type===6&&prop.name==='class'&&prop.value?.content==='previous-failure'))
assert.ok(failureSection)
const PreviousFailure=compile(`<script setup lang="ts">
import {toRefs} from 'vue'
const props=defineProps<{state:any}>()
const {previousFailure,failureError,failureLoading,previousFailureLogs,installation,loadPreviousFailure}=toRefs(props.state)
</script><template>${failureSection.loc.source}</template>`)

test('上一失败日志使用默认收起的原生details，保留键盘浏览且没有任务列表和进度入口',()=>{
  const summary=failureSection.children.find(node=>node.type===1&&node.tag==='summary')
  assert.ok(summary)
  assert.equal(failureSection.props.some(prop=>prop.name==='open'||prop.arg?.content==='open'),false)
  assert.deepEqual(descendants(summary).filter(node=>node!==summary&&node.type===1&&['button','a','input','a-button'].includes(node.tag)),[])
  const pre=descendants(failureSection).find(node=>node.type===1&&node.tag==='pre')
  assert.equal(pre.props.find(prop=>prop.name==='tabindex')?.value?.content,'0')
  assert.doesNotMatch(read('../src/components/K3sProvisionDrawer.vue'),/最近安装任务|recent-operations|viewInstallation/)
})

test('上一失败只渲染日志纯文本，不展示状态元数据或管理入口，当前安装时隐藏',async()=>{
  const state=vue.reactive({previousFailure:{id:'old-task',status:'failed',cluster:{id:'deleted'}},failureError:'',failureLoading:false,previousFailureLogs:{text:'<script>日志</script>',truncated:false},installation:undefined,loadPreviousFailure(){}})
  let html=await render(PreviousFailure,{state})
  assert.match(html,/上一次安装失败日志/);assert.match(html,/&lt;script&gt;日志&lt;\/script&gt;/)
  assert.doesNotMatch(html,/old-task|deleted|管理|查看进度|<script>/)
  state.failureError='日志读取失败';html=await render(PreviousFailure,{state});assert.match(html,/日志读取失败/);assert.match(html,/重新读取/)
  state.installation={id:'current',status:'running'};html=await render(PreviousFailure,{state});assert.doesNotMatch(html,/上一次安装失败日志|日志读取失败/)
})

test('上一失败日志在明暗主题使用可读文本与滚动区域，键盘焦点保持可见',()=>{
  const css=compileStyle({id:'k3s-failure-logs',source:parse(read('../src/components/K3sProvisionDrawer.vue')).descriptor.styles[0].content,scoped:true})
  assert.deepEqual(css.errors,[])
  assert.match(css.code,/\.previous-failure pre[^}]*overflow:auto/)
  assert.match(css.code,/\.previous-failure pre[^}]*color:var\(--edo-text\)/)
  assert.match(css.code,/\.previous-failure pre(?:\[[^\]]+\])?:focus-visible[^}]*outline:2px solid var\(--edo-primary\)/)
  const theme=read('../src/styles/vben.css'),light=theme.match(/:root\s*\{([^]*?)\}/)?.[1],dark=theme.match(/:root\[data-theme="dark"\]\s*\{([^]*?)\}/)?.[1]
  const token=(block,name)=>block.match(new RegExp(`--edo-${name}:\\s*(#[0-9a-f]{6})`,'i'))?.[1]
  const rgb=value=>value.slice(1).match(/../g).map(part=>parseInt(part,16)/255),luminance=value=>value.map(channel=>channel<=.04045?channel/12.92:((channel+.055)/1.055)**2.4).reduce((sum,channel,index)=>sum+channel*[.2126,.7152,.0722][index],0),contrast=(a,b)=>(Math.max(luminance(a),luminance(b))+.05)/(Math.min(luminance(a),luminance(b))+.05)
  for(const [name,block] of [['light',light],['dark',dark]]){
    assert.ok(contrast(rgb(token(block,'text')),rgb(token(block,'bg')))>=4.5,`${name}日志文字须达4.5:1`)
    assert.ok(contrast(rgb(token(block,'primary')),rgb(token(block,'bg')))>=3,`${name}焦点轮廓须达3:1`)
  }
})

test('发行版与root/sudo权限按主机就地展示，失败有引导和受权限控制的配置入口',async()=>{
  for(const distribution of ['Ubuntu 24.04','CentOS 9']){
    const value=report();value.passed=false;value.hosts[0].passed=false
    value.hosts[0].checks=[{id:'privilege',name:'安装权限',status:'failed',message:'重复的权限检查文字'}]
    value.hosts[0].privilege={distribution,account:'standard',sudo:'passwordless',sudo_enabled:false,ready:false,message:'当前账号可以 sudo，但 EDO 未开启安装提权。',guidance:['打开主机配置勾选“使用 sudo 执行 Docker / 安装操作”','测试连接并保存，再重新检测']}
    let html=await render(Report,{report:value,canConfigureHost:true})
    assert.match(html,new RegExp(distribution));assert.match(html,/NAS测试主机/);assert.match(html,/安装权限未就绪/);assert.match(html,/打开主机配置/);assert.match(html,/<ol>/);assert.doesNotMatch(html,/重复的权限检查文字|未发现不满足项/)
    html=await render(Report,{report:value,canConfigureHost:false});assert.doesNotMatch(html,/<button/);assert.match(html,/请联系有主机编辑权限的管理员/)
    value.passed=true;value.hosts[0].passed=true;value.hosts[0].checks=[check('privilege')];value.hosts[0].privilege={...value.hosts[0].privilege,account:'root',sudo:'not_needed',ready:true,message:'当前账号已验证具有 root 管理员权限，无需 sudo。',guidance:[]}
    html=await render(Report,{report:value});assert.match(html,/无需 sudo/);assert.match(html,/安装权限已就绪/);assert.doesNotMatch(html,/<ol>|<button/)
  }
})
