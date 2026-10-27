import assert from 'node:assert/strict'
import {test} from 'node:test'
import * as vue from 'vue'
import {readFileSync} from 'node:fs'
import {parse,compileTemplate} from '@vue/compiler-sfc'
import {componentHarness, loadFrontend} from './acceptanceHarness.mjs'

const writes=h=>h.requests.filter(item=>item.method!=='get')
function harness(t,path,options={}) {const h=componentHarness(path,options);t.after(h.stop);return h}

test('远程主机必须先连接测试，字段改变撤销测试令牌，只提交测试过的连接上下文',async t=>{
  const h=harness(t,'../src/components/HostDrawer.vue',{props:{open:true,canTest:true},api:{post:async path=>path==='/hosts/test'?{data:{test_token:'host-test-ticket',architecture:'arm64'}}:{data:{}}}})
  await vue.nextTick()
  Object.assign(h.state.form,{name:'测试主机',address:'host.example.com',ssh_username:'ubuntu',ssh_auth_type:'password',password:'synthetic-host-password',capability_kinds:['ssh']})
  await vue.nextTick();await h.state.submit();assert.equal(writes(h).length,0)
  await h.state.testConnection()
  assert.equal(h.state.tested.value.test_token,'host-test-ticket')
  assert.equal(writes(h)[0].path,'/hosts/test')
  await h.state.submit()
  assert.equal(writes(h)[1].path,'/hosts');assert.equal(writes(h)[1].body.test_token,'host-test-ticket')
  assert.deepEqual(writes(h)[1].body.ssh,{password:'synthetic-host-password',use_sudo:false})
  assert.deepEqual(h.events,[{name:'saved',args:[]},{name:'update:open',args:[false]}])
  h.state.form.address='changed.example.com';await vue.nextTick()
  assert.equal(h.state.tested.value,undefined)
})

test('远程主机编辑默认复用保存的凭据，只有明确替换时提交新凭据',async t=>{
  const host={id:'host-a',name:'主机A',mode:'ssh',address:'host.example.com',ssh_username:'ubuntu',ssh_port:22,ssh_auth_type:'private_key',credential_configured:true,capabilities:[{kind:'docker',use_sudo:true}]}
  const h=harness(t,'../src/components/HostDrawer.vue',{props:{open:true,canTest:true,host}})
  await vue.nextTick()
  assert.equal(h.state.needsCredential.value,false)
  const original=h.state.payload()
  assert.equal(original.reuse_credential,true);assert.equal(original.ssh,undefined);assert.equal(original.use_sudo,true)
  h.state.replaceCredential.value=true
  h.state.form.private_key='synthetic-private-key';h.state.form.passphrase='synthetic-passphrase'
  const replacement=h.state.payload()
  assert.equal(replacement.reuse_credential,false)
  assert.deepEqual(replacement.ssh,{private_key:'synthetic-private-key',passphrase:'synthetic-passphrase',use_sudo:true})
  const denied=harness(t,'../src/components/HostDrawer.vue',{props:{open:true,canTest:false,host}})
  await denied.state.testConnection()
  assert.equal(writes(denied).length,0);assert.match(denied.notifications[0].value,/缺少.*权限/)
})

test('SSH安装sudo读取顶层配置，显式false优先旧Docker字段，缺字段才回退',async t=>{
  const base={id:'ssh-host',name:'安装主机',mode:'ssh',address:'host.example.com',ssh_username:'operator',ssh_port:22,ssh_auth_type:'password',credential_configured:true}
  for(const [patch,expected] of [[{use_sudo:true,capabilities:[{kind:'ssh'}]},true],[{use_sudo:false,capabilities:[{kind:'docker',use_sudo:true}]},false],[{capabilities:[{kind:'docker',use_sudo:true}]},true],[{capabilities:[{kind:'ssh'}]},false]]){
    const h=harness(t,'../src/components/HostDrawer.vue',{props:{open:true,canTest:true,host:{...base,...patch}}})
    await vue.nextTick();assert.equal(h.state.form.use_sudo,expected)
    assert.equal(h.state.payload().use_sudo,expected);assert.equal(h.state.payload().ssh,undefined)
  }
})

