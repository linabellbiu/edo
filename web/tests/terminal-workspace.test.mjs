import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import * as vue from 'vue'
import * as pinia from 'pinia'
import { parse, compileScript, compileTemplate, compileStyle } from '@vue/compiler-sfc'
import { loadFrontend } from './acceptanceHarness.mjs'

const settle = async () => { await vue.nextTick(); await new Promise(resolve => setImmediate(resolve)); await vue.nextTick() }
const files = loadFrontend('../src/utils/terminalTarget.ts')
const makeAuth = (permissions = ['host.terminal.open','terminal.open','terminal.file.read','terminal.file.upload','terminal.file.edit']) => vue.reactive({ user: {id:'operator',department_id:'department-a',project_id:'project-a',is_superuser:false,permissions}, canAny(required) { return Boolean(this.user?.is_superuser || required.some(code => this.user?.permissions.includes(code))) } })
const host = (id='host-a', name='同名主机') => ({id,name,mode:'ssh',is_active:true,capabilities:[{kind:'ssh',status:'ready'}]})

function storeHarness() {
  const auth = makeAuth(), storage = {project:'project-a',getItem(){return this.project}}
  const module = loadFrontend('../src/stores/hostTerminals.ts', {vue,pinia,'@/stores/auth':{useAuthStore:()=>auth}}, {localStorage:storage})
  const instance = pinia.createPinia(), store = module.useHostTerminalsStore(instance)
  return {auth,storage,module,store,stop:()=>instance._s.clear()}
}

function sessionHarness() {
  const auth=makeAuth(),sockets=[],terminals=[],observers=[],emissions=[],unmounts=[],listeners=new Map(),storage={project:'project-a',getItem(){return this.project}}
  class Socket {static CONNECTING=0;static OPEN=1;readyState=0;sent=[];closes=[];constructor(url,protocol){this.url=url;this.protocol=protocol;sockets.push(this)}send(data){this.sent.push(data)}close(...args){this.closes.push(args);this.readyState=3}}
  class View {cols=100;rows=30;output=[];disposed=false;focused=0;constructor(options){this.options=options;terminals.push(this)}loadAddon(){}open(){}write(value){this.output.push(value)}writeln(value){this.output.push(value)}dispose(){this.disposed=true}focus(){this.focused++}onData(fn){this.input=fn;return{dispose(){}}}onResize(fn){this.resize=fn;return{dispose(){}}}}
  const utilities=loadFrontend('../src/stores/hostTerminals.ts',{vue,pinia,'@/stores/auth':{useAuthStore:()=>auth}},{localStorage:storage})
  const props=vue.reactive({path:'/api/v1/terminals/hosts/host-a/ws?project_id=project-a',visible:true,permission:'host.terminal.open'})
  const component=loadFrontend('../src/components/TerminalSessionPane.vue',{
    vue:{...vue,onBeforeUnmount:fn=>unmounts.push(fn)},'@xterm/xterm':{Terminal:View},'@xterm/addon-fit':{FitAddon:class{fit(){}}},'@/stores/auth':{useAuthStore:()=>auth},'@/stores/hostTerminals':utilities,
    '@/utils/socketUrl':loadFrontend('../src/utils/socketUrl.ts',{}, {location:{protocol:'http:',host:'edo.invalid'},localStorage:storage}),
  },{localStorage:storage,WebSocket:Socket,ResizeObserver:class{constructor(fn){this.fn=fn;observers.push(this)}observe(){}disconnect(){this.disconnected=true}},window:{addEventListener:(key,fn)=>listeners.set(key,fn),removeEventListener:key=>listeners.delete(key)}}).default
  const scope=vue.effectScope();let state
  scope.run(()=>{state=component.setup(props,{expose(){},emit:(...args)=>emissions.push(args)})})
  state.container.value={getBoundingClientRect:()=>({width:900,height:400})}
  return{auth,storage,props,state,sockets,terminals,observers,emissions,listeners,async mount(){await settle()},stop(){unmounts.forEach(fn=>fn());scope.stop()}}
}

