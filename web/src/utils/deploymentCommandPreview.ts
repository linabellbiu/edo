import type { WorkflowNodeConfig } from '@/types/pipeline'

export interface DeploymentCommandPlan {
  id: string
  kind: string
  compose_yaml?: string
  timeout_seconds?: number
  service_name?: string
  docker_config?: {
    deployment_script?: string
    network?: string
    restart_policy?: string
    port_mappings?: Array<{ host_ip?: string; host_port: number; container_port: number; protocol?: string }>
    environment_variables?: Record<string, string>
    volume_mounts?: Array<{ source: string; target: string; read_only?: boolean }>
    command?: string[]
    health_check?: { enabled: boolean; command: string[]; interval_seconds: number; timeout_seconds: number; retries: number; start_period_seconds: number }
  }
  deployment_target?: { environment_id?: string; working_directory?: string; workload_name?: string }
}

function quote(value: string) {
  return /^[A-Za-z0-9_./:@=-]+$/.test(value) ? value : `'${value.replaceAll("'", `'"'"'`)}'`
}

export function validConfigurationPath(value: string) {
  return !!value && value.length <= 512 && value.split('/').every(part => !!part && part !== '.' && part !== '..' && !part.startsWith('-') && !part.startsWith('.edo') && /^[A-Za-z0-9_.-]+$/.test(part))
}

function configurationArgument(value: string) {
  return `"\${EDO_APP_CONFIG_PATH:?缺少应用配置目录}/"'${value}'`
}

// 这里只识别产品支持的 --env-file 参数，不解释任意 Shell 语法；实际校验仍由后端完成。
function configurationParameterPaths(value: string): string[] | undefined {
  let remaining = value.trim()
  const paths: string[] = []
  while (remaining) {
    const match = /^--env-file(?:=|\s+)(?:'([^']*)'|"([^"]*)"|([^\s'"]+))(?:\s+|$)/.exec(remaining)
    if (!match) return undefined
    const path = (match[1] ?? match[2] ?? match[3] ?? '').replace(/^\$(?:\{EDO_APP_CONFIG_PATH\}|EDO_APP_CONFIG_PATH)\//, '')
    if (!validConfigurationPath(path)) return undefined
    paths.push(path)
    remaining = remaining.slice(match[0].length).trim()
  }
  return paths
}

function configurationParameters(value: string, selected: string[]) {
  const paths = configurationParameterPaths(value)
  if (!paths || paths.some(path => !selected.includes(path))) return undefined
  return paths.flatMap(path => ['--env-file', configurationArgument(path)]).join(' ')
}

// 复用已有参数预览的有限语法，只提前提示可准确定位的缺失文件；完整 Shell 校验仍由后端处理。
export function deploymentParametersError(config: WorkflowNodeConfig): string {
  const paths = configurationParameterPaths(config.deployment_parameters || '')
  if (!paths) return ''
  const selected = config.deployment_input_version === 1
    ? (config.deployment_inputs || []).filter(input => input.kind === 'configuration' && input.configuration_id).map(input => input.path)
    : (config.configuration_files || []).filter(file => file.id).map(file => file.path)
  const missing = paths.find(path => !selected.includes(path))
  return missing ? `请先在“部署前下载上游产出文件或制品到主机”中添加并生成配置文件“${missing}”，或删除对应的 --env-file 参数` : ''
}

