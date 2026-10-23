import {runFilterModule} from './pipelineRunTestHarness.mjs'
import {readFileSync} from 'node:fs'
import {parse, compileScript} from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'

export function loadFrontend(path, modules = {}, globals = {}) {
  const source = readFileSync(new URL(path, import.meta.url), 'utf8')
  const script = path.endsWith('.vue') ? compileScript(parse(source).descriptor, {id:'acceptance-test'}).content : source
  const output = ts.transpileModule(script, {compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022,esModuleInterop:true}}).outputText
  const module = {exports:{}}
  new Function('require','module','exports',...Object.keys(globals),output)(name => modules[name] ?? (name === '@/utils/pipelineRunFilters' ? runFilterModule : {}),module,module.exports,...Object.values(globals))
  return module.exports
}

export function componentHarness(path, {codes = [], superuser = false, query = {}, props = {}, api: customAPI = {}, modules = {}, globals = {}} = {}) {
  const requests=[], notifications=[], confirmations=[], replacements=[], locations=[], events=[]
  const user={id:'operator',department_id:'department-a',is_superuser:superuser,permissions:[...codes]}
  const auth={user,permissions:new Set(codes),canAny:required=>user.is_superuser||!required.length||required.some(code=>auth.permissions.has(code))}
  const route=vue.reactive({path:'/test',fullPath:'/test',query:{...query}})
  const dataFor = path => ({
    '/users':{users:[]},'/roles':{roles:[]},'/departments':{departments:[]},'/permissions':{permissions:[]},
    '/audit-logs':{audit_logs:[]},'/identity-providers':{providers:[]},'/projects':{projects:[]},
  })[path.split('?')[0]] || {}
  const client=Object.fromEntries(['get','post','put','patch','delete'].map(method=>[method,async(path,body,options)=>{
    requests.push({method,path,body:body===undefined?undefined:JSON.parse(JSON.stringify(body)),options})
    return customAPI[method] ? customAPI[method](path,body,options) : {data:dataFor(path)}
  }]))
  const scope=vue.effectScope()
  let state
  const component=loadFrontend(path,{
    vue:{...vue,onMounted(){},onBeforeUnmount(){},onUnmounted(){}},
    'vue-i18n':{useI18n:()=>({t:key=>key,locale:vue.ref('zh-CN')})},
    'vue-router':{useRoute:()=>route,useRouter:()=>({replace:value=>{replacements.push(value);return Promise.resolve()},push:value=>{replacements.push(value);return Promise.resolve()}})},
    'ant-design-vue':{Modal:{confirm:value=>confirmations.push(value)},message:Object.fromEntries(['error','warning','success','info'].map(kind=>[kind,value=>notifications.push({kind,value})]))},
    '@/stores/auth':{useAuthStore:()=>auth},
    '@/api/client':{__esModule:true,default:client},
    '@/api/resources':{apiErrorMessage:error=>error?.message||'操作失败'},
    ...modules,
  },{
    location:{origin:'https://edo.example',assign:path=>locations.push(path)},
    window:{dispatchEvent(){},setInterval(){return 1},clearInterval(){},location:{assign:path=>locations.push(path)}},
    ...globals,
  }).default
  scope.run(()=>{state=component.setup(props, {expose(){},emit:(name,...args)=>events.push({name,args})})})
  return {state,auth,route,requests,notifications,confirmations,replacements,locations,events,stop:()=>scope.stop()}
}
