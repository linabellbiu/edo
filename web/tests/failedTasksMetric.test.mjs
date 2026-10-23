import assert from 'node:assert/strict'
import {test} from 'node:test'
import * as vue from 'vue'
import {compile} from './pipelineRunTestHarness.mjs'

function harness({canRead=true,get}={}) {
 const requests=[],routes=[],auth=vue.reactive({user:{id:'user'},canAny:()=>canRead}),props=vue.reactive({count:2,label:'任务失败',display:'2'})
 const scope=vue.effectScope()
 const component=compile('../src/components/FailedTasksMetric.vue',{
  vue:{...vue,onBeforeUnmount(){}},'vue-router':{useRouter:()=>({push:location=>routes.push(location)})},
  '@/stores/auth':{useAuthStore:()=>auth},'@/api/resources':{apiErrorMessage:error=>error.message},
  '@/api/client':{default:{get:(...args)=>{requests.push(args);return get(...args)}}},
 }).default
 let state;scope.run(()=>{state=component.setup(props,{expose(){}})})
 return {state,props,auth,requests,routes,stop:()=>scope.stop()}
}
test('悬浮与点击共享请求，点击等待后打开准确失败详情',async t=>{
 let resolve
 const h=harness({get:()=>new Promise(done=>resolve=done)});t.after(h.stop)
 const hover=h.state.load(),click=h.state.openDetails()
 assert.equal(h.requests.length,1)
 resolve({data:{tasks:[{id:'exact-failed-task',kind:'pipeline.build',error_message:'构建失败'}]}})
 await hover;await click
 assert.deepEqual(h.requests[0][1].params,{status:'failed',limit:3})
 assert.equal(h.routes[0].query.task,'exact-failed-task');assert.equal(h.routes[0].query.status,'failed')
})
test('无权限与零失败数不发出请求，用户变化清除缓存并忽略迟到响应',async t=>{
 const denied=harness({canRead:false,get:assert.fail});t.after(denied.stop)
 await denied.state.load();await denied.state.openDetails();assert.equal(denied.requests.length,0);assert.equal(denied.routes.length,0)
 let resolve
 const h=harness({get:()=>new Promise(done=>resolve=done)});t.after(h.stop)
 const pending=h.state.load();h.auth.user.id='other-user';await vue.nextTick()
 resolve({data:{tasks:[{id:'private-old-task'}]}});await pending
 assert.deepEqual(h.state.tasks.value,[])
 h.props.count=0;await vue.nextTick();await h.state.openDetails();assert.equal(h.routes.length,0)
})
