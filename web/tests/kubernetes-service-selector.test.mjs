import assert from 'node:assert/strict'
import {test} from 'node:test'
import * as vue from 'vue'
import axios from 'axios'
import {loadFrontend} from './acceptanceHarness.mjs'

const clone=value=>JSON.parse(JSON.stringify(value))
const service=(selector={'qa-e02':'maple'},version='12')=>({
 apiVersion:'v1',kind:'Service',metadata:{name:'web',namespace:'team',uid:'service-uid',resourceVersion:version,labels:{app:'web'}},
 spec:{type:'NodePort',selector,clusterIP:'10.43.0.10',clusterIPs:['10.43.0.10'],ipFamilies:['IPv4'],externalTrafficPolicy:'Local',ports:[
  {name:'http',port:80,targetPort:'http-web',protocol:'TCP',nodePort:30080,appProtocol:'http'},
  {name:'dns',port:53,targetPort:5353,protocol:'UDP',nodePort:30053},
 ]},
})
function editor(original,overrides={}){
 const calls=[],dialogs=[],events=[],notifications=[],unmounts=[]
 const client={
  async get(path,options){calls.push({method:'get',path,options});return {data:{object:overrides.fresh?overrides.fresh(original):{...clone(original),metadata:{...original.metadata,resourceVersion:'13'}}}}},
  async post(path,data,options){calls.push({method:'post',path,data:clone(data),options});return {data:{valid:true}}},
  async request(options){calls.push({...options,method:options.method.toLowerCase(),path:options.url,data:clone(options.data)});return {data:{}}},
 }
 const api=loadFrontend('../src/api/kubernetes.ts',{axios,'./client':{__esModule:true,default:client},'./resources':{apiErrorMessage:()=> '安全错误'}})
 const workload=loadFrontend('../src/utils/kubernetesWorkloadForm.ts',{'@/api/kubernetes':api})
 const component=loadFrontend('../src/components/KubeResourceEditor.vue',{
  vue:{...vue,onMounted(){},onBeforeUnmount:fn=>unmounts.push(fn)},'vue-i18n':{useI18n:()=>({t:key=>key})},
  'ant-design-vue':{Modal:{confirm:options=>{const dialog={...options,update(){},destroy(){}};dialogs.push(dialog);return dialog}},message:{success:value=>notifications.push(value)}},
  '@/api/client':{__esModule:true,default:client},'@/api/kubernetes':api,'@/utils/kubernetesWorkloadForm':workload,
 },{window:{addEventListener(){},removeEventListener(){}}}).default
 const props=vue.reactive({open:true,cluster:'cluster-a',namespace:'team',type:api.kubeTypes.find(item=>item.resource==='services'),object:original?clone(original):undefined})
 const scope=vue.effectScope();let state,stopped=false
 scope.run(()=>{state=component.setup(props,{expose(){},emit:(...args)=>events.push(args)})})
 return {state,props,calls,dialogs,events,notifications,stop(){if(stopped)return;stopped=true;unmounts.forEach(fn=>fn());scope.stop()}}
}

test('Service 选择器键改名必须显式删除旧键，不把新旧条件一起保留',t=>{
 const original=service(),before=clone(original),h=editor(original);t.after(h.stop)
 h.state.form.selector=[{key:'app',value:'maple-web'}]
 assert.deepEqual(h.state.buildObject().spec.selector,{app:'maple-web','qa-e02':null})
 assert.deepEqual(original,before);assert.deepEqual(h.props.object,before);assert.deepEqual(h.calls,[])
})

test('Service 移除一个选择器只删除对应键，保留其他未改条件',t=>{
 const h=editor(service({app:'web',tier:'frontend'}));t.after(h.stop)
 h.state.form.selector=[{key:'app',value:'web'}]
 assert.deepEqual(h.state.buildObject().spec.selector,{app:'web',tier:null})
})

test('Service 清空选择器为每个旧键发 null，而不是不会删除数据的空对象',t=>{
 const h=editor(service({app:'web',tier:'frontend'}));t.after(h.stop);h.state.form.selector=[]
 assert.deepEqual(h.state.buildObject().spec.selector,{app:null,tier:null})
})

test('Service 未改选择器与端口保持原值，保留身份而不回写集群分配字段',t=>{
 const original=service({app:'web',tier:'frontend'}),h=editor(original);t.after(h.stop)
 const payload=h.state.buildObject()
 assert.deepEqual(payload.spec.selector,original.spec.selector);assert.deepEqual(payload.spec.ports,original.spec.ports)
 assert.equal(payload.metadata.uid,original.metadata.uid);assert.equal(payload.metadata.resourceVersion,original.metadata.resourceVersion)
 for(const key of ['clusterIP','clusterIPs','ipFamilies','externalTrafficPolicy'])assert.equal(Object.hasOwn(payload.spec,key),false,key)
 assert.deepEqual(h.props.object,original);assert.deepEqual(h.calls,[])
})

test('新建 Service 不带旧键删除标记；重复或空选择器键继续阻断',t=>{
 const h=editor();t.after(h.stop);h.state.form.name='new-service';h.state.form.selector=[{key:'app',value:'web'}]
 assert.deepEqual(h.state.buildObject().spec.selector,{app:'web'})
 assert.equal(Object.hasOwn(h.state.buildObject().metadata,'uid'),false)
 for(const selector of [[{key:'app',value:'web'},{key:' app ',value:'other'}],[{key:' ',value:'web'}]]){
  h.state.form.selector=selector;assert.throws(()=>h.state.buildObject(),/k8s.invalidKeys/)
 }
 assert.deepEqual(h.calls,[])
})

test('Service 重命名经真实表单确认后 fresh、validate 与 PATCH 使用相同 null 补丁和新版本',async t=>{
 const original=service(),h=editor(original);t.after(h.stop);h.state.form.selector=[{key:'app',value:'maple-web'}]
 await h.state.save();assert.equal(h.dialogs.length,1);assert.deepEqual(h.calls,[])
 await h.dialogs[0].onOk()
 assert.deepEqual(h.calls.map(item=>item.method),['get','post','patch'])
 const [read,validate,write]=h.calls
 assert.equal(read.options.params.namespace,'team');assert.ok(validate.path.endsWith('/validate'));assert.ok(write.path.endsWith('/services/web'))
 assert.deepEqual(validate.data.spec.selector,{app:'maple-web','qa-e02':null});assert.deepEqual(write.data,validate.data)
 assert.equal(write.data.metadata.uid,'service-uid');assert.equal(write.data.metadata.resourceVersion,'13')
 assert.deepEqual(write.data.spec.ports,original.spec.ports);assert.deepEqual(h.props.object,original)
 assert.deepEqual(h.events,[['update:open',false],['saved']]);assert.deepEqual(h.notifications,['k8s.saved'])
})

test('Service 保存前选择器已并发变化仍阻断，保留草稿且不校验或写入',async t=>{
 const h=editor(service(),{fresh:original=>({...clone(original),metadata:{...original.metadata,resourceVersion:'13'},spec:{...clone(original.spec),selector:{'qa-e02':'another'}}})});t.after(h.stop)
 h.state.form.selector=[{key:'app',value:'maple-web'}];await h.state.save()
 await assert.rejects(h.dialogs[0].onOk(),/k8s.resourceChanged/)
 assert.deepEqual(h.calls.map(item=>item.method),['get']);assert.deepEqual(h.state.form.selector,[{key:'app',value:'maple-web'}])
 assert.equal(h.state.error.value,'k8s.resourceChanged');assert.deepEqual(h.events,[]);assert.deepEqual(h.notifications,[])
})