function browserHarness(codes, handlers={}) {
  const auth=makeAuth(codes),requests=[],confirmations=[],notifications=[],unmounts=[],listeners=new Map(),storage={project:'project-a',getItem(){return this.project}},downloads=[]
  const actor=loadFrontend('../src/stores/hostTerminals.ts',{vue,pinia,'@/stores/auth':{useAuthStore:()=>auth}},{localStorage:storage})
  const entry={name:'app.conf',path:'/app.conf',kind:'file',size:10,etag:'old-hash',content:'before'}
  const request=async(method,path,data,options)=>{
    requests.push({method,path,data,options})
    if(handlers[method]){const result=handlers[method](path,data,options);if(result!==undefined)return result}
    if(path.endsWith('/content')&&method==='get')return{data:{file:{...entry}}}
    if(path.endsWith('/stat'))throw{response:{status:404}}
    if(path.endsWith('/download'))return{data:new Blob(['download'])}
    if(method==='put')return{data:{file:{...entry,etag:'new-hash',size:data.content.length}}}
    if(method==='post')return{data:{file:entry}}
    return{data:{directory:{path:options.params.path,entries:[{name:'etc',path:'/etc',kind:'directory',size:0},entry,{name:'link',path:'/link',kind:'symlink',size:5}],truncated:false}}}
  }
  class BrowserURL extends URL {static createObjectURL(){return'blob:preview'}static revokeObjectURL(){}}
  const props=vue.reactive({path:'/api/v1/terminals/hosts/host-a/ws?project_id=project-a'})
  const component=loadFrontend('../src/components/TerminalFileBrowser.vue',{
    vue:{...vue,onBeforeUnmount:fn=>unmounts.push(fn)},'@/stores/auth':{useAuthStore:()=>auth},'@/stores/hostTerminals':actor,'@/utils/terminalTarget':files,
    '@/api/client':{__esModule:true,default:{get:(path,options)=>request('get',path,undefined,options),put:(path,data,options)=>request('put',path,data,options),post:(path,data,options)=>request('post',path,data,options)}},
    '@/api/resources':{apiErrorMessage:error=>error.message??'安全错误'},'ant-design-vue':{Modal:{confirm:options=>{const instance={options,destroy(){options.afterClose?.()}};confirmations.push(instance);return instance}},message:Object.fromEntries(['error','success','warning'].map(kind=>[kind,text=>notifications.push({kind,text})]))},
  },{localStorage:storage,window:{addEventListener:(key,fn)=>listeners.set(key,fn),removeEventListener:key=>listeners.delete(key),setTimeout:fn=>fn()},document:{createElement:()=>({click(){downloads.push(this.download)}})},URL:BrowserURL}).default
  const scope=vue.effectScope();let state
  scope.run(()=>{state=component.setup(props,{expose(){}})})
  const input={value:'',click(){}}
  state.uploadInput.value=input
  return{auth,storage,props,state,entry,requests,confirmations,notifications,listeners,downloads,selectUpload(event){state.uploadHere();return state.upload(event)},stop(){unmounts.forEach(fn=>fn());scope.stop()}}
}

test('终端文件目标只允许三类准确接口，保留冻结项目和Pod UID，不接受外站或任意API',()=>{
  for(const path of ['/api/v1/terminals/hosts/h/ws','/api/v1/terminals/docker/d/containers/c/ws','/api/v1/terminals/kubernetes/k/namespaces/n/pods/p/containers/c/ws'])assert.equal(files.terminalFileTarget(`${path}?project_id=a&pod_uid=u&shell=bash`).base,path.replace('/api/v1','').replace('/ws','/files'))
  assert.deepEqual(files.terminalFileTarget('/api/v1/terminals/hosts/h/ws?project_id=a&pod_uid=u&shell=bash').parameters,{project_id:'a',pod_uid:'u'})
  for(const path of ['https://evil.invalid/api/v1/terminals/hosts/h/ws','/api/v1/hosts/h','/api/v1/terminals/ssh/h/ws','/api/v1/terminals/hosts/h/files'])assert.equal(files.terminalFileTarget(path),undefined)
  for(const name of ['..','.','a/b','a\\b','a\u0000b',''])assert.equal(files.terminalChild('/',name),undefined)
  assert.equal(files.terminalChild('/etc','app.conf'),'/etc/app.conf');assert.equal(files.terminalParent('/etc/app'),'/etc');assert.equal(files.terminalParent('/'),'/')
})

