import {test} from 'node:test'
import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import ts from 'typescript'
import {v5 as uuidv5} from 'uuid'
const source=readFileSync(new URL('../src/views/DevOpsView.vue',import.meta.url),'utf8')
const code=source.slice(source.indexOf('function applicationInstanceScope('),source.indexOf('\nfunction currentApplicationDeployments('))
const transpiled=ts.transpileModule(code,{compilerOptions:{target:ts.ScriptTarget.ES2022,module:ts.ModuleKind.CommonJS}}).outputText
const scope=(app,records)=>new Function('uuidv5','deploymentPlans','deployments',transpiled+';return applicationInstanceScope(arguments[3])')(uuidv5,{value:[]},{value:records},app)
const workflowID='workflow-a',nodeID='deploy-a',base=`edo.workflow.deployment:${workflowID}:${nodeID}`
const plan=uuidv5(base+':plan','6ba7b812-9dad-11d1-80b4-00c04fd430c8'),target=uuidv5(base+':target','6ba7b812-9dad-11d1-80b4-00c04fd430c8')
const app={id:'app-a',workflows:[{id:workflowID,stages:[{tasks:[{id:nodeID,type:'deploy',config:{inline_deployment_plan:{kind:'script',deployment_target:{environment_id:'edited-but-not-deployed'}}}}]}]}]}
const record={id:'deployment-a',application_id:'app-a',deployment_plan_id:plan,target_id:target,host_id:'host-a',environment_id:'deployed-environment',status:'succeeded',platform:'ssh',created_at:'2026-11-08T00:00:00Z'}
test('流水线内置部署无需库方案记录也能进入应用实例范围，未运行草稿不覆盖已部署位置',()=>{
 const result=scope(app,[record]);assert.ok(result.ids.has(plan));assert.equal(result.plans.length,1);assert.equal(result.plans[0].deployment_target.environment_id,'deployed-environment');assert.equal(result.plans[0].deployment_target.host_id,'host-a')
})
test('相同主机、计划或镜像不能把其他应用及未关联任务的容器并入当前应用',()=>{
 const result=scope(app,[{...record,application_id:'app-b'}]);assert.equal(result.plans.length,0)
 const unbound=scope({...app,workflows:[]},[record]);assert.equal(unbound.ids.size,0)
 const otherTask=scope({...app,workflows:[{...app.workflows[0],id:'workflow-b'}]},[record]);assert.ok(!otherTask.ids.has(plan));assert.equal(otherTask.plans.length,0)
})
