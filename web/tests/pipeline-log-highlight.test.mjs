import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {test} from 'node:test'
import {baseParse,compile} from '@vue/compiler-dom'
import {parse} from '@vue/compiler-sfc'
import {renderToString} from '@vue/server-renderer'
import * as vue from 'vue'
import {loadFrontend} from './acceptanceHarness.mjs'
const {pipelineLogLines}=loadFrontend('../src/utils/pipelineLogHighlight.ts')

test('同一 Compose 输出记录逐行高亮真实错误，保留正常状态和完整原文',()=>{
 const text='[部署 · 主机] Container api Creating\nContainer api Started\nContainer api Error dependency api failed to start\ndependency failed to start: container api is unhealthy\n'
 const lines=pipelineLogLines(text,'output')
 assert.deepEqual(lines.map(line=>line.tone),['plain','plain','error','error','plain'])
 assert.equal(lines.map(line=>line.text).join(''),text)
})
test('完整命令、警告和错误级别区分显示，不把镜像名称或零错误当失败',()=>{
 for(const text of ['registry/team/error-service:v4','0 errors','no errors found','errors=0','没有错误','0 warnings'])assert.equal(pipelineLogLines(text,'output')[0].tone,'plain',text)
 assert.equal(pipelineLogLines("[部署] 执行命令： 'docker' 'compose' '--file' 'error.yml' 'up' '-d'",'output')[0].tone,'command')
 assert.equal(pipelineLogLines('WARN[0001] deprecated option','output')[0].tone,'warning')
 assert.equal(pipelineLogLines('[WARN] deprecated option','output')[0].tone,'warning')
 assert.equal(pipelineLogLines('time=now level=warning msg=deprecated','output')[0].tone,'warning')
 assert.equal(pipelineLogLines('[ERROR] missing config','output')[0].tone,'error')
 assert.equal(pipelineLogLines('no errors in config, startup failed to start','output')[0].tone,'error')
 assert.equal(pipelineLogLines('curl: permission denied','output')[0].tone,'error')
 assert.equal(pipelineLogLines('请求已超时','output')[0].tone,'error')
 assert.equal(pipelineLogLines('details\n  at app.go:42','error')[1].tone,'error')
 assert.equal(pipelineLogLines('Compose 部署与镜像验收完成','output')[0].tone,'success')
})
test('实际日志模板逐行渲染高亮，历史错误正文与实时输出均不执行 HTML',async()=>{
 const descriptor=parse(readFileSync(new URL('../src/components/PipelineLogDrawer.vue',import.meta.url),'utf8')).descriptor
 const find=node=>node.tag==='div'&&node.props?.some(prop=>prop.name==='class'&&prop.value?.content==='log-viewport')?node:node.children?.map(find).find(Boolean)
 const source=find(baseParse(descriptor.template.content)).loc.source
 const {code}=compile(source,{mode:'function',prefixIdentifiers:true})
 const render=new Function('Vue',code)(vue)
 const message='Container api Started\nContainer api Error dependency failed to start\n<script>alert(1)</script>\n'
 const app=vue.createSSRApp({setup:()=>({title:'任务日志',logs:[{id:1}],highlightedLogs:[{id:1,created_at:'2026-11-12T13:00:00Z',stage:'deploy',level:'output',lines:pipelineLogLines(message,'output')}],connection:'complete'}),render})
 const html=await renderToString(app)
 assert.match(html,/tone-error[^>]*>Container api Error dependency failed to start/)
 assert.match(html,/tone-plain[^>]*>Container api Started/)
 assert.match(html,/&lt;script&gt;alert\(1\)&lt;\/script&gt;/)
 assert.doesNotMatch(html,/<script>/)
})
