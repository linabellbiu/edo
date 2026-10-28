import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {test} from 'node:test'
import {computed,reactive} from 'vue'
import {compileScript,compileStyle,compileTemplate,parse} from '@vue/compiler-sfc'
import {createPreflightPreview,preflightPreviewHostID,preflightPreviewScenarios} from './fixtures/k3s-preflight-data.mjs'
const input={action:'create',mode:'native',name:'fixture-cluster',server_host_id:preflightPreviewHostID}
const request=(preview,url,method='get',extra={})=>preview.adapter({url,method,...extra})

test('专用检测夹具只返回内存报告，native与k3d按主机角色展示，不包含凭据',async()=>{
  for(const mode of ['native','k3d'])for(const scenario of ['passed','warning','failed']){
    const preview=createPreflightPreview(scenario),response=await request(preview,'/kubernetes/k3s/preflight','post',{data:{...input,mode,server_token:'must-not-return'}})
    assert.equal(response.data.passed,scenario!=='failed');assert.equal(response.data.hosts[0].role,mode==='k3d'?'host':'server');assert.equal(response.data.hosts[0].host_id,preflightPreviewHostID)
    assert.equal(response.data.hosts[0].checks.some(check=>check.status==='warning'),scenario==='warning');assert.equal(response.data.hosts[0].checks.some(check=>check.status==='failed'),scenario==='failed');assert.doesNotMatch(JSON.stringify(response.data),/must-not-return|password|server_token/)
    assert.ok((await request(preview,'/hosts')).data.hosts[0].address.startsWith('192.0.2.'));assert.deepEqual((await request(preview,'/kubernetes/k3s/installations')).data.installations,[])
  }
})

test('夹具未知/外部接口及所有plans和installations写均拒绝，不走真实网络',async()=>{
  const preview=createPreflightPreview()
  for(const path of ['/kubernetes/k3s/plans','/kubernetes/k3s/installations','/unknown','https://127.0.0.1:18180/api/v1/hosts','//192.0.2.24/hosts'])for(const method of ['post','patch','put','delete'])await assert.rejects(request(preview,path,method),/前端夹具/)
  assert.ok(preview.calls.every(call=>call.outcome==='rejected'))
  const denied=createPreflightPreview('denied');assert.equal(denied.permissions.includes('cluster.execute'),false);await assert.rejects(request(denied,'/kubernetes/k3s/preflight','post',{data:input}),error=>error.response.status===403)
})

test('延迟夹具响应支持真实AbortSignal取消，没有计划或安装副作用',async()=>{
  const calls=reactive([]),cancelled=computed(()=>calls.filter(call=>call.outcome==='cancelled').length),preview=createPreflightPreview('delay',{calls}),controller=new AbortController(),pending=request(preview,'/kubernetes/k3s/preflight','post',{data:JSON.stringify(input),signal:controller.signal});assert.equal(cancelled.value,0);controller.abort()
  await assert.rejects(pending,{code:'ERR_CANCELED'});assert.deepEqual(preview.calls,[{method:'post',path:'/kubernetes/k3s/preflight',outcome:'cancelled'}])
  assert.equal(cancelled.value,1)
})

test('独立入口挂真实Drawer和页面内Axios adapter，不覆盖全局网络或注册生产路由',()=>{
  const read=path=>readFileSync(new URL(path,import.meta.url),'utf8'),entry=read('./fixtures/k3s-preflight.mjs'),shell=read('./fixtures/K3sPreflightPreview.vue')
  assert.match(entry,/client.defaults.adapter=preview.adapter/);assert.doesNotMatch(entry,/window\.(?:fetch|WebSocket)\s*=/);assert.match(entry,/is_superuser:false/);assert.match(shell,/import K3sProvisionDrawer from '\/src\/components\/K3sProvisionDrawer.vue'/)
  assert.doesNotMatch(read('../src/router/index.ts'),/K3sPreflightPreview|k3s-preflight\.html/);assert.ok(preflightPreviewScenarios.includes('delay'));assert.match(shell,/非真实主机检测/)
  const {descriptor,errors}=parse(shell);assert.deepEqual(errors,[]);compileScript(descriptor,{id:'preflight-preview'});assert.deepEqual(compileTemplate({id:'preflight-preview',source:descriptor.template.content}).errors,[])
  for(const style of descriptor.styles)assert.deepEqual(compileStyle({id:'preflight-preview',source:style.content,scoped:style.scoped}).errors,[])
})
