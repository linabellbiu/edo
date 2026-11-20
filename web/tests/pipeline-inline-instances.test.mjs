import {test} from 'node:test'
import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import ts from 'typescript'
import {v5 as uuidv5} from 'uuid'
import {applicationDeploymentLocations,deploymentHostNodeID,kubernetesInstancePlan,selectCurrentDeploymentInstances,selectLatestFailedDeploymentAttempts,workflowDeploymentIDs} from '../src/utils/deploymentInstances.ts'
const source=readFileSync(new URL('../src/views/DevOpsView.vue',import.meta.url),'utf8')
const code=source.slice(source.indexOf('function applicationInstanceScope('),source.indexOf('\nconst applicationCards='))
const transpiled=ts.transpileModule(code,{compilerOptions:{target:ts.ScriptTarget.ES2022,module:ts.ModuleKind.CommonJS}}).outputText
const getScope=new Function('deploymentHostNodeID','workflowDeploymentIDs','kubernetesInstancePlan','deploymentPlans','records','app',transpiled+';return applicationInstanceScope(app,records)')
const scope=(app,records,plans=[])=>getScope(deploymentHostNodeID,workflowDeploymentIDs,kubernetesInstancePlan,{value:plans},records,app)
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

const kubeTarget={platform:'kubernetes',runtime_id:'cluster-a',namespace:'test',workload_name:'api',container_name:'app'}
const kubePlan={id:'kube-template',kind:'kubernetes'}
const kubeApp=(override,taskID=nodeID)=>({...app,workflows:[{id:workflowID,stages:[{tasks:[{id:taskID,type:'deploy',config:{deployment_plan_id:kubePlan.id,deployment_override:override}}]}]}]})
const kubeRecord={...record,...kubeTarget,host_id:'',environment_id:'',pipeline_run_id:'run',deployment_plan_kind:'kubernetes'}

test('多条流水线共用一个待部署位置时，保留每个来源且只展示一个位置',()=>{
 const plan={...kubePlan,deployment_target:{...kubeTarget,id:'fixed'}}
 const bindings=['主线发布','备用发布'].map(workflowName=>({...plan,workflowName,taskName:'部署'}))
 const view=applicationDeploymentLocations(app.id,[],new Set(),[...bindings,bindings[0]],[],new Set())
 assert.equal(view.waiting.length,1)
 assert.deepEqual(view.waiting[0].sources,['主线发布 / 部署','备用发布 / 部署'])
})

test('更换主机、库方案集群或任务内集群时，待部署、失败与成功使用同一规则，旧位置仅进历史',()=>{
 const targetB={...kubeTarget,runtime_id:'cluster-b'}
 const inline={...app,workflows:[{id:workflowID,stages:[{tasks:[{id:nodeID,type:'deploy',config:{inline_deployment_plan:{kind:'kubernetes',deployment_target:targetB}}}]}]}]}
 for(const [configured,library,a,b] of [
  [inline,[],kubeRecord,{...kubeRecord,...targetB}],
  [kubeApp({deployment_target:targetB}),[kubePlan],kubeRecord,{...kubeRecord,...targetB}],
  [hostApp(['host-b']),[],hostRecord('host-a'),hostRecord('host-b')],
 ]){
  const old={...a,id:'old',row_id:1}
  const failed={...b,id:'failed',row_id:2,status:'failed'}
  const good={...b,id:'succeeded',row_id:3}
  for(const [records,waitingStatus,currentID] of [[[old],'pending',null],[[old,failed],'failed',null],[[old,failed,good],null,good.id]]){
   const {plans,ids,unresolved}=scope(configured,records,library)
   const current=selectCurrentDeploymentInstances(app.id,records,new Set(),ids,plans)
   const view=applicationDeploymentLocations(app.id,records,new Set(),plans,current,unresolved)
   assert.deepEqual(current.map(row=>row.id),currentID?[currentID]:[])
   assert.deepEqual(view.history.map(row=>row.id),['old'])
   assert.deepEqual(view.waiting.map(row=>row.attempt?.status||'pending'),waitingStatus?[waitingStatus]:[])
   assert.deepEqual(selectLatestFailedDeploymentAttempts(app.id,records,new Set(),ids,plans).map(row=>row.id),waitingStatus==='failed'?['failed']:[])
  }
 }
})

test('通用 Kubernetes 方案按流水线任务解析部署实例和失败记录',()=>{
 const configured=kubeApp({plan_snapshot:{kind:'kubernetes'},deployment_target:kubeTarget})
 const success={...kubeRecord,id:'kube-success'}
 const older={...success,id:'kube-older',created_at:'2026-11-07T00:00:00Z'}
 const failed={...success,id:'kube-failed',status:'failed',created_at:'2026-11-09T00:00:00Z'}
 const records=[older,success,failed]
 const {plans,ids}=scope(configured,records,[kubePlan])
 assert.deepEqual(instances(configured,records,[kubePlan]).map(item=>item.id),['kube-success'])
 assert.deepEqual(selectLatestFailedDeploymentAttempts(app.id,records,new Set(),ids,plans).map(item=>item.id),['kube-failed'])
 assert.deepEqual(instances(kubeApp({deployment_target:kubeTarget},'deploy-other'),records,[kubePlan]),[])
 assert.deepEqual(instances(configured,records,[]),[])
 assert.deepEqual(instances(configured,[{...success,application_id:'other-app'}],[kubePlan]),[])
 for(const changes of [{runtime_id:'old-cluster'},{namespace:'old-namespace'},{workload_name:'old-workload'},{container_name:'old-container'}]){
  assert.deepEqual(instances(configured,[{...success,...changes}],[kubePlan]),[])
 }
 assert.deepEqual(instances(configured,[{...success,runtime_deleted_at:success.created_at},older],[kubePlan]),[])
})

test('Kubernetes 有固定目标时，参数覆盖沿用目标 ID，修改目标后使用本阶段 ID',()=>{
 const fixed={...kubePlan,deployment_target:{...kubeTarget,id:'fixed-target',environment_id:'env'}}
 const fixedRecord={...kubeRecord,environment_id:'env',target_id:'fixed-target'}
 const snapshot={kind:'kubernetes',deployment_target_id:'fixed-target',deployment_target:{...kubeTarget,environment_id:'env'}}
 const editedLibrary={...fixed,deployment_target:{...fixed.deployment_target,id:'edited',runtime_id:'another-cluster'}}
 assert.equal(instances(kubeApp({plan_snapshot:snapshot,kubernetes:{}}),[{...fixedRecord,deployment_plan_id:kubePlan.id}],[editedLibrary]).length,1)
 assert.equal(instances(kubeApp({plan_snapshot:snapshot,timeout_seconds:180}),[fixedRecord],[editedLibrary]).length,1)
 const moved={...kubeTarget,namespace:'new-test'}
 assert.deepEqual(instances(kubeApp({plan_snapshot:snapshot,deployment_target:moved}),[fixedRecord],[editedLibrary]),[])
 assert.equal(instances(kubeApp({plan_snapshot:snapshot,deployment_target:moved}),[{...fixedRecord,...moved,target_id:target}],[editedLibrary]).length,1)
})
