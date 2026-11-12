import assert from 'node:assert/strict'
import {test} from 'node:test'
import {spawnSync} from 'node:child_process'
import {loadFrontend} from './acceptanceHarness.mjs'
const {deploymentCommandPreview,deploymentParametersError}=loadFrontend('../src/utils/deploymentCommandPreview.ts')
const references=(...paths)=>paths.map((path,index)=>({id:`config-${index}`,path}))
const argument=path=>'"${EDO_APP_CONFIG_PATH:?缺少应用配置目录}/"\''+path+'\''

test('Compose 命令包含应用配置路径、项目、文件和等待参数，旧服务器 ENV 字段不进入新命令',()=>{
 const plan={id:'plan',kind:'compose_stack',timeout_seconds:600,deployment_target:{working_directory:'/srv/my app'}}
 const config={configuration_files:references('.prod.env','config/override.env'),compose:{resolved_project_name:'demo',env_file:'/srv/base.env',use_sudo:false,services:[]},deployment_parameters:'--env-file .prod.env --env-file config/override.env'}
 const preview=deploymentCommandPreview(plan,config,[],'订单应用')
 assert.equal(preview.command,'export EDO_APP_CONFIG_PATH="${HOME:?无法取得目标用户目录}/edo/"\'订单应用\'"/config"\n'+"cd '/srv/my app' && docker compose --ansi never --project-name demo --project-directory '/srv/my app' --env-file "+argument('.prod.env')+' --env-file '+argument('config/override.env')+' --file .edo/compose/demo/current.yml up -d --no-build --pull never --wait --wait-timeout 570')
 assert.ok(!preview.command.includes('/srv/base.env'))
 assert.equal(preview.equivalent,false);assert.equal(preview.warning,undefined)
 assert.equal(spawnSync('sh',['-n'],{input:preview.command}).status,0)
 config.compose.services=['api','jobs'];config.compose.use_sudo=true
 const next=deploymentCommandPreview(plan,config,[],'订单应用')
 assert.match(next.command,/sudo -n docker compose/);assert.ok(next.command.endsWith(' api jobs'))
})

test('Docker 等效预览保留运行参数和应用配置引用，环境变量值脱敏',()=>{
 const plan={id:'plan',kind:'docker',deployment_target:{workload_name:'demo'},docker_config:{environment_variables:{TOKEN:'preview-secret-value'},port_mappings:[{host_ip:'::1',host_port:8080,container_port:80}],volume_mounts:[{source:'data',target:'/data',read_only:true}],health_check:{enabled:true,command:['curl','--fail','http://127.0.0.1/health'],interval_seconds:30,timeout_seconds:5,retries:3,start_period_seconds:10}}}
 const preview=deploymentCommandPreview(plan,{configuration_files:references('.prod.env','config/extra.env'),deployment_parameters:'--env-file config/extra.env'},['.prod.env'])
 assert.equal(preview.equivalent,true);assert.equal(preview.warning,undefined)
 assert.match(preview.command,/--name demo --network bridge --restart unless-stopped/)
 assert.match(preview.command,/--publish '\[::1\]:8080:80\/tcp'/)
 assert.match(preview.command,/--env 'TOKEN=<已隐藏>'/);assert.ok(!preview.command.includes('preview-secret-value'))
 assert.ok(preview.command.includes('--env-file '+argument('.prod.env')+' --env-file '+argument('config/extra.env')))
 assert.match(preview.command,/--health-interval 30s --health-timeout 5s --health-retries 3/)
 assert.match(preview.command,/<目标作用域卷:data>/)
 assert.ok(preview.command.endsWith('"${EDO_IMAGE}"'))
})