test('主机终端默认恢复已有会话，同主机可显式新增，按UUID而非同名分组',t=>{
  const h=storeHarness();t.after(h.stop)
  const first=h.store.open(host());h.store.minimize();assert.equal(h.store.minimized,true)
  assert.deepEqual(h.store.open(host()),first);assert.equal(h.store.sessions.length,1);assert.equal(h.store.minimized,false)
  h.store.duplicate(first.id);h.store.open(host('host-b'))
  assert.equal(h.store.sessions.length,3);assert.deepEqual(h.store.groups.map(group=>[group.id,group.sessions.length]),[['host-a',2],['host-b',1]])
  assert.equal(new URL(h.store.sessions[0].path,'http://edo.invalid').searchParams.get('project_id'),'project-a')
  const active=h.store.activeId;h.store.close(active);assert.equal(h.store.sessions.length,2);assert.ok(h.store.active)
})

test('主机会话上限8个，权限独立于容器权限，不连接停用或不支持的主机',t=>{
  const h=storeHarness();t.after(h.stop)
  h.auth.user.permissions=['terminal.open'];assert.match(h.store.open(host()).error,/权限/)
  h.auth.user.permissions=['host.terminal.open'];assert.match(h.store.open({...host(),is_active:false}).error,/不可用/)
  assert.match(h.store.open({...host(),capabilities:[]}).error,/不可用/)
  for(let i=0;i<8;i++)assert.ok(h.store.open(host(`h-${i}`)).id)
  assert.equal(h.store.sessions.length,8);assert.match(h.store.open(host('ninth')).error,/8/)
  assert.match(h.store.duplicate(h.store.activeId).error,/8/)
})

test('主机会话在身份、部门、项目及权限变化时清空，不将旧目标带到新项目',t=>{
  for(const change of [h=>h.auth.user.id='other',h=>h.auth.user.department_id='other',h=>h.auth.user.project_id='other',h=>h.auth.user.permissions=[],h=>{h.storage.project='other';h.store.syncContext()},h=>h.auth.user=null]){
    const h=storeHarness();t.after(h.stop);h.store.open(host());change(h);assert.equal(h.store.sessions.length,0);assert.equal(h.store.activeId,'')
  }
})

test('最小化与恢复保留原WebSocket/xterm，输入和尺寸协议保持不变，scrollback有限',async t=>{
  const h=sessionHarness();t.after(h.stop);await h.mount()
  assert.equal(h.sockets.length,1);const socket=h.sockets[0],view=h.terminals[0]
  assert.equal(view.options.scrollback,1000);assert.equal(socket.protocol,'edo-terminal-v1');assert.equal(new URL(socket.url).searchParams.get('project_id'),'project-a')
  socket.readyState=1;socket.onmessage({data:JSON.stringify({type:'ready'})});view.input('input');assert.equal(new TextDecoder().decode(socket.sent[0]),'input')
  h.props.visible=false;await settle();view.resize({cols:100,rows:30});assert.equal(socket.sent.length,1)
  socket.onmessage({data:new TextEncoder().encode('background output').buffer});assert.ok(view.output.includes('background output'))
  h.props.visible=true;await settle();view.resize({cols:120,rows:40});assert.deepEqual(JSON.parse(socket.sent[1]),{type:'resize',columns:120,rows:40})
  assert.equal(h.sockets.length,1);assert.equal(h.terminals.length,1);assert.equal(view.disposed,false)
})

test('关闭CONNECTING会话也释放socket/observer，迟到回调不复活终端',async()=>{
  const h=sessionHarness();await h.mount();const socket=h.sockets[0],callback=socket.onmessage,view=h.terminals[0],before=view.output.length
  h.stop();assert.equal(socket.closes.length,1);assert.equal(view.disposed,true);assert.equal(h.observers[0].disconnected,true);assert.equal(h.listeners.size,0)
  callback({data:new TextEncoder().encode('late').buffer});assert.equal(view.output.length,before)
})

