import assert from 'node:assert/strict'
import {test} from 'node:test'
import * as vue from 'vue'
import * as state from '@codemirror/state'
import {loadFrontend} from './acceptanceHarness.mjs'
const settle=async()=>{await vue.nextTick();await new Promise(resolve=>setImmediate(resolve))}
function harness(post){
 const requests=[],events=[],unmounts=[],storage={project:'project'}
 const auth=vue.reactive({authorizationState:'ready',user:{id:'actor',project_id:'project',permissions:['workflow.update']},canAny(codes){return codes.some(code=>this.user.permissions.includes(code))}})
 const props=vue.reactive({modelValue:'printf "%s" "${IMAGE_TAG}"',disabled:false})
 const component=loadFrontend('../src/components/DeploymentScriptEditor.vue',{
  vue:{...vue,onMounted(){},onBeforeUnmount:fn=>unmounts.push(fn)},'@codemirror/state':state,'@/stores/auth':{useAuthStore:()=>auth},'@/api/resources':{apiErrorMessage:()=> '检查失败'},'@/api/client':{__esModule:true,default:{post:async(path,body,options)=>{requests.push({path,body,options});return post?post():{data:{issues:[]}}}}}
 },{localStorage:{getItem:()=>storage.project}}).default
 const scope=vue.effectScope(),value=scope.run(()=>component.setup(props,{expose(){},emit:(name,text)=>{events.push({name,text});props.modelValue=text}}))
 return {value,props,auth,storage,requests,events,stop(){unmounts.forEach(fn=>fn());scope.stop()}}
}
test('部署脚本通过正常权限接口检查原文，结果展示语法范围',async t=>{
 const h=harness();t.after(h.stop);await h.value.check()
 assert.equal(h.requests[0].path,'/workflows/deployment-script/check')
 assert.equal(h.requests[0].body.script,'printf "%s" "${IMAGE_TAG}"')
 assert.equal(h.requests[0].options.headers['X-EDO-Project-ID'],'project')
 assert.match(h.value.status.value,/POSIX sh/)
})
test('上传脚本保留 UTF-8 原文，不执行上传内容，超限和撤权不写入',async t=>{
 const h=harness();t.after(h.stop)
 const text='#!/bin/sh\nprintf "%s" "${IMAGE_TAG}"  \n'
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
 assert.deepEqual(h.value.issues.value,[]);assert.equal(h.value.status.value,'')
 h.auth.user.permissions=[];await settle();await h.value.check();assert.equal(h.requests.length,1)
})
