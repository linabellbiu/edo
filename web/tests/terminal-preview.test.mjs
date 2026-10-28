import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { createTerminalPreviewAdapter } from './fixtures/terminal-preview-data.mjs'

const base='/terminals/hosts/preview-local/files'
test('终端隔离预览只登记测试目标，无未知网络透传；宿主和应用业务写操作继续拒绝',async()=>{
  const p=createTerminalPreviewAdapter()
  for(const url of ['https://outside.invalid/terminals/hosts/preview-local/files','/terminals/hosts/production/files','/terminals/ssh/preview-local/files','/terminals/hosts/preview-local/files/delete'])await assert.rejects(p.adapter({url,method:'get'}))
  for(const [url,method] of [['/hosts','post'],['/environments','delete'],['/docker/endpoints','put']])await assert.rejects(p.adapter({url,method}))
  const controller=new AbortController();controller.abort();await assert.rejects(p.adapter({url:base,signal:controller.signal}))
})
test('三类预览目录与文件接口包装符合真实契约，只有确认且SHA一致才可覆盖',async()=>{
  const p=createTerminalPreviewAdapter(),config={url:`${base}/content`,method:'get',params:{path:'/etc/app.conf'}}
  const initial=(await p.adapter(config)).data.file;assert.equal(initial.kind,'file');assert.match(initial.etag,/^[a-f0-9]{64}$/)
  const result=await p.adapter({url:`${base}/content`,method:'put',data:JSON.stringify({path:initial.path,content:'new content',etag:initial.etag})});assert.notEqual(result.data.file.etag,initial.etag)
  await assert.rejects(p.adapter({url:`${base}/content`,method:'put',data:JSON.stringify({path:initial.path,content:'stale',etag:initial.etag})}),err=>err.response.status===409)
  await assert.rejects(p.adapter({url:`${base}/upload`,method:'post',params:{path:initial.path},data:new Blob(['upload'])}),err=>err.response.status===409)
  await p.adapter({url:`${base}/upload`,method:'post',params:{path:initial.path},headers:{'If-Match':result.data.file.etag},data:new Blob(['upload'])})
  assert.equal((await p.adapter(config)).data.file.content,'upload')
  for(const target of ['/terminals/docker/preview-docker-local/containers/preview-container-web/files','/terminals/kubernetes/preview-k3s/namespaces/preview/pods/preview-web/containers/web/files'])assert.equal((await p.adapter({url:target,method:'get',params:{path:'/'}})).data.directory.path,'/')
})
test('终端预览不接服务器或开发库，模拟Socket明确标注，样式直接复用真实组件',()=>{
  const read=path=>readFileSync(new URL(path,import.meta.url),'utf8')
  const boot=read('./fixtures/terminal-workspace.mjs'),shell=read('./fixtures/TerminalWorkspacePreview.vue'),data=read('./fixtures/terminal-preview-data.mjs')
  assert.match(boot,/client.defaults.adapter=preview.adapter/);assert.match(boot,/window.fetch=/);assert.match(boot,/installTerminalPreviewSocket\(\)/);assert.match(boot,/createMemoryHistory/);assert.match(boot,/HostView/)
  assert.match(shell,/TerminalDock/);assert.match(shell,/TerminalDrawer/);assert.match(shell,/不连接服务器/);assert.match(data,/模拟会话，不执行真实命令/)
  assert.doesNotMatch(boot+shell+data,/192\.168\.2\.147|108\.186\.240\.14|localStorage.setItem|document.cookie/)
})