test('终端权限撤销及项目切换立即拒绝输入并断开，不自动重新连接',async t=>{
  for(const change of [h=>h.auth.user.permissions=[],h=>{h.storage.project='other';h.listeners.get('edo:projects-changed')()}]){
    const h=sessionHarness();t.after(h.stop);await h.mount();const socket=h.sockets[0];socket.readyState=1;change(h);await settle();assert.equal(socket.readyState,3);h.terminals[0].input('not allowed');assert.equal(socket.sent.length,0);assert.equal(h.sockets.length,1)
  }
})

test('目录按需单层读取，符号链接不能冒充文件预览，读权限缺失不探测目标',async t=>{
  const h=browserHarness(['terminal.file.read']);t.after(h.stop);await settle();assert.equal(h.requests.length,1);assert.equal(h.requests[0].options.params.path,'/')
  assert.equal(h.state.items.value[0].kind,'directory');await h.state.preview({kind:'symlink',path:'/link',size:10});assert.equal(h.requests.length,1)
  const denied=browserHarness(['terminal.file.upload']);t.after(denied.stop);await settle();assert.equal(denied.requests.length,0)
})

test('文本预览只读时不可保存，超过1MiB不请求；编辑携带etag并保留失败草稿',async t=>{
  const read=browserHarness(['terminal.file.read']);t.after(read.stop);await settle();await read.state.preview({...read.entry,size:files.terminalTextLimit+1});assert.equal(read.requests.length,1)
  await read.state.preview(read.entry);read.state.draft.value='changed';await read.state.save();assert.equal(read.requests.some(row=>row.method==='put'),false)
  const edit=browserHarness(['terminal.file.read','terminal.file.edit'],{put:()=>Promise.reject(new Error('文件已变化，请重新读取后再操作'))});t.after(edit.stop);await settle();await edit.state.preview(edit.entry);edit.state.draft.value='changed';await edit.state.save()
  assert.equal(edit.requests.find(row=>row.method==='put').data.etag,'old-hash');assert.equal(edit.state.draft.value,'changed');assert.equal(edit.state.dirty.value,true);assert.match(edit.state.fileError.value,/已变化/)
})

test('文件成功保存采用新etag，关闭脏编辑明确确认且取消不丢内容',async t=>{
  const h=browserHarness(['terminal.file.read','terminal.file.edit']);t.after(h.stop);await settle();await h.state.preview(h.entry);h.state.draft.value='after';await h.state.save();assert.equal(h.state.editor.value.etag,'new-hash');assert.equal(h.state.dirty.value,false)
  h.state.draft.value='unsaved';const pending=h.state.closeEditor();await settle();assert.equal(h.confirmations.length,1);h.confirmations[0].options.onCancel();await pending;assert.equal(h.state.draft.value,'unsaved');assert.ok(h.state.editor.value)
})

test('上传新文件不附If-Match，覆盖必须read+edit并明确确认原SHA256',async t=>{
  const uploadEvent=()=>({target:{files:[Object.assign(new Blob(['payload']),{name:'app.conf'})],value:'selected'}})
  const fresh=browserHarness(['terminal.file.upload']);t.after(fresh.stop);await settle();await fresh.selectUpload(uploadEvent());assert.equal(fresh.requests.length,1);assert.equal(fresh.requests[0].method,'post');assert.equal(fresh.requests[0].options.headers['If-Match'],undefined)
  const existing={get:(path)=>path.endsWith('/stat')?{data:{file:{etag:'original-sha'}}}:undefined}
  const denied=browserHarness(['terminal.file.read','terminal.file.upload'],existing);t.after(denied.stop);await settle();await denied.selectUpload(uploadEvent());assert.equal(denied.requests.some(row=>row.method==='post'),false);assert.match(denied.state.error.value,/编辑权限/)
  const allowed=browserHarness(['terminal.file.read','terminal.file.upload','terminal.file.edit'],existing);t.after(allowed.stop);await settle();const pending=allowed.selectUpload(uploadEvent());await settle();assert.equal(allowed.requests.some(row=>row.method==='post'),false);allowed.confirmations[0].options.onOk();await pending
  assert.equal(allowed.requests.find(row=>row.method==='post').options.headers['If-Match'],'original-sha')
})

