import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {test} from 'node:test'
import {parse,compileScript,compileTemplate,compileStyle} from '@vue/compiler-sfc'
import {effect,stop} from 'vue'
import {selectTraceOptions,selectTraceValue,selectTraceLedger} from './fixtures/kubernetes-select-trace-data.mjs'

test('公开事件夹具仅投影固定合成值，不保存搜索、未知按键或对象内容',()=>{
 const ledger=selectTraceLedger(),secret='SYNTHETIC_DO_NOT_KEEP'
 ledger.append('wrapped','select',{value:secret,token:secret})
 ledger.append('controlled','search',secret)
 ledger.append('native','inputKeyDown',{key:secret,defaultPrevented:false,cancelBubble:true})
 ledger.append('native','change',['NodePort',secret])
 const trace=ledger.sample().entries
 assert.doesNotMatch(JSON.stringify(trace),new RegExp(secret))
 assert.deepEqual(trace.at(-1).value,['NodePort','<其他>']);assert.deepEqual({key:trace[2].key,prevented:trace[2].prevented,stopped:trace[2].stopped},{key:'<其他>',prevented:false,stopped:true})
 assert.deepEqual(selectTraceOptions().map(option=>option.value),['ClusterIP','NodePort','LoadBalancer'])
 assert.equal(selectTraceValue(undefined),null);assert.equal(selectTraceValue(8080),'<其他>')
})

test('公开事件有界顺序、清空和允许名单固定，不伪造内部展开状态',()=>{
 const ledger=selectTraceLedger()
 for(let n=0;n<90;n++)ledger.append('controlled','dropdownVisibleChange',n%2===0)
 const trace=ledger.sample().entries
 assert.equal(trace.length,80);assert.equal(trace[0].sequence,11);assert.equal(trace.at(-1).sequence,90)
 ledger.clear();assert.deepEqual(ledger.sample().entries,[]);ledger.append('modal','open',true);assert.equal(ledger.sample().entries[0].sequence,1)
 assert.throws(()=>ledger.append('unknown','focus'),/未登记/);assert.throws(()=>ledger.append('wrapped','privateState'),/未登记/)
})

test('采集和读取快照不触发Vue响应依赖或再次投递回调',()=>{
 const ledger=selectTraceLedger();let renders=0,delivered=0
 const runner=effect(()=>{renders++;JSON.stringify(ledger.sample())})
 const event={key:'Escape',keyCode:27,type:'keydown',isTrusted:true,constructor:{name:'KeyboardEvent'}}
 const callback=payload=>{delivered++;ledger.append('controlled','inputKeyDown',payload)}
 ledger.append('controlled','domKeyDown',event);callback(event)
 for(let n=0;n<1000;n++)JSON.stringify(ledger.sample())
 assert.equal(renders,1);assert.equal(delivered,1)
 assert.equal(ledger.sample().counts.controlled.domKeyDown,1)
 assert.equal(ledger.sample().counts.controlled.inputKeyDown,1)
 assert.equal(ledger.sample().sequence,2)
 stop(runner)
})

test('键盘公开元数据使用固定枚举，缺失和未知事件可区分且不保留原对象',()=>{
 const ledger=selectTraceLedger(),secret='SYNTHETIC_TRACE_PRIVATE_VALUE'
 const event={key:'Escape',keyCode:27,type:'keydown',isTrusted:true,constructor:{name:'KeyboardEvent'},target:{value:secret}}
 ledger.append('controlled','domKeyDown',event)
 ledger.append('controlled','inputKeyDown',event)
 ledger.append('controlled','inputKeyDown',undefined)
 ledger.append('controlled','inputKeyDown',{key:secret,keyCode:65,type:secret,isTrusted:secret,constructor:{name:secret}})
 const trace=ledger.sample().entries
 assert.deepEqual(trace[1],{sequence:2,target:'controlled',event:'inputKeyDown',key:'Escape',keyCode:27,type:'keydown',isTrusted:true,constructor:'KeyboardEvent',prevented:false,stopped:false})
 assert.equal(trace[2].key,null);assert.equal(trace[2].keyCode,null);assert.equal(trace[2].type,null);assert.equal(trace[2].constructor,'undefined')
 assert.equal(trace[3].key,'<其他>');assert.equal(trace[3].keyCode,'<其他>');assert.equal(trace[3].type,'<其他>');assert.equal(trace[3].constructor,'<其他>')
 assert.doesNotMatch(JSON.stringify(ledger.sample()),new RegExp(secret))
 event.key='Enter';assert.equal(ledger.sample().entries[1].key,'Escape')
})

