import assert from 'node:assert/strict'
import {test} from 'node:test'
import * as vue from 'vue'
import {componentHarness,loadFrontend} from './acceptanceHarness.mjs'
const helpers=loadFrontend('../src/utils/k3dProvision.ts')
const settle=async()=>{await vue.nextTick();await new Promise(resolve=>setImmediate(resolve));await vue.nextTick()}
const deferred=()=>{let resolve;const promise=new Promise(done=>resolve=done);return {promise,resolve}}
const domestic='swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/rancher/k3s'
const dockerhub='docker.io/rancher/k3s'
const catalogs={
  domestic:{source:'domestic',repository:domestic,versions:[{version:'v1.37.1+k3s1',tag:'v1.37.1-k3s1',image:`${domestic}:v1.37.1-k3s1`,platforms:['linux/amd64']},{version:'v1.29.6+k3s2',tag:'v1.29.6-k3s2-linuxarm64',image:`${domestic}:v1.29.6-k3s2-linuxarm64`,platforms:['linux/arm64']}]},
  dockerhub:{source:'dockerhub',repository:dockerhub,versions:[{version:'v1.37.2+k3s1',tag:'v1.37.2-k3s1',image:`${dockerhub}:v1.37.2-k3s1`,platforms:['linux/amd64','linux/arm64']}]},
}
function harness({architecture='amd64',api}={}){
  const props=vue.reactive({value:helpers.defaultK3dConfig(),drafts:{},architecture,disabled:false,scope:'project-a'}),calls=[],unmount=[]
  const ctx=componentHarness('../src/components/K3dImageFields.vue',{props,modules:{vue:{...vue,onBeforeUnmount:fn=>unmount.push(fn)},'@/utils/k3dProvision':helpers,'@/api/k3s':{getK3dImages:async(source,signal)=>{calls.push({source,signal});return api?api(source,signal):structuredClone(catalogs[source])}}}})
  ctx.events.push=(...events)=>{for(const event of events){if(event.name==='update:value')props.value=event.args[0];if(event.name==='update:drafts')props.drafts=event.args[0]}return Array.prototype.push.apply(ctx.events,events)}
  return {...ctx,props,calls,stop(){unmount.forEach(fn=>fn());ctx.stop()}}
}

test('镜像表单默认国内并自动选实际匹配架构的首个版本，切换来源与自定义保留各自草稿',async()=>{
  const ctx=harness()
  try{
    await settle();assert.equal(ctx.props.value.image_source,'domestic');assert.equal(ctx.props.value.image,catalogs.domestic.versions[0].image);assert.equal(ctx.state.valid.value,true)
    assert.deepEqual(ctx.state.options.value.map(item=>item.value),[catalogs.domestic.versions[0].image])
    ctx.state.updateSource('dockerhub');await settle();assert.equal(ctx.props.value.image,catalogs.dockerhub.versions[0].image)
    ctx.state.updateSource('custom');await settle();assert.equal(ctx.props.value.image,'');assert.equal(ctx.state.valid.value,false)
    ctx.state.updateImage('registry.example.com:5000/team/k3s:v1.36.6-k3s1');await settle();assert.equal(ctx.state.valid.value,true)
    ctx.state.updateSource('domestic');await settle();assert.equal(ctx.props.value.image,catalogs.domestic.versions[0].image)
    ctx.state.updateSource('custom');await settle();assert.equal(ctx.props.value.image,'registry.example.com:5000/team/k3s:v1.36.6-k3s1')
    assert.equal(ctx.calls.some(item=>item.source==='custom'),false,'手动地址不触发后端任意地址读取')
  }finally{ctx.stop()}
})

test('ARM64只展示真实ARM64镜像，换主机不静默覆盖已有选择，无匹配平台时阻断',async()=>{
  const ctx=harness({architecture:'arm64'})
  try{
    await settle();assert.deepEqual(ctx.state.options.value.map(item=>item.value),[catalogs.domestic.versions[1].image])
    assert.equal(ctx.props.value.image,catalogs.domestic.versions[1].image)
    ctx.props.architecture='amd64';await settle();assert.equal(ctx.state.valid.value,false);assert.equal(ctx.props.value.image,catalogs.domestic.versions[1].image)
    ctx.props.architecture='riscv64';await settle();assert.deepEqual(ctx.state.options.value,[]);assert.equal(ctx.state.valid.value,false)
  }finally{ctx.stop()}
})

test('读取失败保留选择且可重试，旧来源和旧项目的延迟响应不能回写',async()=>{
  let failed=true
  const ctx=harness({api:async source=>{if(failed)throw new Error('版本读取失败，请重试');return structuredClone(catalogs[source])}})
  try{
    await settle();assert.match(ctx.state.error.value,/读取失败/);assert.equal(ctx.state.valid.value,false)
    failed=false;await ctx.state.load();await settle();assert.equal(ctx.state.valid.value,true)
    failed=true;await ctx.state.load();await settle();assert.equal(ctx.props.value.image,catalogs.domestic.versions[0].image);assert.equal(ctx.state.valid.value,false)
  }finally{ctx.stop()}
  const pending=deferred(),old=harness({api:source=>source==='domestic'?pending.promise:structuredClone(catalogs[source])})
  try{
    await settle();old.state.updateSource('dockerhub');await settle();assert.equal(old.calls[0].signal.aborted,true)
    pending.resolve(structuredClone(catalogs.domestic));await settle()
    assert.equal(old.state.catalog.value.source,'dockerhub');assert.equal(old.props.value.image,catalogs.dockerhub.versions[0].image)
    old.props.scope='project-b';await settle();assert.equal(old.calls.at(-1).source,'dockerhub')
    old.props.disabled=true;await settle();assert.equal(old.state.catalog.value,undefined);assert.equal(old.state.valid.value,false)
  }finally{old.stop()}
})

test('镜像输入支持明确标签和摘要，拒绝协议、凭据、无标签和命令字符',()=>{
  for(const image of ['rancher/k3s:v1.37.1-k3s1','registry.example.com:5000/team/k3s:v1.36.6-k3s1',`registry.example.com/k3s@sha256:${'a'.repeat(64)}`])assert.equal(helpers.validK3dImage(image),true,image)
  for(const image of ['',undefined,'rancher/k3s','https://registry.example.com/k3s:v1','user:secret@registry.example.com/k3s:v1','rancher/k3s:v1\nfalse','rancher/k3s:v1;false'])assert.equal(helpers.validK3dImage(image),false,image)
})
