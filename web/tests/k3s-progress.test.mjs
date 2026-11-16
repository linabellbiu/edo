import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {test} from 'node:test'
import {compileScript,parse} from '@vue/compiler-sfc'
import * as vue from 'vue'
import * as yaml from 'yaml'
import ts from 'typescript'
import {loadFrontend} from './acceptanceHarness.mjs'

const helpers=loadFrontend('../src/utils/k3sProvision.ts',{yaml})
const time=loadFrontend('../src/utils/time.ts')
const read=path=>readFileSync(new URL(path,import.meta.url),'utf8')
function compile(source,modules={}){
  const script=compileScript(parse(source).descriptor,{id:'k3s-progress-render',inlineTemplate:true}).content
  const code=ts.transpileModule(script,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022,esModuleInterop:true}}).outputText,module={exports:{}}
  new Function('require','module','exports',code)(name=>name==='vue'?vue:modules[name]??{},module,module.exports)
  return module.exports.default
}
const Progress=compile(read('../src/components/K3sInstallationProgress.vue'),{'@/utils/k3sProvision':helpers,'@/utils/time':time,'lucide-vue-next':{Server:()=>vue.h('svg')}})
const node=(tag,text='')=>({tag,text,props:{},children:[],parent:null})
const renderer=vue.createRenderer({
  createElement:tag=>node(tag),createText:text=>node('#text',text),createComment:()=>node('#comment'),setText:(n,text)=>{n.text=text},setElementText:(n,text)=>{n.text=text;n.children=[]},patchProp:(n,key,_old,value)=>{n.props[key]=value},parentNode:n=>n.parent,nextSibling:n=>n.parent?.children[n.parent.children.indexOf(n)+1]??null,
  remove(n){if(n.parent)n.parent.children.splice(n.parent.children.indexOf(n),1);n.parent=null},
  insert(n,parent,anchor=null){if(n.parent)n.parent.children.splice(n.parent.children.indexOf(n),1);const index=anchor?parent.children.indexOf(anchor):-1;parent.children.splice(index<0?parent.children.length:index,0,n);n.parent=parent},
})
const walk=n=>[n,...n.children.flatMap(walk)],text=n=>n.tag==='#comment'?'':n.text+n.children.map(text).join('')
function mount(t,installation,now=Date.parse('2026-10-28T23:22:28+08:00')){
  const state=vue.reactive({installation,now}),root=node('root'),app=renderer.createApp({setup:()=>()=>vue.h(Progress,state)})
  for(const name of ['a-tag','a-button'])app.component(name,{inheritAttrs:false,setup:(_p,{attrs,slots})=>()=>vue.h(name==='a-button'?'button':'span',attrs,slots.default?.())})
  app.mount(root);t.after(()=>app.unmount());return {state,root,content:()=>text(root)}
}
const record=()=>({id:'run',status:'running',started_at:'2026-10-28T23:20:28+08:00',hosts:[{id:'nas',name:'NAS 测试主机',address:'192.0.2.24',status:'installing',stage:'installer_start',message:'等待 K3s 服务启动',stage_started_at:'2026-10-28T23:21:28+08:00',progress_updated_at:'2026-10-28T23:22:00+08:00'}],logs:'23:20:28 任务开始\n23:21:28 等待 K3s 服务启动'})

test('安装进度只显示服务端确认的秒数，日志旁显示黄色后台提示',async t=>{
  const h=mount(t,record())
  assert.match(h.content(),/已持续 32 秒/);assert.doesNotMatch(h.content(),/本阶段|最近反馈/);assert.match(h.content(),/安装日志可以关闭窗口，安装会继续执行。/);assert.match(h.content(),/总耗时2 分 0 秒/)
  assert.doesNotMatch(h.content(),/\d+%|installer_start/)
  const status=walk(h.root).find(n=>n.props.role==='status');assert.equal(status.props['aria-live'],'polite')
  h.state.now+=30_000;await vue.nextTick();assert.match(h.content(),/已持续 32 秒/);assert.match(h.content(),/总耗时2 分 30 秒/)
  assert.doesNotMatch(h.content(),/暂未收到新反馈/)
  h.state.installation.hosts[0].progress_updated_at='2026-10-28T23:23:25+08:00';await vue.nextTick();assert.match(h.content(),/已持续 117 秒/);assert.doesNotMatch(h.content(),/最近反馈|本阶段|安装失败/)
})

