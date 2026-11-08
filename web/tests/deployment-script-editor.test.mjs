import assert from 'node:assert/strict'
import {test} from 'node:test'
import * as vue from 'vue'
import * as state from '@codemirror/state'
import {loadFrontend} from './acceptanceHarness.mjs'
const settle=async()=>{await vue.nextTick();await new Promise(resolve=>setImmediate(resolve))}
function harness(post){
 const requests=[],events=[],unmounts=[],storage={project:'project'},timers=new Map()
 let timerID=0
 const auth=vue.reactive({authorizationState:'ready',user:{id:'actor',project_id:'project',permissions:['workflow.update']},canAny(codes){return codes.some(code=>this.user.permissions.includes(code))}})
 const props=vue.reactive({modelValue:'printf "%s" "${EDO_IMAGE_TAG}"',disabled:false})
 const component=loadFrontend('../src/components/DeploymentScriptEditor.vue',{
  vue:{...vue,onMounted(){},onBeforeUnmount:fn=>unmounts.push(fn)},'@codemirror/state':state,'@/stores/auth':{useAuthStore:()=>auth},'@/stores/preferences':{usePreferencesStore:()=>vue.reactive({theme:'light'})},'@/api/resources':{apiErrorMessage:()=> '检查失败'},'@/api/client':{__esModule:true,default:{post:async(path,body,options)=>{requests.push({path,body,options});return post?post():{data:{issues:[]}}}}}
 },{localStorage:{getItem:()=>storage.project},setTimeout(callback,delay){const id=++timerID;timers.set(id,{callback,delay});return id},clearTimeout:id=>timers.delete(id)}).default
 const scope=vue.effectScope(),value=scope.run(()=>component.setup(props,{expose(){},emit:(name,text)=>{events.push({name,text});props.modelValue=text}}))
 return {value,props,auth,storage,requests,events,timers,async flushChecks(){const pending=[...timers.values()];timers.clear();pending.forEach(item=>item.callback());await settle()},stop(){unmounts.forEach(fn=>fn());scope.stop()}}
}
test('部署脚本通过正常权限接口检查原文，结果展示语法范围',async t=>{
 const h=harness();t.after(h.stop);await h.value.check()
 assert.equal(h.requests[0].path,'/workflows/deployment-script/check')
 assert.equal(h.requests[0].body.script,'printf "%s" "${EDO_IMAGE_TAG}"')
 assert.equal(h.requests[0].options.headers['X-EDO-Project-ID'],'project')
 assert.match(h.value.status.value,/POSIX sh/)
})
test('上传脚本保留 UTF-8 原文，不执行上传内容，超限和撤权不写入',async t=>{
 const h=harness();t.after(h.stop)
 const text='#!/bin/sh\nprintf "%s" "${EDO_IMAGE_TAG}"  \n'
 const event={target:{value:'selected',files:[{size:text.length,arrayBuffer:async()=>new TextEncoder().encode(text).buffer}]}}
 await h.value.readUpload(event);assert.equal(h.events[0].text,text);assert.equal(event.target.value,'');assert.equal(h.requests.length,0)
 await h.value.readUpload({target:{value:'large',files:[{size:300000}]}});assert.equal(h.events.length,1);assert.match(h.value.error.value,/256 KiB/)
 h.auth.user.permissions=[];await h.value.readUpload(event);assert.equal(h.events.length,1)
})
test('切项目、改正文和撤权取消检查，迟到诊断不能恢复',async t=>{
 let resolve;const pending=new Promise(yes=>resolve=yes),h=harness(()=>pending);t.after(h.stop)
 const checking=h.value.check();h.props.modelValue='echo new';h.auth.user.project_id='other';h.storage.project='other';await settle()
 resolve({data:{issues:[{line:1,column:1,message:'旧错误'}]}});await checking
 assert.equal(h.requests[0].options.signal.aborted,true)
 assert.deepEqual(h.value.issues.value,[]);assert.doesNotMatch(h.value.status.value,/旧错误|检查通过/)
 h.auth.user.permissions=[];await settle();await h.value.check();assert.equal(h.requests.length,1)
})
test('输入自动合并检查，及时清理旧结果，并对当前正文展示语法诊断',async t=>{
 let count=0
 const h=harness(()=>({data:{issues:++count===1?[{line:2,column:3,message:'Shell 语法无效'}]:[]}}));t.after(h.stop)
 h.props.modelValue='if true; then\n';await settle()
 assert.equal(h.timers.size,1);assert.equal([...h.timers.values()][0].delay,500)
 h.props.modelValue='if true; then\n  echo ok';await settle()
 assert.equal(h.requests.length,0);assert.equal(h.timers.size,1)
 await h.flushChecks()
 assert.equal(h.requests.length,1);assert.equal(h.requests[0].body.script,h.props.modelValue)
 assert.equal(h.value.issues.value[0].line,2);assert.match(h.value.status.value,/未通过/)
 h.props.modelValue+='\nfi\n';await settle()
 assert.deepEqual(h.value.issues.value,[]);assert.doesNotMatch(h.value.status.value,/未通过|检查通过/)
 await h.flushChecks();assert.match(h.value.status.value,/检查通过（POSIX sh）/)
})
test('清空文本、禁用、撤权及卸载取消排队检查，不保留旧诊断',async t=>{
 const h=harness();t.after(h.stop)
 h.props.modelValue='echo update';await settle();assert.equal(h.timers.size,1)
 h.props.modelValue='';await settle();assert.equal(h.timers.size,0);assert.equal(h.value.status.value,'')
 h.props.modelValue='echo update';await settle();h.props.disabled=true;await settle()
 assert.equal(h.timers.size,0);await h.flushChecks();assert.equal(h.requests.length,0)
 h.props.disabled=false;await settle();assert.equal(h.timers.size,1)
 h.auth.user.permissions=[];await settle();assert.equal(h.timers.size,0)
 h.auth.user.permissions=['workflow.update'];await settle();assert.equal(h.timers.size,1)
 h.stop();assert.equal(h.timers.size,0)
})
test('自动检查失败不伪报通过，重新输入后自动重试',async t=>{
 let fail=true
 const h=harness(()=>{if(fail)throw Error('网络错误');return {data:{issues:[]}}});t.after(h.stop)
 h.props.modelValue='echo first';await settle();await h.flushChecks()
 assert.equal(h.value.error.value,'检查失败');assert.equal(h.value.status.value,'')
 fail=false;h.props.modelValue='echo retry';await settle();assert.equal(h.value.error.value,'')
 await h.flushChecks();assert.match(h.value.status.value,/检查通过/)
})

test('独立部署方案使用方案权限和独立语法检查接口，无需流水线修改权限',async t=>{
 const h=harness();t.after(h.stop)
 h.props.editPermission='deployment_plan.update';h.auth.user.permissions=['deployment_plan.update'];await settle()
 assert.equal(h.value.canEdit(),true)
 await h.value.check();assert.equal(h.requests.at(-1).path,'/deployment-plans/script/check')
 h.auth.user.permissions=[];await settle();const count=h.requests.length
 await h.value.check();assert.equal(h.requests.length,count)
})