test('上传与下载限制64MiB，下载为blob attachment，不将原文当HTML执行',async t=>{
  const h=browserHarness(['terminal.file.read','terminal.file.upload']);t.after(h.stop);await settle();const before=h.requests.length
  await h.selectUpload({target:{files:[{name:'huge',size:files.terminalTransferLimit+1}],value:''}});await h.state.download({...h.entry,size:files.terminalTransferLimit+1});assert.equal(h.requests.length,before)
  await h.state.download(h.entry);assert.equal(h.requests.at(-1).options.responseType,'blob');assert.deepEqual(h.downloads,['app.conf'])
})

test('文件目标或权限切换取消旧请求，迟到响应不覆盖新目录；卸载清理监听',async t=>{
  let resolveOld
  const h=browserHarness(['terminal.file.read'],{get:(path,_body,options)=>path.endsWith('/files')&&options.params.path==='/'?new Promise(resolve=>{resolveOld=resolve}):undefined});t.after(h.stop)
  const old=h.requests[0];h.auth.user.permissions=[];await settle();assert.equal(old.options.signal.aborted,true)
  resolveOld({data:{directory:{path:'/old',entries:[h.entry],truncated:false}}});await settle();assert.deepEqual(h.state.entries.value,[]);assert.equal(h.state.editor.value,undefined)
  h.stop();assert.equal(h.listeners.size,0)
})

test('相同actor权限校准替换user对象不重置目录或未保存草稿，也不改变权限原数组',async t=>{
  const h=browserHarness(['terminal.file.read','terminal.file.edit']);t.after(h.stop);await settle()
  await h.state.load('/tmp/fixture-directory');await h.state.preview(h.entry);h.state.draft.value='unsaved fixture draft'
  const before=h.requests.length, editor=h.state.editor.value, permissions=[...h.auth.user.permissions]
  h.auth.user={...h.auth.user,nickname:'updated profile',permissions:[...permissions].reverse()};await settle()
  assert.equal(h.state.directory.value,'/tmp/fixture-directory')
  assert.equal(h.state.address.value,'/tmp/fixture-directory')
  assert.equal(h.state.editor.value,editor);assert.equal(h.state.draft.value,'unsaved fixture draft');assert.equal(h.state.dirty.value,true)
  assert.equal(h.requests.length,before)
  assert.deepEqual(h.auth.user.permissions,[...permissions].reverse())
})

test('相同actor的新user引用不会取消正在加载的目录或另发根目录请求',async t=>{
  let finish
  const h=browserHarness(['terminal.file.read'],{get:(path,_body,options)=>path.endsWith('/files')&&options.params.path==='/tmp/fixture-pending'?new Promise(resolve=>{finish=resolve}):undefined});t.after(h.stop);await settle()
  const pending=h.state.load('/tmp/fixture-pending'), current=h.requests.at(-1), before=h.requests.length
  h.auth.user={...h.auth.user,permissions:[...h.auth.user.permissions]};await settle()
  assert.equal(current.options.signal.aborted,false);assert.equal(h.requests.length,before);assert.equal(h.state.loading.value,true)
  finish({data:{directory:{path:'/tmp/fixture-pending',entries:[],truncated:false}}});await pending
  assert.equal(h.state.directory.value,'/tmp/fixture-pending');assert.equal(h.state.loading.value,false)
})

