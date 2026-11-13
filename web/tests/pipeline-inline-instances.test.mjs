import {test} from 'node:test'
import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import ts from 'typescript'
import {v5 as uuidv5} from 'uuid'
import {selectCurrentDeploymentInstances} from '../src/utils/deploymentInstances.ts'
const source=readFileSync(new URL('../src/views/DevOpsView.vue',import.meta.url),'utf8')
const code=source.slice(source.indexOf('function applicationInstanceScope('),source.indexOf('\nfunction currentApplicationDeployments('))
const transpiled=ts.transpileModule(code,{compilerOptions:{target:ts.ScriptTarget.ES2022,module:ts.ModuleKind.CommonJS}}).outputText
const scope=(app,records,plans=[])=>new Function('uuidv5','deploymentPlans','deployments',transpiled+';return applicationInstanceScope(arguments[3])')(uuidv5,{value:plans},{value:records},app)
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

const namespace='6ba7b812-9dad-11d1-80b4-00c04fd430c8'
const hostNode=(node,host)=>uuidv5(`edo.deployment.host:${node}:${host}`,namespace)
const hostRecord=host=>{
 const base=`edo.workflow.deployment:${workflowID}:${hostNode(nodeID,host)}`
 return {...record,id:`deployment-${host}`,pipeline_run_id:'run',host_id:host,deployment_plan_id:uuidv5(base+':plan',namespace),target_id:uuidv5(base+':target',namespace),deployment_plan_kind:'compose_stack'}
}
const hostApp=hosts=>({...app,workflows:[{id:workflowID,stages:[{tasks:[{id:nodeID,type:'deploy',config:{inline_deployment_plan:{kind:'compose_stack',deployment_target:{platform:'ssh',host_ids:hosts}}}}]}]}]})
const instances=(app,records,plans=[])=>{
 const result=scope(app,records,plans)
 return selectCurrentDeploymentInstances(app.id,records,new Set(),result.ids,result.plans)
}

test('自定义方案按勾选主机展开身份，单台与多台主机均能显示成功部署实例',()=>{
 for(const hosts of [['host-a'],['host-a','host-b']]){
  const records=hosts.map(hostRecord)
  assert.deepEqual(new Set(instances(hostApp(hosts),records).map(item=>item.id)),new Set(records.map(item=>item.id)))
 }
})

test('主机勾选顺序或分组变化不影响实例，取消主机后不显示该主机历史实例',()=>{
 const records=['host-a','host-b'].map(hostRecord)
 assert.equal(instances(hostApp(['host-b','host-a']),records).length,2)
 assert.deepEqual(instances(hostApp(['host-b']),records).map(item=>item.id),['deployment-host-b'])
 assert.deepEqual(instances(hostApp([]),records),[])
 assert.deepEqual(instances(hostApp(['host-c']),records),[])
})

test('自定义方案不接管身份不符、其他应用和没有成功记录的容器',()=>{
 const configured=hostApp(['host-a'])
 for(const changes of [{host_id:'host-b'},{application_id:'app-b'},{status:'failed'},{target_id:'other-target'},{deployment_plan_id:'other-plan'}]){
  assert.deepEqual(instances(configured,[{...hostRecord('host-a'),...changes}]),[])
 }
})

test('库方案每台主机使用独立目标，共享方案 ID 不能丢掉其他主机',()=>{
 const libraryTarget={id:'library-target',platform:'docker',environment_id:'env',host_ids:['host-a','host-b'],runtime_id:''}
 const library={id:'library-plan',kind:'docker',deployment_target:libraryTarget}
 const configured={...app,workflows:[{id:workflowID,stages:[{tasks:[{id:nodeID,type:'deploy',config:{deployment_plan_id:library.id}}]}]}]}
 const records=['host-a','host-b'].map(host=>({...record,id:`library-${host}`,pipeline_run_id:'run',platform:'docker',runtime_id:`runtime-${host}`,environment_id:'env',host_id:host,deployment_plan_id:library.id,target_id:hostNode(libraryTarget.id,host)}))
 assert.deepEqual(new Set(instances(configured,records,[library]).map(item=>item.id)),new Set(['library-host-a','library-host-b']))
 const single={...library,deployment_target:{...libraryTarget,host_ids:['host-b']}}
 assert.deepEqual(instances(configured,records,[single]).map(item=>item.id),['library-host-b'])
 assert.deepEqual(instances(configured,records,[{...library,deployment_target:{...libraryTarget,host_ids:[]}}]),[])
 assert.deepEqual(instances(configured,records,[]),[])
 assert.deepEqual(instances(configured,records,[{...library,deployment_target:{...libraryTarget,environment_id:'changed'}}]),[])
})
