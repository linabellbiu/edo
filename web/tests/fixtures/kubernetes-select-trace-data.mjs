const values=['ClusterIP','NodePort','LoadBalancer']
const targets=new Set(['native','controlled','wrapped','modal'])
const events=new Set(['open','cancel','close','dropdownVisibleChange','inputKeyDown','domKeyDown','select','change','update:value','focus','blur','search'])
const keys=new Set(['Escape','Enter','ArrowDown','ArrowUp','Tab','Backspace'])
const keyCodes=new Set([0,8,9,13,27,38,40])
const eventTypes=new Set(['keydown','keyup','focus','blur','mousedown','click','input','change','compositionstart','compositionend','paste'])
const constructors=new Set(['KeyboardEvent','Event','FocusEvent','MouseEvent','InputEvent','CompositionEvent','Object','Function','Array','Promise','Number','String','Boolean','Symbol','BigInt'])
const unreadable=Symbol('unreadable')

function read(payload,key){try{return payload===null||payload===undefined?undefined:payload[key]}catch{return unreadable}}
function enumText(value,allowed){return value===unreadable?'<不可读取>':value===null||value===undefined?null:typeof value==='string'&&allowed.has(value)?value:'<其他>'}
function keyboardMetadata(payload){
  const key=read(payload,'key'),keyCode=read(payload,'keyCode'),type=read(payload,'type'),trusted=read(payload,'isTrusted')
  const constructorValue=read(payload,'constructor')
  const constructor=payload===undefined?'undefined':payload===null?'null':constructorValue===unreadable?'<不可读取>':enumText(read(constructorValue,'name'),constructors)??'<其他>'
  return {key:enumText(key,keys),keyCode:keyCode===unreadable?'<不可读取>':keyCode===null||keyCode===undefined?null:keyCodes.has(keyCode)?keyCode:'<其他>',type:enumText(type,eventTypes),isTrusted:typeof trusted==='boolean'?trusted:null,constructor,prevented:read(payload,'defaultPrevented')===true,stopped:read(payload,'cancelBubble')===true}
}
function leafValue(value){return value===null||value===undefined?null:typeof value==='string'&&values.includes(value)?value:'<其他>'}
function freshCounts(){return Object.fromEntries([...targets].map(target=>[target,Object.fromEntries([...events].map(event=>[event,0]))]))}
function freshReported(){return {native:null,controlled:null,wrapped:null}}

export function selectTraceOptions(){return values.map(value=>({value,label:value}))}
export function selectTraceValue(value){
  return Array.isArray(value)?value.slice(0,8).map(leafValue):leafValue(value)
}
export function selectTraceLedger(){
  let sequence=0,entries=[],counts=freshCounts(),reported=freshReported(),recording=false,droppedReentrant=0
  return {
    append(target,event,payload){
      if(!targets.has(target)||!events.has(event))throw new Error('未登记的夹具事件')
      if(recording){droppedReentrant++;return}
      recording=true
      try{
        const entry={sequence:++sequence,target,event}
        if(event==='dropdownVisibleChange'||event==='open')entry.open=payload===true
        else if(event==='inputKeyDown'||event==='domKeyDown')Object.assign(entry,keyboardMetadata(payload))
        else if(['select','change','update:value'].includes(event))entry.value=selectTraceValue(payload)
        counts[target][event]++
        if(event==='dropdownVisibleChange'&&target in reported)reported[target]=payload===true
        entries.push(entry)
        if(entries.length>80)entries.shift()
      }finally{recording=false}
    },
    sample(){
      return {sequence,droppedReentrant,entries:entries.map(entry=>Array.isArray(entry.value)?{...entry,value:entry.value.slice()}:{...entry}),reported:{...reported},counts:Object.fromEntries([...targets].map(target=>[target,{...counts[target]}]))}
    },
    clear(){
      if(recording){droppedReentrant++;return}
      sequence=0;entries=[];counts=freshCounts();reported=freshReported();droppedReentrant=0
    },
  }
}