test('真实用户部门项目权限或目标变化仍清草稿并取消旧目录，迟到响应不能回填',async t=>{
  for(const change of [
    h=>{h.auth.user.id='another-user'},h=>{h.auth.user.department_id='another-department'},
    h=>{h.auth.user.project_id='another-project'},h=>{h.storage.project='another-project';h.listeners.get('storage')()},
    h=>{h.auth.user.permissions=['terminal.file.read']},h=>{h.props.path='/api/v1/terminals/hosts/host-b/ws?project_id=project-a'},
  ]){
    let finish
    const h=browserHarness(['terminal.file.read','terminal.file.edit'],{get:(path,_body,options)=>path.endsWith('/files')&&options.params.path==='/tmp/fixture-old'?new Promise(resolve=>{finish=resolve}):undefined});t.after(h.stop);await settle()
    await h.state.load('/tmp/fixture-current');await h.state.preview(h.entry);h.state.draft.value='sensitive fixture draft'
    const pending=h.state.load('/tmp/fixture-old'), old=h.requests.at(-1)
    change(h);await settle()
    assert.equal(old.options.signal.aborted,true);assert.equal(h.state.editor.value,undefined);assert.equal(h.state.draft.value,'');assert.equal(h.state.directory.value,'/')
    finish({data:{directory:{path:'/tmp/fixture-old',entries:[h.entry],truncated:false}}});await pending
    assert.equal(h.state.directory.value,'/');assert.equal(h.state.editor.value,undefined);assert.equal(h.state.draft.value,'')
  }
})

test('文件选择期间目录身份项目权限或目标变化会取消旧选择，不向新目录或新目标探测上传',async t=>{
  for(const change of [
    h=>h.state.load('/tmp/another-directory'),async h=>{await h.state.load('/tmp/another-directory');await h.state.load('/tmp/fixture-directory')},
    h=>{h.auth.user.id='another-user'},h=>{h.auth.user.department_id='another-department'},
    h=>{h.auth.user.project_id='another-project'},h=>{h.storage.project='another-project';h.listeners.get('storage')()},
    h=>{h.auth.user.permissions=['terminal.file.read','terminal.file.upload']},h=>{h.props.path='/api/v1/terminals/hosts/host-b/ws?project_id=project-a'},
  ]){
    const h=browserHarness(['terminal.file.read','terminal.file.upload','terminal.file.edit']);t.after(h.stop);await settle();await h.state.load('/tmp/fixture-directory')
    h.state.uploadHere();await change(h);await settle();const before=h.requests.length
    await h.state.upload({target:{files:[Object.assign(new Blob(['fixture']),{name:'upload.txt'})],value:'selected'}})
    assert.equal(h.requests.length,before);assert.equal(h.requests.some(row=>row.method==='post'),false)
  }
})

test('文件选择保持点击时目录且同actor校准不影响，取消及重复事件不能复用旧选择',async t=>{
  const h=browserHarness(['terminal.file.read','terminal.file.upload']);t.after(h.stop);await settle();await h.state.load('/tmp/fixture-directory')
  const event=()=>({target:{files:[Object.assign(new Blob(['fixture']),{name:'upload.txt'})],value:'selected'}})
  h.state.uploadHere();h.auth.user={...h.auth.user,permissions:[...h.auth.user.permissions].reverse()};await settle()
  await h.state.upload(event());assert.equal(h.requests.find(row=>row.method==='post').options.params.path,'/tmp/fixture-directory/upload.txt')
  await settle();let before=h.requests.length;await h.state.upload(event());assert.equal(h.requests.length,before)
  h.state.uploadHere();await h.state.upload({target:{files:[],value:''}});before=h.requests.length;await h.state.upload(event());assert.equal(h.requests.length,before)
  h.state.uploadHere();h.state.cancelUploadSelection();before=h.requests.length;await h.state.upload(event());assert.equal(h.requests.length,before)
  await h.selectUpload(event());assert.equal(h.requests.filter(row=>row.method==='post').length,2)
})

test('只有上传权限时也冻结显式目录，选择期间改地址不会转为新路径',async t=>{
  const h=browserHarness(['terminal.file.upload']);t.after(h.stop);await settle();h.state.address.value='/tmp/fixture-directory';h.state.uploadHere();h.state.address.value='/tmp/other'
  await h.state.upload({target:{files:[Object.assign(new Blob(['fixture']),{name:'upload.txt'})],value:'selected'}})
  assert.equal(h.requests.length,1);assert.equal(h.requests[0].options.params.path,'/tmp/fixture-directory/upload.txt')
})

