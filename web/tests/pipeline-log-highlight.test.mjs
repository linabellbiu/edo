import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {test} from 'node:test'
import {baseParse,compile} from '@vue/compiler-dom'
import {parse} from '@vue/compiler-sfc'
import {renderToString} from '@vue/server-renderer'
import * as vue from 'vue'
import Anser from 'anser'
import {loadFrontend} from './acceptanceHarness.mjs'
const {pipelineLogLines,highlightPipelineLogs}=loadFrontend('../src/utils/pipelineLogHighlight.ts',{anser:Anser})

test('任意工具的错误文案不参与判断，显式级别与输出来源决定基础样式',()=>{
 for(const text of ['Error dependency api failed to start','container is unhealthy','curl: permission denied','请求已超时','ERROR_COUNT=0','任意编译器输出','registry/team/error-service:v4']){
  assert.equal(pipelineLogLines(text,'output')[0].tone,'plain',text)
  assert.equal(pipelineLogLines(text,'error')[0].tone,'error',text)
  assert.equal(pipelineLogLines(text,'stderr')[0].tone,'stderr',text)
 }
 for(const [level,tone] of [['WARN','warning'],['fatal','error'],['command','command'],['success','success']])assert.equal(pipelineLogLines('不依赖任何固定文案',level)[0].tone,tone)
 assert.equal(pipelineLogLines('{"level":"error","message":"arbitrary"}','output')[0].tone,'error')
 assert.equal(pipelineLogLines('{"severity":"WARNING","message":"arbitrary"}','output')[0].tone,'warning')
 assert.equal(pipelineLogLines('{"level":"success"}','error')[0].tone,'error')
})
test('通用 ANSI 颜色、真彩色和装饰由标准解析器处理，保留可见文本和换行',()=>{
 const text='普通\n\x1b[31;1m任意工具诊断\x1b[0m\n\x1b[38;2;12;34;56m任意内容\x1b[0m\n'
 const lines=pipelineLogLines(text,'output')
 assert.equal(lines.map(line=>line.text).join(''),'普通\n任意工具诊断\n任意内容\n')
 assert.equal(lines.find(line=>line.text==='任意工具诊断').style.fontWeight,'600')
 assert.ok(lines.find(line=>line.text==='任意工具诊断').style.color)
 assert.equal(lines.find(line=>line.text==='任意内容').style.color,'rgb(12,34,56)')
 assert.equal(pipelineLogLines('\x1b[8m不能隐藏的日志\x1b[0m','output')[0].style.visibility,undefined)
})
test('ANSI 状态跨记录延续但按任务和输出流隔离，重新读取相同历史结果一致',()=>{
 const logs=[{id:1,node_id:'a',stage:'build',level:'output',message:'\x1b[31m第一段\n'},{id:2,node_id:'b',stage:'build',level:'output',message:'其他任务\n'},{id:3,node_id:'a',stage:'build',level:'stderr',message:'错误流\n'},{id:4,node_id:'a',stage:'build',level:'output',message:'后续\x1b[0m正常\n'}]
 const values=highlightPipelineLogs(logs)
 assert.equal(values[0].lines[0].style.color,values[3].lines[0].style.color)
 assert.equal(values[1].lines[0].style.color,undefined)
 assert.equal(values[2].lines[0].style.color,undefined)
 assert.equal(values[3].lines.find(line=>line.text.startsWith('正常')).style.color,undefined)
 assert.deepEqual(highlightPipelineLogs(logs),values)
 assert.equal(highlightPipelineLogs([{...logs[0],message:'另一运行'}])[0].lines[0].style.color,undefined)
})
test('实际日志模板按元数据与 ANSI 渲染，HTML 和终端控制不能生成页面元素',async()=>{
 const descriptor=parse(readFileSync(new URL('../src/components/PipelineLogDrawer.vue',import.meta.url),'utf8')).descriptor
 const find=node=>node.tag==='div'&&node.props?.some(prop=>prop.name==='class'&&prop.value?.content==='log-viewport')?node:node.children?.map(find).find(Boolean)
 const {code}=compile(find(baseParse(descriptor.template.content)).loc.source,{mode:'function',prefixIdentifiers:true})
 const render=new Function('Vue',code)(vue)
 const logs=[{id:1,created_at:'2026-11-12T13:00:00Z',stage:'deploy',level:'error',message:'任意错误原文\n<script>alert(1)</script>\n'},{id:2,created_at:'2026-11-12T13:00:01Z',stage:'deploy',level:'output',message:'\x1b[36m任何命令\x1b[0m\n'},{id:3,created_at:'2026-11-12T13:00:02Z',stage:'deploy',level:'stderr',message:'独立输出流\n'}]
 const app=vue.createSSRApp({setup:()=>({title:'任务日志',logs,highlightedLogs:highlightPipelineLogs(logs),connection:'complete'}),render})
 const html=await renderToString(app)
 assert.match(html,/tone-error[^>]*>任意错误原文/)
 assert.match(html,/style="color:rgb\([^)]*\);"[^>]*>任何命令/)
 assert.match(html,/log-channel[^>]*>stderr/)
 assert.match(html,/&lt;script&gt;alert\(1\)&lt;\/script&gt;/)
 assert.doesNotMatch(html,/<script>|v-html/)
})
