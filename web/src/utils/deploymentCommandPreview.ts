import type { WorkflowNodeConfig } from '@/types/pipeline'

export interface DeploymentCommandPlan {
  id: string
  kind: string
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
  deployment_target?: { working_directory?: string; workload_name?: string }
}

function quote(value: string) {
  return /^[A-Za-z0-9_./:@=-]+$/.test(value) ? value : `'${value.replaceAll("'", `'"'"'`)}'`
}

// 仅生成展示文本，不读取配置正文，也不执行用户输入的命令。
export function deploymentCommandPreview(plan: DeploymentCommandPlan | undefined, config: WorkflowNodeConfig, envPaths: string[]): { command: string; equivalent: boolean; warning?: string } | undefined {
  if (!plan) return undefined
  const parameters = config.deployment_parameters?.trim() || ''
  if (plan.kind === 'compose_stack') {
    const settings = config.compose
    if (!settings?.project_name || !plan.deployment_target?.working_directory) return undefined
    const directory = plan.deployment_target.working_directory
    const args = [settings.use_sudo ? 'sudo -n docker' : 'docker', 'compose', '--ansi never', '--project-name', quote(settings.project_name), '--project-directory', quote(directory)]
    if (settings.env_file) args.push('--env-file', quote(settings.env_file))
    if (parameters) args.push(parameters)
    args.push('--file', quote(`.edo/compose/${settings.project_name}/current.yml`), 'up -d --no-build --pull never --wait --wait-timeout', String((plan.timeout_seconds || 600) - 30))
    args.push(...(settings.services || []).map(quote))
    return { command: `cd ${quote(directory)} && ${args.join(' ')}`, equivalent: false }
  }
  if (plan.kind === 'compose') {
    const args = ['docker compose --ansi never --project-name "<运行时项目名>" --project-directory "<运行时临时目录>"']
    if (parameters) args.push(parameters)
    args.push('--file - up --detach --no-build --pull never --wait --wait-timeout', String(plan.timeout_seconds || 600))
    if (plan.service_name) args.push(quote(plan.service_name))
    return { command: args.join(' '), equivalent: true }
  }
  if (plan.kind !== 'docker') return undefined
  const docker = plan.docker_config || {}
  if (docker.deployment_script) return {
    command: docker.deployment_script, equivalent: true,
    warning: parameters || config.configuration_files?.length ? 'Docker 自定义命令不能混用配置文件参数，请使用结构化 Docker 部署方案。' : undefined,
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
  for (const name of envPaths) args.push('--env-file', quote(name))
  if (parameters) args.push(parameters)
  if (docker.health_check?.enabled) {
    const check = docker.health_check
    args.push('--health-cmd', quote(check.command.map(quote).join(' ')), '--health-interval', `${check.interval_seconds}s`, '--health-timeout', `${check.timeout_seconds}s`, '--health-retries', String(check.retries), '--health-start-period', `${check.start_period_seconds}s`)
  }
  args.push('"${EDO_IMAGE}"', ...(docker.command || []).map(quote))
  return { command: args.join(' '), equivalent: true }
}
