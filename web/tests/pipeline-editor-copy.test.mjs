import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { baseParse, compile } from '@vue/compiler-dom'
import { parse } from '@vue/compiler-sfc'
import { renderToString } from '@vue/server-renderer'
import * as vue from 'vue'
import ts from 'typescript'

const template = path => parse(readFileSync(new URL(path, import.meta.url), 'utf8')).descriptor.template.content
const editorTemplate = template('../src/views/PipelineEditorView.vue')
const composeTemplate = template('../src/components/ComposeTaskFields.vue')
function findSection(node) {
  if (node.tag === 'section' && node.props.some(prop => prop.name === 'class' && prop.value?.content === 'node-notification-section')) return node
  return node.children?.map(findSection).find(Boolean)
}
const notificationTemplate = findSection(baseParse(editorTemplate)).loc.source
const passthrough = {
  inheritAttrs: false,
  setup(_, { attrs, slots }) {
    return () => vue.h('div', attrs, [attrs.message, attrs.description, slots.label?.(), slots.default?.()])
  },
}
async function render(source, state) {
  const { code } = compile(source, { mode: 'function', prefixIdentifiers: true, expressionPlugins: ['typescript'] })
  const output = ts.transpileModule(code, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText
  const render = new Function('Vue', output)(vue)
  const app = vue.createSSRApp({ setup: () => state, render })
  for (const name of ['a-alert', 'a-button', 'a-select', 'a-form-item', 'a-input', 'a-textarea', 'a-checkbox', 'a-tooltip', 'Bell', 'Plus', 'Trash2']) app.component(name, passthrough)
  return renderToString(app)
}
function notificationState(patch = {}) {
  return {
    selectedTask: { config: { notifications: [] } }, canReadNotificationChannels: true,
    canCreateNotificationChannels: true, canEdit: true, activeNotificationChannels: [],
    openNotificationChannelModal() {}, addNotificationRule() {}, ...patch,
  }
}

test('没有通知渠道时删除空渠道提示，保留创建入口与禁用的添加按钮', async () => {
  for (const notifications of [[], [{ id: 'saved-rule', channel_id: 'inactive-channel' }]]) {
    const html = await render(notificationTemplate, notificationState({ selectedTask: { config: { notifications } } }))
    assert.doesNotMatch(html, /还没有可用通知渠道|当前任务尚未配置通知|node-notification-list/)
    assert.match(html, /新建渠道/)
    assert.match(html, /disabled[^>]*>.*?添加/s)
  }
})
test('通知渠道读取权限提醒和已有渠道下的未配置通知提醒继续保留', async () => {
  const denied = await render(notificationTemplate, notificationState({ canReadNotificationChannels: false }))
  assert.match(denied, /当前账号没有读取通知渠道的权限/)
  const available = await render(notificationTemplate, notificationState({ activeNotificationChannels: [{ id: 'channel' }] }))
  assert.match(available, /当前任务尚未配置通知/)
})
test('部署任务自动管理项目名，保留 Docker Compose 名称和服务提示', async () => {
  const html = await render(composeTemplate, {
    error: '', kind: 'compose_stack', disabled: false,
    node: { config: { deployment_plan_id: 'plan' } }, tagReference:'${EDO_IMAGE_TAG}', validTagReference:()=>true,
    settings: { project_name: 'team', tag_variable: 'EDO_IMAGE_TAG', services: [] }, services: [], updateSettings() {},
  })
  assert.doesNotMatch(html, /使用 EDO 保存的|上游构建镜像自动匹配并固定摘要/)
  assert.match(html, /Docker Compose 部署配置/)
  assert.doesNotMatch(html, /查看和编辑 Docker Compose 部署方案/)
  assert.doesNotMatch(html, /Docker Compose 项目名/)
  assert.match(html, /镜像 Tag/)
  assert.match(html, /留空执行整个 compose.yaml 项目/)
})
test('部署方案读取失败或方案类型不匹配时仍显示必要警告', async () => {
  const html = await render(composeTemplate, { error: '读取失败', kind: '', node: { config: { compose: {} } } })
  assert.match(html, /读取失败/)
  assert.match(html, /请选择 Docker Compose 部署方案/)
})

test('部署任务通知分区只渲染通知，不再渲染部署参数编辑器',async()=>{
 const tree=baseParse(editorTemplate)
 const find=node=>node.tag==='a-form'&&node.children?.some(child=>child.tag==='section'&&child.props?.some(prop=>prop.name==='class'&&prop.value?.content==='node-notification-section'))?node:node.children?.map(find).find(Boolean)
 const form=find(tree)
 const html=await render(form.loc.source.replace('v-else',''),{...notificationState(),selectedNode:{id:'deploy',type:'deploy',config:{}},canUpdate:true,canEdit:true})
 assert.match(html,/node-notification-section/)
 assert.doesNotMatch(html,/task-controls|构建制品来源|部署脚本|额外引用共享变量|部署前下载/)
})