test('无Docker的SSH主机可启用安装sudo，复用凭据且开关变化必须重新测试',async t=>{
  const host={id:'ssh-only',name:'安装主机',mode:'ssh',address:'host.example.com',ssh_username:'operator',ssh_port:22,ssh_auth_type:'password',credential_configured:true,use_sudo:false,capabilities:[{kind:'ssh'}]}
  const h=harness(t,'../src/components/HostDrawer.vue',{props:{open:true,canTest:true,host},api:{post:async()=>({data:{test_token:'sudo-test-ticket',architecture:'amd64'}})}})
  await vue.nextTick();await h.state.testConnection();assert.equal(h.state.tested.value.test_token,'sudo-test-ticket')
  h.state.form.use_sudo=true;await vue.nextTick();assert.equal(h.state.tested.value,undefined)
  await h.state.submit();assert.equal(writes(h).length,1)
  await h.state.testConnection();await h.state.submit()
  const tested=writes(h)[1],saved=writes(h)[2]
  assert.equal(tested.path,'/hosts/ssh-only/test');assert.equal(saved.path,'/hosts/ssh-only')
  for(const request of [tested,saved]){assert.equal(request.body.use_sudo,true);assert.equal(request.body.reuse_credential,true);assert.equal(request.body.ssh,undefined);assert.deepEqual(request.body.capability_kinds,['ssh']);assert.equal(request.body.kubernetes_cluster_id,undefined)}
  assert.equal(saved.body.test_token,'sudo-test-ticket')
  h.state.form.use_sudo=false;await vue.nextTick();assert.equal(h.state.tested.value,undefined)
})

test('新SSH密码和私钥的sudo与顶层开关一致，不依赖Docker能力且本地不发送sudo',async t=>{
  for(const kind of ['password','private_key']){
    const h=harness(t,'../src/components/HostDrawer.vue',{props:{open:true,canTest:true}})
    await vue.nextTick();Object.assign(h.state.form,{ssh_auth_type:kind,password:'synthetic-password',private_key:'synthetic-key',passphrase:'synthetic-passphrase',use_sudo:true,capability_kinds:['ssh']})
    const enabled=h.state.payload();assert.equal(enabled.use_sudo,true);assert.equal(enabled.ssh.use_sudo,true)
    h.state.form.capability_kinds=['docker'];assert.equal(h.state.payload().use_sudo,true)
    h.state.form.capability_kinds=['ssh'];h.state.form.use_sudo=false;const disabled=h.state.payload();assert.equal(disabled.use_sudo,false);assert.equal(disabled.ssh.use_sudo,false)
  }
  const local=harness(t,'../src/components/HostDrawer.vue',{props:{open:true,host:{id:'local',mode:'local',name:'本地',capabilities:[]}}})
  await vue.nextTick();local.state.form.use_sudo=true;assert.equal('use_sudo' in local.state.payload(),false)
})

test('SSH安装sudo控件不再以Docker为显示条件，文案区分安装和部署脚本提权边界',()=>{
  const {descriptor,errors}=parse(readFileSync(new URL('../src/components/HostDrawer.vue',import.meta.url),'utf8'));assert.deepEqual(errors,[])
  assert.deepEqual(compileTemplate({source:descriptor.template.content,filename:'HostDrawer.vue',id:'host-install-sudo'}).errors,[])
  const nodes=[];function visit(node){if(node.type===1)nodes.push(node);for(const child of node.children??[])visit(child)}visit(descriptor.template.ast)
  const item=nodes.find(node=>node.tag==='a-form-item'&&node.props.some(prop=>prop.name==='class'&&prop.value?.content.includes('sudo-toggle')))
  assert.ok(item);assert.equal(item.props.some(prop=>prop.type===7&&prop.name==='if'),false)
  assert.ok(descriptor.template.content.includes("t('hostForm.installUseSudo')"));assert.ok(descriptor.template.content.includes("t('hostForm.installSudoHint')"))
  const {messages}=loadFrontend('../src/locales/index.ts',{'vue-i18n':{createI18n:options=>options}})
  const zh=messages['zh-CN'].hostForm,en=messages['en-US'].hostForm
  assert.equal(zh.installUseSudo,'安装操作使用 sudo');assert.match(zh.installSudoHint,/K3s.*Docker/);assert.match(zh.installSudoHint,/SSH 部署脚本不会自动提权/)
  assert.equal(typeof en.installUseSudo,'string');assert.match(en.installSudoHint,/K3s.*Docker/)
})

test('本地主机不发送SSH或伪造连接凭据，不可用Docker能力保持置灰',async t=>{
  const host={id:'local-host',name:'本地',mode:'local',capabilities:[{kind:'local_exec'}],capability_options:[{kind:'docker',available:false,reason:'Docker不可用'},{kind:'local_exec',available:true}]}
  const h=harness(t,'../src/components/HostDrawer.vue',{props:{open:true,host}})
  await vue.nextTick()
  h.state.form.capability_kinds=['local_exec','ssh'];h.state.form.password='should-not-send'
  assert.equal(h.state.localCapabilityDisabled('docker'),true)
  assert.equal(h.state.localCapabilityDisabled('local_exec'),false)
  await h.state.submit()
  assert.deepEqual(writes(h)[0].body,{name:'本地',mode:'local',capability_kinds:['local_exec']})
  assert.equal(writes(h)[0].path,'/hosts/local-host')
})

