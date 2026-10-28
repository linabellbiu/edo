import {readFileSync} from 'node:fs'
import {parse,compileScript} from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'

export function compile(path, modules={}) {
  const source=readFileSync(new URL(path,import.meta.url),'utf8')
  const code=path.endsWith('.vue')?compileScript(parse(source).descriptor,{id:'run-filters-test'}).content:source
  const output=ts.transpileModule(code,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022}}).outputText
  const module={exports:{}}
  new Function('require','module','exports',output)(name=>modules[name]||{},module,module.exports)
  return module.exports
}
export const runFilterModule=compile('../src/utils/pipelineRunFilters.ts')
export function runListHarness({records=[],query={},get,post,user={id:'reviewer',project_id:'project',permissions:['deployment.read','deployment.review']},canAny=()=>true}={}) {
  const route=vue.reactive({query:{view:'runs',...query}}), requests=[],posts=[],errors=[]
  const auth=vue.reactive({user,canAny})
  const update=async value=>{route.query=value.query}
  const client={async get(path,options={}){
    requests.push({path,...options})
    if(get)return get(path,options)
    if(path.startsWith('/pipeline-runs/'))return {data:{pipeline_run:records.find(row=>row.id===path.split('/').at(-1))}}
    const p=options.params||{}, rows=records.filter(row=>!p.status||row.status===p.status)
    const size=p.page_size||10,page=p.page||1
    return {data:{pipeline_runs:rows.slice((page-1)*size,page*size),total:rows.length}}
  },async post(path,body,options={}){posts.push({path,body,...options});return post?post(path,body,options):{data:{}}}}
  const component=compile('../src/views/DevOpsView.vue',{
    vue:{...vue,onMounted(){},onBeforeUnmount(){},watch(){}},
    'vue-router':{useRoute:()=>route,useRouter:()=>({push:update,replace:update})},
    'vue-i18n':{useI18n:()=>({t:key=>key,locale:vue.ref('zh-CN')})},
    'ant-design-vue':{Modal:{},message:{error:error=>errors.push(error)}},
    '@/stores/auth':{useAuthStore:()=>auth},
    '@/api/client':{default:client},
    '@/api/resources':{apiErrorMessage:error=>error.message},
    '@/utils/pipelineRunFilters':runFilterModule,
  }).default
  return {state:component.setup({section:'release-plans'},{expose(){}}),route,requests,posts,errors,auth}
}