test('事件getter重入或抛错不会递归采集，格式化快照不会再次读取原事件',()=>{
 const ledger=selectTraceLedger();let keyReads=0
 const event={get key(){keyReads++;ledger.append('controlled','inputKeyDown',{key:'Tab'});return 'Escape'},get type(){throw new Error('不得保留原始错误')},keyCode:27,isTrusted:false,constructor:{name:'KeyboardEvent'}}
 ledger.append('controlled','inputKeyDown',event)
 for(let n=0;n<100;n++)JSON.stringify(ledger.sample())
 assert.equal(keyReads,1);assert.equal(ledger.sample().sequence,1);assert.equal(ledger.sample().droppedReentrant,1)
 assert.equal(ledger.sample().entries[0].type,'<不可读取>')
 assert.doesNotMatch(JSON.stringify(ledger.sample()),/不得保留原始错误/)
 ledger.append('controlled','inputKeyDown',{get constructor(){throw new Error('不得记录构造器错误')}})
 assert.equal(ledger.sample().entries.at(-1).constructor,'<不可读取>')
 assert.doesNotMatch(JSON.stringify(ledger.sample()),/不得记录构造器错误/)
})

test('快照和固定多选值与采集器隔离，循环数组不导致格式化递归',()=>{
 const ledger=selectTraceLedger(),cyclic=[];cyclic.push(cyclic)
 ledger.append('wrapped','change',['NodePort','ClusterIP'])
 const sample=ledger.sample();sample.entries[0].value.push('LoadBalancer');sample.counts.wrapped.change=999;sample.reported.wrapped=true
 assert.deepEqual(ledger.sample().entries[0].value,['NodePort','ClusterIP']);assert.equal(ledger.sample().counts.wrapped.change,1);assert.equal(ledger.sample().reported.wrapped,null)
 assert.deepEqual(selectTraceValue(cyclic),['<其他>'])
})

test('三路对照只使用公开组件事件与自持 open，模板和局部样式可编译',()=>{
 const path=new URL('./fixtures/KubernetesSelectTracePreview.vue',import.meta.url),source=readFileSync(path,'utf8'),descriptor=parse(source).descriptor
 const script=compileScript(descriptor,{id:'select-trace'}),template=compileTemplate({source:descriptor.template.content,filename:path.pathname,id:'select-trace',compilerOptions:{bindingMetadata:script.bindings}})
 assert.deepEqual(template.errors,[])
 for(const style of descriptor.styles)assert.deepEqual(compileStyle({source:style.content,filename:path.pathname,id:'select-trace',scoped:style.scoped}).errors,[])
 assert.match(source,/:open="controlledOpen"/);assert.match(source,/<KubeEditorSelect :active="active"/)
 for(const event of ['dropdown-visible-change','input-key-down','select','change','focus','blur','search'])assert.equal((source.match(new RegExp(`@${event}=`,'g'))??[]).length,3,event)
 assert.doesNotMatch(source,/\.exposed|querySelector|document\.activeElement|client\.|fetch\(|localStorage|sessionStorage|defineExpose|window\./)
 assert.equal((source.match(/@keydown\.capture=/g)??[]).length,3)
 assert.match(source,/读取采样/);assert.match(source,/ledger\.sample\(\)/)
 assert.doesNotMatch(source,/trace\.value\s*=\s*ledger\.append|reported\[target\]\s*=/)
})
