import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'
import * as compose from '../src/utils/composeRuntime.ts'

const source=readFileSync(new URL('../src/components/ComposeRuntimeServices.vue',import.meta.url),'utf8')
const {outputText}=ts.transpileModule(compileScript(parse(source).descriptor,{id:'compose-runtime-services'}).content,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022}})
function harness(canLogs=true,canTerminal=true){
  const containers=[{id:'a'.repeat(64),name:'api-1',state:'running',running:true,restart_count:0},{id:'b'.repeat(64),name:'api-2',state:'unhealthy',running:true,restart_count:3}]
  const props=vue.reactive({services:[{name:'api',state:'running',count:2,ready_count:2,containers},{name:'setup',state:'completed',count:1,ready_count:1,containers:[{id:'c'.repeat(64),name:'setup-1',state:'completed',running:false,restart_count:0}]}],canLogs,canTerminal})
  const events=[],module={exports:{}}
  const modules={vue:{...vue,watch(){}},'@/utils/composeRuntime':compose}
  new Function('require','module','exports',outputText)(name=>modules[name]||{},module,module.exports)
  const state=module.exports.default.setup(props,{expose(){},emit:(...event)=>events.push(event)})
  return {state,props,events,containers}
}
test('逐容器展示多副本，不把创建时间、重启次数聚合成服务的虚假值',()=>{
  const {state,props,containers}=harness()
  assert.deepEqual(state.rows.value.map(row=>[row.service.name,row.container.id,row.container.restart_count]),[['api',containers[0].id,0],['api',containers[1].id,3],['setup','c'.repeat(64),0]])
  assert.equal(state.rows.value[0].span,2)
  state.selectedID.value=containers[1].id
  assert.equal(state.selectedContainer.value.name,'api-2')
  props.services[0].containers=containers.slice(0,1)
  assert.equal(state.selectedContainer.value,undefined,'容器消失后不自动换为第一个副本')
  props.services=[{name:'missing',state:'missing',count:0,ready_count:0}]
  assert.equal(state.rows.value[0].container,undefined)
})
test('日志可读已完成任务，终端只允许有权限且仍在运行的准确容器',()=>{
  const {state,events,containers}=harness()
  state.openStream(containers[1].id,true)
  state.openStream('c'.repeat(64))
  state.openStream('c'.repeat(64),true)
  state.openStream('missing',true)
  assert.deepEqual(events,[['terminal',containers[1].id],['logs','c'.repeat(64)]])
  for(const flags of [[false,true],[true,false]]){
    const denied=harness(...flags)
    denied.state.openStream(denied.containers[0].id,true)
    assert.deepEqual(denied.events,[])
  }
  for(const state of ['paused','restarting','exited','completed','failed'])assert.equal(compose.composeContainerCanTerminal({state,running:true}),false)
  assert.equal(compose.composeContainerCanTerminal({state:'running',running:true,terminal_unavailable_reason:'缺少 init'}),false)
  assert.equal(compose.composeRuntimeStatus('completed',true).label,'已完成')
  assert.equal(compose.composeRuntimeColor('running'),'green')
  assert.equal(compose.composePortLabel({container_port:'80/tcp',host_ip:'::',host_port:'8080'}),'[::]:8080 → 80/tcp')
  assert.equal(compose.composePortLabel({container_port:'443/tcp'}),'443/tcp')
})

test('容器重启、停止、服务重建分别按权限显示和分发，不借用日志或终端权限',()=>{
  const {state,props,events,containers}=harness(false,false)
  props.canStop=true
  state.control(containers[0].id,'stop')
  state.control(containers[0].id,'restart')
  state.control(containers[0].id,'recreate')
  state.control('c'.repeat(64),'stop')
  assert.deepEqual(events,[['control',containers[0].id,'stop']])
  props.canRecreate=true
  state.control(containers[1].id,'recreate')
  assert.deepEqual(events.at(-1),['control',containers[1].id,'recreate'])
  props.opening=true
  state.control(containers[0].id,'stop')
  assert.equal(events.length,2)
})