// 仅生成展示文本，不读取配置正文，也不执行用户输入的命令。
export function deploymentCommandPreview(plan: DeploymentCommandPlan | undefined, config: WorkflowNodeConfig, envPaths: string[], applicationName?: string): { command: string; equivalent: boolean; warning?: string } | undefined {
  if (!plan) return undefined
  const rawParameters = config.deployment_parameters?.trim() || ''
  const selected = (config.configuration_files || []).map(file => file.path)
  const parameters = configurationParameters(rawParameters, selected)
  const warning = selected.some(path => !validConfigurationPath(path)) ? '配置文件只支持应用配置目录下的相对路径。' : parameters === undefined ? '额外参数只支持 --env-file，并且只能引用已选择的配置文件。' : undefined
  const prefix = selected.length || rawParameters ? `export EDO_APP_CONFIG_PATH="\${HOME:?无法取得目标用户目录}/edo/"'${(applicationName || '<应用名称>').replaceAll("'", `'"'"'`)}'"/config"\n` : ''
  const withConfiguration = (command: string, equivalent: boolean) => ({ command: prefix + command, equivalent, ...(warning ? { warning } : {}) })
  if (plan.kind === 'compose_stack') {
    const settings = config.compose
    if (!settings?.resolved_project_name || !plan.deployment_target?.working_directory) return undefined
    const directory = plan.deployment_target.working_directory
    const args = [settings.use_sudo ? 'sudo -n docker' : 'docker', 'compose', '--ansi never', '--project-name', quote(settings.resolved_project_name), '--project-directory', quote(directory)]
    if (parameters ?? rawParameters) args.push(parameters ?? rawParameters)
    args.push('--file', quote(`.edo/compose/${settings.resolved_project_name}/current.yml`), 'up -d --no-build --pull never --wait --wait-timeout', String((plan.timeout_seconds || 600) - 30))
    args.push(...(settings.services || []).map(quote))
    return withConfiguration(`cd ${quote(directory)} && ${args.join(' ')}`, false)
  }
  if (plan.kind === 'compose') {
    const args = ['docker compose --ansi never --project-name "<运行时项目名>" --project-directory "<运行时临时目录>"']
    if (parameters ?? rawParameters) args.push(parameters ?? rawParameters)
    args.push('--file - up --detach --no-build --pull never --wait --wait-timeout', String(plan.timeout_seconds || 600))
    if (plan.service_name) args.push(quote(plan.service_name))
    return withConfiguration(args.join(' '), true)
  }
  if (plan.kind !== 'docker') return undefined
  const docker = plan.docker_config || {}
  if (docker.deployment_script) return {
    command: docker.deployment_script, equivalent: true,
    warning: rawParameters || config.configuration_files?.length ? 'Docker 自定义命令不能混用配置文件参数，请使用结构化 Docker 部署方案。' : undefined,
  }
  const args = ['docker run --detach --name', plan.deployment_target?.workload_name ? quote(plan.deployment_target.workload_name) : '"${EDO_CONTAINER_NAME}"', '--network', quote(docker.network || 'bridge'), '--restart', quote(docker.restart_policy || 'unless-stopped')]
  args.push('--label edo.managed=true', '--label "edo.deployment.target.id=${EDO_DEPLOYMENT_TARGET_ID}"', '--label "edo.deployment.id=${EDO_DEPLOYMENT_ID}"')
  for (const port of docker.port_mappings || []) {
    const address = port.host_ip || '127.0.0.1'
    args.push('--publish', quote(`${address.includes(':') ? `[${address}]` : address}:${port.host_port}:${port.container_port}/${port.protocol || 'tcp'}`))
  }
  for (const name of Object.keys(docker.environment_variables || {}).sort()) args.push('--env', quote(`${name}=<已隐藏>`))
  for (const volume of docker.volume_mounts || []) args.push('--mount', quote(`type=volume,source=<目标作用域卷:${volume.source}>,target=${volume.target}${volume.read_only ? ',readonly' : ''}`))
  // ENV 自动注入以及额外参数在后端合并为容器环境，预览使用等效的文件参数。
  for (const name of envPaths.filter(validConfigurationPath)) args.push('--env-file', configurationArgument(name))
  if (parameters ?? rawParameters) args.push(parameters ?? rawParameters)
  if (docker.health_check?.enabled) {
    const check = docker.health_check
    args.push('--health-cmd', quote(check.command.map(quote).join(' ')), '--health-interval', `${check.interval_seconds}s`, '--health-timeout', `${check.timeout_seconds}s`, '--health-retries', String(check.retries), '--health-start-period', `${check.start_period_seconds}s`)
  }
  args.push('"${EDO_IMAGE}"', ...(docker.command || []).map(quote))
  return withConfiguration(args.join(' '), true)
}
