<script setup>
import {computed,ref} from 'vue'
import {theme} from 'ant-design-vue'
import client from '/src/api/client'
import {kubeTypes,resourceURL} from '/src/api/kubernetes'
import KubeResourceEditor from '/src/components/KubeResourceEditor.vue'

const query=new URLSearchParams(location.search),dark=query.get('theme')==='dark'
document.documentElement.dataset.theme=dark?'dark':'light'
const metadata=(name,uid)=>({name,uid,namespace:'preview',resourceVersion:'12',labels:{app:'demo-web'}})
const objects=new Map([
 ['configmaps',{apiVersion:'v1',kind:'ConfigMap',metadata:metadata('demo-settings','focus-config'),data:{setting:'FIRST'}}],
 ['services',{apiVersion:'v1',kind:'Service',metadata:metadata('demo-service','focus-service'),spec:{type:'ClusterIP',selector:{app:'demo-web'},ports:[{port:8080,targetPort:8080}]}}],
 ['deployments',{
  apiVersion:'apps/v1',kind:'Deployment',metadata:metadata('demo-web','focus-workload'),
  spec:{replicas:1,selector:{matchLabels:{app:'demo-web'}},template:{
   metadata:{labels:{app:'demo-web'}},
   spec:{containers:[{name:'web',image:'example.invalid/demo:1',resources:{requests:{cpu:'25m',memory:'16Mi'},limits:{cpu:'100m',memory:'64Mi'}}}]},
  }},
 }],
])
const open=ref(false),resource=ref('configmaps'),snapshot=ref(),saved=ref(0),resourceType=computed(()=>kubeTypes.find(item=>item.resource===resource.value))
function edit(value){resource.value=value;snapshot.value=structuredClone(objects.get(value));open.value=true}
// 本夹具只用内存对象响应已登记操作，任何请求都不会交给真实后端。
client.defaults.adapter=async config=>{
 config.signal?.throwIfAborted()
 const expected=resourceURL('focus-preview',resourceType.value,objects.get(resource.value).metadata.name)
 if(![expected,`${expected}/validate`].includes(config.url))throw new Error('本地夹具未登记此请求')
 if(query.get('scenario')==='slow')await new Promise(resolve=>setTimeout(resolve,1500))
 config.signal?.throwIfAborted()
 let data
 if(config.method==='get'&&config.url===expected)data={object:structuredClone(objects.get(resource.value))}
 else if(config.method==='post'&&config.url===`${expected}/validate`)data={}
 else if(config.method==='patch'&&config.url===expected){
  const patch=typeof config.data==='string'?JSON.parse(config.data):config.data,current=objects.get(resource.value)
  const updated={...current,...patch,metadata:{...current.metadata,...patch.metadata,resourceVersion:String(Number(current.metadata.resourceVersion)+1)}}
  objects.set(resource.value,updated);data={object:structuredClone(updated)}
 }else throw new Error('本地夹具未登记此操作')
 return {data,status:200,statusText:'OK',headers:{},config}
}
</script>

<template>
  <a-config-provider :theme="{algorithm:dark?theme.darkAlgorithm:theme.defaultAlgorithm,token:{colorPrimary:'#5269ff',borderRadius:6,fontSize:14}}">
    <main class="editor-focus-preview">
      <h1>EDO Kubernetes 编辑器</h1><p>本地交互夹具 · 内存数据 · 不连接集群</p>
      <div class="fixture-actions"><a-button @click="edit('configmaps')">编辑配置项</a-button><a-button @click="edit('deployments')">编辑工作负载</a-button><a-button @click="edit('services')">编辑服务</a-button></div>
      <table><thead><tr><th>名称</th><th>状态</th><th>操作</th></tr></thead><tbody><tr v-for="n in 8" :key="n"><td>demo-resource-{{ n }}</td><td>就绪</td><td><button type="button" @click="edit('configmaps')">编辑 demo-resource-{{ n }}</button></td></tr></tbody></table>
      <div class="fixture-pagination"><button type="button">上一页</button><button type="button">下一页</button><label>每页条数 <select aria-label="每页条数"><option>10 条/页</option><option>20 条/页</option></select></label></div>
      <output aria-live="polite">内存保存次数：{{ saved }}</output>
      <KubeResourceEditor :open="open" cluster="focus-preview" namespace="preview" :type="resourceType" :object="snapshot" @update:open="open=$event" @saved="saved++" />
    </main>
  </a-config-provider>
</template>

<style scoped>
.editor-focus-preview{min-height:120dvh;padding:24px;background:var(--edo-bg);color:var(--edo-text)}h1{font-size:20px;margin:0 0 8px}p,output{color:var(--edo-muted);font-size:13px}.fixture-actions,.fixture-pagination{display:flex;align-items:center;flex-wrap:wrap;gap:12px;margin:20px 0}table{width:100%;border-collapse:collapse;background:var(--edo-surface)}th,td{text-align:left;padding:16px;border:1px solid var(--edo-border)}th{background:var(--edo-surface-soft)}button,select{font:inherit}button:focus-visible,select:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}
</style>
