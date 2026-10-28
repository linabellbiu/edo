import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {test} from 'node:test'
import * as vue from 'vue'
import {renderToString} from '@vue/server-renderer'
import {compileScript,parse} from '@vue/compiler-sfc'
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

test('真实检测报告SFC按模式呈现通过/警告/不满足，明确检测只读且不创建计划',async()=>{
  for(const mode of ['native','k3d']){
    const value=report(),html=await render(Report,{report:value,mode,standalone:true})
    assert.match(html,new RegExp(`检测模式：${mode==='native'?'原生 K3s':'k3d'}`));assert.match(html,/安装前检测通过，含 1 项警告/);assert.match(html,/>通过</);assert.match(html,/>警告</)
    assert.match(html,/只读检测，不创建安装计划、不安装或修改主机/);assert.match(html,/role="status"/);assert.match(html,/tabindex="-1"/);assert.match(html,/<details[^>]*open/)
    value.passed=false;value.hosts[0].passed=false;value.hosts[0].checks.push(check('filesystem','failed'))
    const failed=await render(Report,{report:value,mode,standalone:true});assert.match(failed,/安装前检测未通过，1 项不满足/);assert.match(failed,/>不满足</);assert.doesNotMatch(failed,/安装前检测通过，/)
  }
})

test('报告没有检查项不标绿，后台passed矛盾时以真实不满足项阻断，额外凭据不渲染',async()=>{
  const value=report();value.hosts[0].checks=[];value.server_token='do-not-render-token';value.config={password:'do-not-render-password'}
  let html=await render(Report,{report:value,mode:'native',standalone:true});assert.match(html,/安装前检测未通过/);assert.match(html,/未取得检测结果/);assert.doesNotMatch(html,/color="green"|do-not-render/)
  value.hosts[0].checks=[check('blocked','failed')];html=await render(Report,{report:value,mode:'native',standalone:true});assert.match(html,/安装前检测未通过，1 项不满足/);assert.doesNotMatch(html,/安装前检测通过，/)
})

test('主机和结果长文本按纯文本渲染，失败检查保持展开并可键盘进入原生details',async()=>{
  const value=report();value.passed=false;value.hosts[0].passed=false;value.hosts[0].host_name='<script>not-executed</script>';value.hosts[0].checks=[{...check('filesystem','failed'),message:'只读诊断 '.repeat(300)+'<img src=x onerror=alert(1)>'}]
  const html=await render(Report,{report:value,mode:'native',standalone:true});assert.match(html,/&lt;script&gt;not-executed&lt;\/script&gt;/);assert.match(html,/&lt;img src=x onerror=alert\(1\)&gt;/);assert.doesNotMatch(html,/<script>|<img/);assert.match(html,/<details[^>]*open/);assert.match(html,/<summary>/)
})

const ast=parse(read('../src/components/K3sProvisionDrawer.vue')).descriptor.template.ast
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