test('文件stat期间换目录及组件卸载都不能让旧选择继续提交',async t=>{
  for(const change of [h=>h.state.load('/tmp/another-directory'),h=>h.stop()]){
    let finish
    const h=browserHarness(['terminal.file.read','terminal.file.upload'],{get:path=>path.endsWith('/stat')?new Promise(resolve=>{finish=resolve}):undefined});t.after(h.stop);await settle();await h.state.load('/tmp/fixture-directory')
    const pending=h.selectUpload({target:{files:[Object.assign(new Blob(['fixture']),{name:'upload.txt'})],value:'selected'}});await settle()
    await change(h);finish({data:{file:{etag:''}}});await pending
    assert.equal(h.requests.some(row=>row.method==='post'),false)
  }
})

test('覆盖确认期间项目或上传权限变化会取消操作，不向旧目标提交文件',async t=>{
  for(const change of [h=>h.auth.user.permissions=['terminal.file.read'],h=>{h.storage.project='other';h.listeners.get('edo:projects-changed')()}]){
    const h=browserHarness(['terminal.file.read','terminal.file.upload','terminal.file.edit'],{get:path=>path.endsWith('/stat')?{data:{file:{etag:'old'}}}:undefined});t.after(h.stop);await settle()
    const pending=h.selectUpload({target:{files:[Object.assign(new Blob(['payload']),{name:'app.conf'})],value:''}});await settle();assert.equal(h.confirmations.length,1)
    const old=h.confirmations[0];change(h);await settle();old.options.onOk();await pending
    assert.equal(h.requests.some(row=>row.method==='post'),false)
  }
})

test('终端快速换目标时不会留下原目标的CONNECTING连接',async t=>{
  const h=sessionHarness();t.after(h.stop);h.props.path='/api/v1/terminals/hosts/host-b/ws?project_id=project-a';await h.mount()
  assert.equal(h.sockets.length,1);assert.match(h.sockets[0].url,/hosts\/host-b\/ws/)
})

test('主机程序栏出现时为内容及键盘滚动预留底部空间，关闭后不改变全站布局',()=>{
  const source=readFileSync(new URL('../src/components/TerminalDock.vue',import.meta.url),'utf8')
  const {descriptor}=parse(source)
  const css=descriptor.styles.map(style=>compileStyle({id:'dock',source:style.content,scoped:true}).code).join('\n')
  assert.match(css,/body:has\(\.terminal-program-bar\) \.vben-content\s*\{\s*padding-bottom:calc\(60px/)
  assert.match(css,/html:has\(\.terminal-program-bar\)\s*\{\s*scroll-padding-bottom:/)
  assert.match(css,/body:has\(\.terminal-program-bar\) \.vben-content :is\([^}]+scroll-margin-bottom:/)
  assert.match(css,/@media\(max-width:700px\)[^{]*\{body:has\(\.terminal-program-bar\) \.vben-content\s*\{\s*padding-bottom:calc\(54px/)
})

test('UI组件独立可编译；只有主机Dock提供最小化，容器抽屉没有最小化且保留左侧文件区',()=>{
  for(const name of ['TerminalDock','TerminalSessionPane','TerminalFileBrowser','TerminalDrawer']){
    const source=readFileSync(new URL(`../src/components/${name}.vue`,import.meta.url),'utf8'),{descriptor,errors}=parse(source)
    assert.deepEqual(errors,[]);compileScript(descriptor,{id:name});assert.deepEqual(compileTemplate({id:name,source:descriptor.template.content}).errors,[])
    for(const style of descriptor.styles)assert.deepEqual(compileStyle({id:name,source:style.content,scoped:true}).errors,[])
  }
  const drawer=readFileSync(new URL('../src/components/TerminalDrawer.vue',import.meta.url),'utf8'),dock=readFileSync(new URL('../src/components/TerminalDock.vue',import.meta.url),'utf8'),app=readFileSync(new URL('../src/App.vue',import.meta.url),'utf8')
  assert.doesNotMatch(drawer,/最小化|useHostTerminalsStore/);assert.match(drawer,/TerminalFileBrowser/);assert.match(dock,/v-show="!store.minimized"/);assert.match(dock,/主机终端程序栏/);assert.match(app,/defineAsyncComponent\(\(\) => import\('@\/components\/TerminalDock.vue'\)\)/)
})
