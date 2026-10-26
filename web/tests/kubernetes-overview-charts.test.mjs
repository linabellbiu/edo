import assert from 'node:assert/strict'
import {test} from 'node:test'
import * as vue from 'vue'
import {loadFrontend} from './acceptanceHarness.mjs'

const settle=async()=>{await vue.nextTick();await new Promise(resolve=>setImmediate(resolve));await vue.nextTick()}
const overview=()=>({
  counts:{nodes:{count:1,state:'ok'},pods:{count:3,state:'ok'}},
  nodes:[{name:'worker',cpu:6,memory:3*1024**3,ready:true}],
  usage:{nodes:{worker:{cpu:.12,memory:512*1024**2}}},
  phases:{Running:3},collected_at:new Date().toISOString(),
})

function chartHarness(){
  const hooks={mounted:[],unmounted:[]},instances=[],observers=[]
  const props=vue.reactive({data:null,loading:true,error:''})
  const preferences=vue.reactive({theme:'light'}),locale=vue.ref('zh-CN')
  const component=loadFrontend('../src/components/KubeOverview.vue',{
    vue:{...vue,onMounted:fn=>hooks.mounted.push(fn),onBeforeUnmount:fn=>hooks.unmounted.push(fn)},
    'vue-i18n':{useI18n:()=>({t:key=>key,locale})},
    '@/stores/preferences':{usePreferencesStore:()=>preferences},
    '@/api/kubernetes':{usageFor:(data,node)=>data?.usage.nodes[node.metadata.name],formatKubeCPU:String,formatKubeMemory:String},
    'echarts/core':{use(){},init:element=>{
      const chart={element,options:[],disposed:false,resizes:0,getDom:()=>element,
        setOption(option){assert.equal(this.disposed,false);this.options.push(option)},
        resize(){this.resizes++},dispose(){this.disposed=true},
      }
      instances.push(chart)
      return chart
    }},
  },{
    getComputedStyle:()=>({getPropertyValue:()=>preferences.theme==='dark'?'#283140':'#e5e7eb'}),
    ResizeObserver:class{
      constructor(callback){this.callback=callback;this.disconnected=false;observers.push(this)}
      observe(){}
      disconnect(){this.disconnected=true}
    },
  }).default
  const scope=vue.effectScope()
  const state=scope.run(()=>component.setup(props,{expose(){},emit(){}}))
  state.root.value=vue.markRaw({})
  hooks.mounted.forEach(fn=>fn())
  const names=['cpuChart','memoryChart','podChart']
  return {state,props,instances,observers,preferences,locale,
    attach(){return names.map(name=>state[name].value=vue.markRaw({clientWidth:112,clientHeight:112}))},
    detach(){names.forEach(name=>{state[name].value=null})},
    stop(){hooks.unmounted.forEach(fn=>fn());scope.stop()},
  }
}

test('总览数据先返回、图表容器后挂载时，三个圆环仍会绘制',async()=>{
  const ctx=chartHarness()
  try{
    ctx.props.data=overview();ctx.props.loading=false;await settle()
    assert.equal(ctx.instances.length,0)
    const elements=ctx.attach();await settle()
    assert.equal(ctx.instances.length,3)
    for(const element of elements)assert.ok(ctx.instances.some(chart=>chart.element===element&&chart.options.length>0))
  }finally{ctx.stop()}
})

test('总览重新加载并替换容器后，在新容器绘制圆环并释放旧画布',async()=>{
  const ctx=chartHarness()
  try{
    ctx.props.data=overview();ctx.props.loading=false;ctx.attach();await ctx.state.render();await settle()
    const original=[...ctx.instances]
    ctx.props.data=null;ctx.props.loading=true;ctx.detach();await settle()
    ctx.props.data=overview();ctx.props.loading=false;const elements=ctx.attach();await settle()
    assert.equal(original.every(chart=>chart.disposed),true)
    for(const element of elements)assert.ok(ctx.instances.some(chart=>chart.element===element&&!chart.disposed&&chart.options.length>0))
    assert.equal(ctx.instances.filter(chart=>!chart.disposed).length,3)
  }finally{ctx.stop()}
})

test('只替换图表容器也会重建；普通数据、主题和语言刷新复用当前画布',async()=>{
  const ctx=chartHarness()
  try{
    ctx.props.data=overview();ctx.props.loading=false;ctx.attach();await ctx.state.render();await settle()
    const original=[...ctx.instances],elements=ctx.attach();await settle()
    assert.equal(original.every(chart=>chart.disposed),true)
    const active=ctx.instances.filter(chart=>!chart.disposed),updates=active.map(chart=>chart.options.length)
    ctx.props.data.phases={Running:2,Failed:1};ctx.preferences.theme='dark';ctx.locale.value='en-US';await settle()
    assert.equal(ctx.instances.length,6)
    active.forEach((chart,index)=>{assert.equal(chart.element,elements[index]);assert.ok(chart.options.length>updates[index])})
  }finally{ctx.stop()}
})

test('隐藏的容器恢复尺寸后绘制；卸载时释放实例并停止后续绘制',async()=>{
  const ctx=chartHarness()
  try{
    ctx.props.data=overview();ctx.props.loading=false;const elements=ctx.attach()
    elements.forEach(element=>{element.clientWidth=0;element.clientHeight=0});await ctx.state.render();await settle()
    assert.equal(ctx.instances.length,0)
    elements.forEach(element=>{element.clientWidth=112;element.clientHeight=112})
    ctx.observers[0].callback();await settle();assert.equal(ctx.instances.length,3)
    ctx.stop();assert.equal(ctx.instances.every(chart=>chart.disposed),true)
    assert.equal(ctx.observers[0].disconnected,true)
    await ctx.state.render();assert.equal(ctx.instances.length,3)
  }finally{if(!ctx.observers[0].disconnected)ctx.stop()}
})