test('成功或失败任务按真实结束时间冻结，完成主机不继续累加阶段耗时',async t=>{
  const running=record();running.hosts.push({id:'done',name:'已完成主机',status:'succeeded',stage:'node',stage_started_at:'2026-10-28T23:20:28+08:00',progress_updated_at:'2026-10-28T23:21:28+08:00'})
  const h=mount(t,running);assert.match(h.content(),/已完成主机已完成已持续 60 秒/)
  h.state.installation.status='failed';h.state.installation.finished_at='2026-10-28T23:22:28+08:00';await vue.nextTick()
  const before=h.content();h.state.now+=3_600_000;await vue.nextTick();assert.equal(h.content(),before);assert.match(before,/结束时间/);assert.doesNotMatch(before,/可以关闭窗口/)
  h.state.installation.status='succeeded';await vue.nextTick();assert.match(h.content(),/总耗时2 分 0 秒/)
})

test('等待其他主机期间ready或checking主机按最后反馈冻结，不伪造当前阶段持续执行',async t=>{
  const installation=record();installation.hosts[0].status='ready';installation.hosts[0].progress_updated_at='2026-10-28T23:22:00+08:00'
  const h=mount(t,installation);assert.match(h.content(),/已持续 32 秒/)
  h.state.now+=120_000;await vue.nextTick();assert.match(h.content(),/已持续 32 秒/);assert.doesNotMatch(h.content(),/暂未收到新反馈/)
  h.state.installation.hosts[0].status='checking';await vue.nextTick();assert.match(h.content(),/已持续 32 秒/)
  delete h.state.installation.hosts[0].progress_updated_at;await vue.nextTick();assert.doesNotMatch(h.content(),/已持续|本阶段/)
  h.state.installation.hosts[0].status='running';await vue.nextTick();assert.doesNotMatch(h.content(),/已持续|本阶段/)
})

test('旧记录与非法时间保持空值，未知阶段不回显内部code，日志只作为文本',t=>{
  for(const [start,end] of [[undefined,Date.now()],['bad',Date.now()],['2026-10-28T23:22:28+08:00','2026-10-28T23:20:28+08:00'],['2026-10-28T23:20:28+08:00',undefined]])assert.equal(helpers.k3sElapsed(start,end),'—')
  const h=mount(t,{id:'legacy',status:'failed',server_token:'never-render-token',config:{password:'never-render-password'},hosts:[{id:'old',name:'旧主机',status:'failed',stage:'untrusted-private-code',message:'等待服务返回失败'}],logs:'<script>alert(1)</script>\n安全阶段日志'})
  assert.match(h.content(),/总耗时—/);assert.match(h.content(),/等待服务返回失败/)
  assert.doesNotMatch(h.content(),/never-render|untrusted-private-code/);assert.equal(walk(h.root).some(n=>n.tag==='script'),false)
  const log=walk(h.root).find(n=>n.tag==='pre');assert.match(text(log),/<script>alert\(1\)<\/script>/);assert.equal(log.props.innerHTML,undefined);assert.equal(log.props['aria-live'],'off');assert.equal(log.props.tabindex,'0')
})

test('长日志以UTF8字节为界保留完整中文日韩及emoji，保留尾部并可键盘浏览',t=>{
  for(const item of ['中文','日本語','한국어','🚀']){
    const view=helpers.k3sInstallationLogView({logs:item.repeat(1_100_000)+'\n最后一行'})
    assert.equal(view.truncated,true);assert.ok(new TextEncoder().encode(view.text).length<=2*1024*1024);assert.ok(view.text.endsWith('最后一行'));assert.doesNotMatch(view.text,/�|[\ud800-\udbff](?![\udc00-\udfff])|(?<![\ud800-\udbff])[\udc00-\udfff]/u)
  }
  const compact=helpers.k3sInstallationLogView({logs:'06:00:54 vps：官方安装器下载文件\n06:02:52 vps：官方安装器下载文件仍在执行，已持续 117 秒'})
  assert.equal(compact.text,'06:00:54 vps：官方安装器下载文件\n06:02:52 vps：已持续 117 秒')
  const h=mount(t,{...record(),logs:'中'.repeat(1_100_000)+'\u001b[31m\u0000\n最后一行'})
  assert.match(h.content(),/仅显示最近 2 MiB 日志/);const log=walk(h.root).find(n=>n.tag==='pre');assert.ok(new TextEncoder().encode(text(log)).length<=2*1024*1024);assert.doesNotMatch(text(log),/\x1b|\x00/)
  log.scrollHeight=987;walk(h.root).find(n=>n.tag==='button'&&text(n)==='查看最新').props.onClick();assert.equal(log.scrollTop,987)
})