test('单服务 Compose 参数支持内置配置路径，未知路径和变量明确标记为无效',()=>{
 const plan={id:'plan',kind:'compose',service_name:'api',timeout_seconds:120}
 for(const parameters of ['--env-file .prod.env','--env-file $EDO_APP_CONFIG_PATH/.prod.env','--env-file "${EDO_APP_CONFIG_PATH}/.prod.env"']){
  const preview=deploymentCommandPreview(plan,{configuration_files:references('.prod.env'),deployment_parameters:parameters},[])
  assert.equal(preview.equivalent,true);assert.equal(preview.warning,undefined)
  assert.match(preview.command,/<运行时项目名>/)
  assert.ok(preview.command.includes('--env-file '+argument('.prod.env')+' --file - up '))
  assert.ok(preview.command.endsWith('--wait-timeout 120 api'))
 }
 for(const parameters of ['--env-file /srv/app/.prod.env','--env-file ${EDO_APP_CONFIG_PATH:-/etc}/passwd','--env-file $(id)']){
  assert.match(deploymentCommandPreview(plan,{configuration_files:references('.prod.env'),deployment_parameters:parameters},[]).warning,/只支持 --env-file/)
 }
})

test('不选配置时保留原命令，不生成配置目录或读取旧服务器 ENV',()=>{
 const plan={id:'plan',kind:'compose_stack',deployment_target:{working_directory:'/srv/app'}}
 const preview=deploymentCommandPreview(plan,{compose:{resolved_project_name:'demo',env_file:'/srv/old.env'}},[])
 assert.ok(!preview.command.includes('EDO_APP_CONFIG_PATH'));assert.ok(!preview.command.includes('--env-file'))
 assert.ok(preview.command.endsWith('--wait-timeout 570'))
})

test('未选择或尚未完成方案配置时不伪造命令',()=>{
 assert.equal(deploymentCommandPreview(undefined,{},[]),undefined)
 assert.equal(deploymentCommandPreview({id:'plan',kind:'compose_stack'},{},[]),undefined)
 assert.equal(deploymentCommandPreview({id:'plan',kind:'kubernetes'},{},[]),undefined)
})
test('自定义 Docker 命令不伪装成支持后端已禁止的配置文件参数',()=>{
 const command='docker run --name demo ${EDO_IMAGE}'
 const preview=deploymentCommandPreview({id:'plan',kind:'docker',docker_config:{deployment_script:command}},{deployment_parameters:'--env-file .prod.env'},[])
 assert.equal(preview.command,command)
 assert.match(preview.warning,/不能混用配置文件参数/)
})


test('仓库相对路径不要求选择配置，显式配置目录和同名配置简写仍保留',()=>{
 const plan={id:'plan',kind:'compose_stack',deployment_target:{working_directory:'/srv/app'}}
 const config={deployment_input_version:1,deployment_inputs:[],deployment_parameters:'--env-file .env',compose:{resolved_project_name:'demo'}}
 assert.equal(deploymentParametersError(config),'')
 let preview=deploymentCommandPreview(plan,config,[])
 assert.equal(preview.warning,undefined)
 assert.match(preview.command,/--env-file \.env --file/)
 assert.ok(!preview.command.includes('EDO_APP_CONFIG_PATH'))
 config.deployment_parameters='--env-file "${EDO_APP_CONFIG_PATH}/settings.env"'
 preview=deploymentCommandPreview(plan,config,[])
 assert.ok(preview.command.includes('--env-file '+argument('settings.env')))
 config.deployment_parameters='--env-file .env';config.configuration_files=[{id:'stale',path:'.env'}]
 assert.match(deploymentCommandPreview(plan,config,[]).command,/--env-file \.env --file/)
 config.deployment_inputs=[{kind:'configuration',configuration_id:'config',path:'.env'}]
 preview=deploymentCommandPreview(plan,config,[])
 assert.ok(preview.command.includes('--env-file '+argument('.env')))
 config.deployment_parameters='--env-file ../secret'
 assert.match(deploymentParametersError(config),/路径无效/)
 config.deployment_parameters=''
 assert.equal(deploymentParametersError(config),'')
})