function environment(t,options={}) {
  const list=loadFrontend('../src/utils/resourceList.ts',{vue,'vue-i18n':{useI18n:()=>({t:key=>key})}})
  return harness(t,'../src/views/EnvironmentView.vue',{
    codes:['deployment.create','deployment.update'],
    modules:{'@/utils/resourceList':list,'@/api/infrastructure':{listEnvironments:async()=>[],listHosts:async()=>[],environmentIDsOf:host=>host.environment_ids||[]}},
    api:{post:async()=>({data:{environment:{id:'new-env'}}}),put:async()=>({data:{environment:{id:'environment-a'}}}),patch:async()=>({data:{environment:{id:'environment-a'}}})},
    ...options,
  })
}

test('环境创建、基本信息和主机成员分别提交，不让编辑名称改写主机关联',async t=>{
  const h=environment(t)
  h.state.create();Object.assign(h.state.form,{name:' 新环境 ',description:' 说明 ',host_ids:['host-a']})
  await h.state.save()
  assert.deepEqual(writes(h)[0].body,{name:'新环境',description:'说明',host_ids:['host-a']})
  const item={id:'environment-a',name:'环境A',description:'描述',hosts:[{id:'host-a'}]}
  h.state.edit(item,'details');h.state.form.name='修改名称';h.state.form.host_ids=['must-not-write']
  await h.state.save()
  assert.deepEqual(writes(h)[1],{method:'patch',path:'/environments/environment-a',body:{name:'修改名称',description:'描述'},options:undefined})
  h.state.edit(item,'hosts');h.state.form.host_ids=['host-b'];h.state.form.name='must-not-write'
  await h.state.save()
  assert.deepEqual(writes(h)[2],{method:'put',path:'/environments/environment-a/hosts',body:{host_ids:['host-b']},options:undefined})
})

test('环境保存失败保留表单与主机选择，停用中的异步请求不会重复提交',async t=>{
  const h=environment(t,{api:{post:async()=>{throw new Error('环境名称重复')}}})
  h.state.create();Object.assign(h.state.form,{name:'环境A',host_ids:['host-a']})
  await h.state.save()
  assert.equal(h.state.formOpen.value,true);assert.equal(h.state.form.name,'环境A');assert.deepEqual(h.state.form.host_ids,['host-a']);assert.equal(h.state.saving.value,false)
  let finish
  const pending=environment(t,{api:{patch:()=>new Promise(resolve=>finish=resolve)}})
  const first=pending.state.toggle({id:'environment-a',is_active:true})
  await pending.state.toggle({id:'environment-b',is_active:true})
  assert.equal(writes(pending).length,1)
  finish({data:{}});await first
  assert.equal(pending.state.mutatingID.value,'')
})

test('镜像仓库必须先真实测试，DockerHub地址固定且不允许HTTP，字段变更清除测试结果',async t=>{
  const deployments=loadFrontend('../src/utils/deploymentInstances.ts')
  const h=harness(t,'../src/views/DevOpsView.vue',{props:{section:'image-registries'},codes:['image_registry.read','image_registry.create','image_registry.execute'],modules:{'@/utils/deploymentInstances':deployments},globals:{document:{hidden:true}}})
  h.state.create();Object.assign(h.state.registryForm,{name:'镜像仓库',provider:'docker_hub',endpoint:'https://not-used.example',namespace:'team',username:'test-user',credential:'synthetic-registry-token',allow_insecure_http:true})
  await vue.nextTick()
  await h.state.save();assert.equal(writes(h).length,0)
  const payload=h.state.registryRequestPayload()
  assert.equal(payload.endpoint,'');assert.equal(payload.allow_insecure_http,false)
  await h.state.testRegistry()
  assert.equal(writes(h)[0].path,'/image-registries/test');assert.equal(h.state.registryTested.value,true)
  await h.state.save()
  assert.equal(writes(h)[1].path,'/image-registries')
  h.state.registryForm.namespace='changed-team';await vue.nextTick()
  assert.equal(h.state.registryTested.value,false)
})

test('总览只按实际健康检查计算可用性，保留最多24次页面采样',async t=>{
  const system=vue.reactive({ready:{status:'failed',checks:{database:'ok',redis:'failed',nats:'ok'}},refresh:async()=>{}})
  const h=harness(t,'../src/views/OverviewView.vue',{modules:{'@/stores/system':{useSystemStore:()=>system}}})
  assert.equal(h.state.healthPercent.value,67)
  for(let i=0;i<30;i++)await h.state.refresh()
  assert.equal(h.state.history.value.length,24)
  system.ready.checks.redis='ok';await h.state.refresh()
  assert.equal(h.state.healthPercent.value,100)
})