test('系统组件阶段与安装器阶段全部用稳定中文名称，不把未知值冒充完成',()=>{
  for(const stage of ['installer_hash','installer_links','installer_killall','installer_uninstall','installer_enable','guard','preflight','download','configuration','installer','installer_download','installer_verify','installer_binary','installer_environment','installer_service','installer_start','version','service','api','import','node','association','components','component_coredns','component_metrics-server','component_local-path-provisioner','k3d_tool','k3d_create','k3d_resources'])assert.notEqual(helpers.k3sStageLabel(stage),'执行安装步骤',stage)
  assert.equal(helpers.k3sStageLabel('unknown'),'执行安装步骤');assert.equal(helpers.k3sStageLabel(undefined),'')
})

test('卸载任务复用进度与安全日志但不显示安装文案，原安装标签保持不变',t=>{
  const h=mount(t,{...record(),action:'uninstall',logs:'',hosts:[{id:'host-a',name:'目标主机',status:'installing',stage:'untrusted-uninstall-stage'}]})
  assert.match(h.content(),/卸载中|执行卸载步骤/);assert.match(h.content(),/卸载日志/);assert.match(h.content(),/等待卸载日志/);assert.doesNotMatch(h.content(),/安装|untrusted-uninstall-stage/)
  assert.equal(walk(h.root).find(n=>n.props.role==='log').props['aria-label'],'卸载安全日志')
  assert.equal(walk(h.root).find(n=>n.props['aria-label']==='卸载任务进度')?.tag,'section')
  assert.equal(helpers.k3sInstallationLabel('running','uninstall'),'正在卸载');assert.equal(helpers.k3sInstallationLabel('succeeded','uninstall'),'卸载完成');assert.equal(helpers.k3sInstallationLabel('failed','uninstall'),'卸载失败')
  assert.equal(helpers.k3sInstallationLabel('running'),'正在安装');assert.equal(helpers.k3sHostStatusLabel('installing'),'安装中')
})

test('卸载完成只保留日志，去掉重复主机、时间与阶段信息',t=>{
  const h=mount(t,{...record(),action:'uninstall',status:'succeeded',finished_at:'2026-10-28T23:20:30+08:00',hosts:[{id:'host-a',name:'重复主机',address:'192.0.2.24',status:'succeeded',stage:'uninstall_verify',message:'官方卸载已完成',stage_started_at:'2026-10-28T23:20:30+08:00',progress_updated_at:'2026-10-28T23:20:30+08:00'}],logs:'官方卸载脚本执行完成'})
  assert.match(h.content(),/卸载日志/);assert.match(h.content(),/官方卸载脚本执行完成/)
  assert.doesNotMatch(h.content(),/重复主机|192\.0\.2\.24|开始时间|结束时间|总耗时|本阶段|最近反馈|核验卸载结果|官方卸载已完成|可以关闭窗口|查看最新/)
})

test('终端日志追加跟随底部，手动上滚后保留阅读位置，查看最新后恢复跟随',async t=>{
  const h=mount(t,{...record(),logs:'Pulling image rancher/k3s\n'})
  await vue.nextTick()
  const log=walk(h.root).find(n=>n.props.role==='log')
  assert.equal(log.props['aria-label'],'安装执行日志')
  Object.assign(log,{scrollHeight:1000,clientHeight:200,scrollTop:800})
  log.props.onScroll()
  h.state.installation.logs+='layer-a: Downloading 1MB/10MB\n';await vue.nextTick();await vue.nextTick()
  assert.equal(log.scrollTop,1000)
  Object.assign(log,{scrollHeight:1200,scrollTop:100});log.props.onScroll()
  h.state.installation.logs+='layer-a: Download complete\n';await vue.nextTick();await vue.nextTick()
  assert.equal(log.scrollTop,100)
  walk(h.root).find(n=>n.tag==='button'&&text(n)==='查看最新').props.onClick()
  assert.equal(log.scrollTop,1200)
  log.scrollHeight=1300;h.state.installation.logs+='Starting k3s\n';await vue.nextTick();await vue.nextTick()
  assert.equal(log.scrollTop,1300)
})
