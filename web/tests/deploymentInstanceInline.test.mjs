import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'

const view = readFileSync(new URL('../src/views/DevOpsView.vue', import.meta.url), 'utf8')
const css = readFileSync(new URL('../src/styles/application-workspace.css', import.meta.url), 'utf8')
const rollbackModal = readFileSync(new URL('../src/components/DeploymentRollbackModal.vue', import.meta.url), 'utf8')

const itemStart = view.indexOf('<div v-for="record in card.deploymentInstances"')
const itemEnd = view.indexOf('</Transition>', itemStart)
const item = view.slice(itemStart, itemEnd)
const detailsIndex = item.indexOf('<Transition name="deployment-details">')
const summaryEnd = item.indexOf('</component>')

test('K8s 部署实例在行内展示命名空间、工作负载、副本与镜像，不再依赖展开', () => {
  assert.ok(view.includes('function deploymentDetailsAvailable(record:DeploymentRecord)'))
  assert.match(view, /deploymentDetailsAvailable\(record\)\?'button':'div'/)
  assert.match(view, /:aria-expanded="deploymentDetailsAvailable\(record\)\?deploymentDetailsOpen\(record\):undefined"/)
  const inline = item.slice(summaryEnd, detailsIndex)
  for (const label of ['命名空间', '工作负载', '副本', '镜像']) assert.ok(inline.includes('<dt>' + label + '</dt>'), label)
  assert.ok(inline.includes('application-instance-facts-inline'))
  // 事实行出现在展开区之前，且不再留在详情里
  const details = item.slice(detailsIndex)
  assert.ok(!details.includes('<dt>工作负载</dt>'))
  assert.ok(!details.includes('<dt>副本</dt>'))
  assert.ok(!details.includes('application-deployment-actions'), '操作按钮不应只在展开区显示')
})

test('行内操作包含日志、停止、重启、扩缩容与回退，K8s 日志按真实 Pod 身份打开', () => {
  const inline = item.slice(summaryEnd, detailsIndex)
  for (const label of ['日志', '停止', '重启', '扩缩容', '回退上一镜像']) {
    const matcher = new RegExp('>' + label + '<')
    assert.ok(inline.match(matcher) || (label === '回退上一镜像' && inline.includes("rollbackT('rollback.title')")), label)
  }
  assert.ok(view.includes('async function openKubernetesDeploymentLogs(record:DeploymentRecord)'))
  assert.ok(view.includes("'/pods'"))
  assert.ok(view.includes("'/logs/ws'"))
  assert.ok(view.includes("kubernetes.workload.read"))
  assert.ok(view.includes('if(!pod){message.warning('), '没有运行中的 Pod 时必须明确提示')
})

test('停止与重启先二次确认，扩缩容使用真实运行时副本与 HPA 状态', () => {
  assert.ok(view.includes('Modal.confirm({'))
  assert.equal(view.match(/confirmDeploymentRuntimeAction\(record,'stop'\)/g)?.length >= 1, true)
  assert.equal(view.match(/confirmDeploymentRuntimeAction\(record,'restart'\)/g)?.length >= 1, true)
  assert.ok(view.includes("operation:'stop'") === false)
  assert.ok(view.includes("'/resources/horizontalpodautoscalers/'"), '自动伸缩必须读取真实 HPA')
  assert.ok(view.includes('scaleAutoscaler.error=apiErrorMessage(error)') || view.includes('scaleAutoscaler.error=apiErrorMessage(error)'))
  assert.ok(view.includes('手动伸缩') && view.includes('自动伸缩'))
  assert.ok(view.includes(':disabled="!scaleChanged"'), '副本数未变化时不能提交')
})

test('回退弹窗先列历史版本，点击回退后再二次确认', () => {
  assert.ok(rollbackModal.includes('rollback-history'))
  assert.ok(rollbackModal.includes('function pickRollback(entry: HistoryEntry)'))
  assert.ok(rollbackModal.includes("stage.value = 'confirm'"))
  assert.ok(rollbackModal.includes("stage === 'confirm'"))
  assert.ok(rollbackModal.includes('rollback-action') === false)
  assert.ok(rollbackModal.includes('!entry.rollbackable'), '只能回退服务端核对的上一镜像')
  assert.ok(rollbackModal.includes('rollback.onlyPrevious'))
})

test('行内条与空状态样式只作用于部署实例列表', () => {
  assert.ok(css.includes('.application-deployment-inline'))
  assert.ok(css.includes('.application-instance-facts-inline'))
  assert.ok(css.includes('.application-instance-facts-inline > .fact-image dd'))
  assert.ok(css.includes('.application-deployment-summary.is-static'))
  assert.ok(css.includes('.scale-dialog') && css.includes('.scale-panel'))
})
