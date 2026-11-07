import assert from 'node:assert/strict'
import {test} from 'node:test'
import {loadFrontend} from './acceptanceHarness.mjs'
const {deploymentCommandPreview}=loadFrontend('../src/utils/deploymentCommandPreview.ts')

test('Compose 命令包含部署工作目录、项目、文件、等待参数，服务为空时部署整个项目',()=>{
 const plan={id:'plan',kind:'compose_stack',timeout_seconds:600,deployment_target:{working_directory:'/srv/my app'}}
 const config={compose:{project_name:'demo',env_file:'/srv/base.env',use_sudo:false,services:[]},deployment_parameters:'--env-file .prod.env --env-file config/override.env'}
 const preview=deploymentCommandPreview(plan,config,[])
 assert.equal(preview.command,"cd '/srv/my app' && docker compose --ansi never --project-name demo --project-directory '/srv/my app' --env-file /srv/base.env --env-file .prod.env --env-file config/override.env --file .edo/compose/demo/current.yml up -d --no-build --pull never --wait --wait-timeout 570")
 assert.equal(preview.equivalent,false)
 config.compose.services=['api','jobs'];config.compose.use_sudo=true
 const next=deploymentCommandPreview(plan,config,[])
 assert.match(next.command,/sudo -n docker compose/);assert.ok(next.command.endsWith(' api jobs'))
})

test('Docker 等效预览保留运行参数和文件引用，环境变量值脱敏',()=>{
 const plan={id:'plan',kind:'docker',deployment_target:{workload_name:'demo'},docker_config:{environment_variables:{TOKEN:'preview-secret-value'},port_mappings:[{host_ip:'::1',host_port:8080,container_port:80}],volume_mounts:[{source:'data',target:'/data',read_only:true}],health_check:{enabled:true,command:['curl','--fail','http://127.0.0.1/health'],interval_seconds:30,timeout_seconds:5,retries:3,start_period_seconds:10}}}
 const preview=deploymentCommandPreview(plan,{deployment_parameters:'--env-file config/extra.env'},['.prod.env'])
 assert.equal(preview.equivalent,true)
 assert.match(preview.command,/--name demo --network bridge --restart unless-stopped/)
 assert.match(preview.command,/--publish '\[::1\]:8080:80\/tcp'/)
 assert.match(preview.command,/--env 'TOKEN=<已隐藏>'/);assert.ok(!preview.command.includes('preview-secret-value'))
 assert.match(preview.command,/--env-file \.prod\.env --env-file config\/extra\.env/)
 assert.match(preview.command,/--health-interval 30s --health-timeout 5s --health-retries 3/)
 assert.match(preview.command,/<目标作用域卷:data>/)
 assert.ok(preview.command.endsWith('"${EDO_IMAGE}"'))
})

test('单服务 Compose 的等效预览实时包含额外参数，尚未确定的值标记为占位值',()=>{
 const plan={id:'plan',kind:'compose',service_name:'api',timeout_seconds:120}
 const preview=deploymentCommandPreview(plan,{deployment_parameters:'--env-file /srv/app/.prod.env'},[])
 assert.equal(preview.equivalent,true)
 assert.match(preview.command,/<运行时项目名>/)
 assert.match(preview.command,/--env-file \/srv\/app\/\.prod\.env --file - up /)
 assert.ok(preview.command.endsWith('--wait-timeout 120 api'))
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
