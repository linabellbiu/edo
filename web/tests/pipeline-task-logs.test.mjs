import assert from 'node:assert/strict'
import {test} from 'node:test'
import * as vue from 'vue'
import {componentHarness} from './acceptanceHarness.mjs'

function harness(t) {
 const pending=[],sockets=[]
 const props=vue.reactive({open:true,runID:'run-a',nodeID:'one',title:'任务日志',initialStatus:'running'})
 const h=componentHarness('../src/components/PipelineLogDrawer.vue',{
  props,
  api:{get:(path,options)=>new Promise(resolve=>pending.push({path,options,resolve}))},
  modules:{'@/utils/liveSocket':{openLiveSocket:options=>{const socket={options,closed:false};sockets.push(socket);return()=>{socket.closed=true}}}},
  globals:{AbortController},
 })
 t.after(h.stop)
 const flush=async()=>{await vue.nextTick();await Promise.resolve();await vue.nextTick()}
 const resolve=async(index,logs=[],status='running')=>{pending[index].resolve({data:{logs,status,next_after_id:logs.at(-1)?.id||0}});await flush()}
 return {...h,props,pending,sockets,flush,resolve}
}

test('同一次运行切换任务取消旧请求、重置日志与游标，并丢弃迟到响应',async t=>{
 const h=harness(t)
 assert.match(h.pending[0].path,/node_id=one/)
 h.props.nodeID='two';await h.flush()
 assert.equal(h.pending[0].options.signal.aborted,true)
 await h.resolve(0,[{id:99,node_id:'one',message:'迟到'}])
 assert.equal(h.sockets.length,0)
 await h.resolve(1,[{id:7,node_id:'two',message:'任务二'},{id:8,node_id:'one',message:'不能混入'}])
 assert.deepEqual(h.state.logs.value.map(x=>x.message),['任务二'])
 const previous=h.sockets[0]
 assert.match(previous.options.path,/node_id=two/)
 h.props.nodeID='one';await h.flush()
 assert.equal(previous.closed,true)
 assert.deepEqual(h.state.logs.value,[])
 previous.options.onEvent({type:'logs',logs:[{id:100,node_id:'two',message:'旧连接'}]})
 previous.options.onDenied()
 assert.equal(h.state.error.value,'')
 await h.resolve(2)
 assert.match(h.sockets[1].options.path,/after_id=0&node_id=one/)
 h.sockets[1].options.onEvent({type:'logs',logs:[{id:2,node_id:'one',message:'一'},{id:3,node_id:'two',message:'二'}]})
 assert.deepEqual(h.state.logs.value.map(x=>x.message),['一'])
 assert.match(h.sockets[1].options.path,/after_id=2&node_id=one/)
})

test('整条运行入口不带任务过滤，关闭后迟到数据不能重连',async t=>{
 const h=harness(t)
 h.props.nodeID='';await h.flush()
 assert.equal(h.pending[1].path.includes('node_id'),false)
 await h.resolve(1,[{id:1,message:'全局'},{id:2,node_id:'one',message:'任务'}],'succeeded')
 assert.equal(h.state.logs.value.length,2)
 assert.equal(h.sockets[0].options.path.includes('node_id'),false)
 h.props.open=false;await h.flush()
 assert.equal(h.sockets[0].closed,true)
 await h.resolve(0,[{id:55,node_id:'one'}])
 assert.equal(h.sockets.length,1)
})
