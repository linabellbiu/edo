import assert from 'node:assert/strict'
import { test } from 'node:test'
import * as vue from 'vue'
import { renderToString } from '@vue/server-renderer'
import * as configText from '../src/utils/configText.ts'
import { loadFrontend } from './acceptanceHarness.mjs'

const envTextModule = loadFrontend('../src/utils/envText.ts')

const codes = ['delivery_variable.read', 'delivery_variable.create', 'delivery_variable.update', 'delivery_variable.delete', 'application.read', 'deployment.read']
const settle = async () => { await vue.nextTick(); await new Promise(resolve => setImmediate(resolve)); await vue.nextTick() }
const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
const variable = (project = 'project-a', patch = {}) => ({ id: `variable-${project}`, project_id: project, name: 'API_URL', description: '仅当前项目使用', kind: 'env', usage: 'build', application_id: '', environment_id: '', is_secret: false, is_active: true, version: 3, protected_refs: [], value: 'https://example.test', ...patch })

async function harness({ permissions = codes, project = 'project-a', selected = project, props = {}, search = '', preferences = new Map(), get, put, post, del, resolveVueProps = false, authorizationState = 'ready' } = {}) {
  const requests = [], notifications = [], confirmations = [], mounted = [], unmounts = []
  const auth = vue.reactive({
    user: { id: 'operator-a', department_id: 'department-a', project_id: project, is_superuser: false, permissions: [...permissions] }, authorizationState,
    get authorizationReady() { return this.authorizationState === 'ready' },
    canAny(required) { return !!this.user && (this.user.is_superuser || required.some(code => this.user.permissions.includes(code))) },
    waitForAuthorization(signal) {
      if (signal.aborted || this.authorizationState !== 'checking') return Promise.resolve(!signal.aborted && this.authorizationReady)
      return new Promise(resolve => {
        const finish = ready => { stop(); signal.removeEventListener('abort', abort); resolve(ready) }
        const abort = () => finish(false)
        const stop = vue.watch(() => this.authorizationState, state => { if (state !== 'checking') finish(state === 'ready') }, { flush: 'sync' })
        signal.addEventListener('abort', abort, { once: true })
      })
    },
  })
  const storage = { selected }
  const localStorage = {
    getItem: key => key === 'edo.project' ? storage.selected : (preferences.get(key) ?? null),
    setItem: (key, value) => { if (key === 'edo.project') storage.selected = String(value); else preferences.set(key, String(value)) },
  }
  const window = Object.assign(new EventTarget(), { location: { search } })
  const client = Object.fromEntries(['get', 'put', 'post', 'delete'].map(method => [method, async (path, bodyOrOptions, options) => {
    const request = { method, path, body: ['post', 'put'].includes(method) ? structuredClone(bodyOrOptions) : undefined, options: ['post', 'put'].includes(method) ? options : bodyOrOptions }
    requests.push(request)
    const handler = { get, put, post, delete: del }[method]
    if (handler) { const response = await handler(request); if (response) return response }
    const projectID = request.options.headers['X-EDO-Project-ID']
    return { data: path === '/delivery-variables' ? { variables: [variable(projectID)] } : path === '/projects' ? { projects: [{ id: 'project-a', name: '项目 A' }, { id: 'project-b', name: '项目 B' }] } : path === '/applications' ? { applications: [{ id: `app-${projectID}`, name: '应用' }] } : path === '/environments' ? { environments: [{ id: `env-${projectID}`, name: '环境' }] } : {} }
  }]))
  const component = loadFrontend('../src/views/DeliveryVariablesView.vue', {
    vue: { ...vue, onMounted: fn => mounted.push(fn), onBeforeUnmount: fn => unmounts.push(fn) },
    '@/utils/configText':configText, '@/utils/envText': envTextModule, '@/stores/auth': { useAuthStore: () => auth }, '@/api/client': { __esModule: true, default: client }, '@/api/resources': { apiErrorMessage: () => '当前项目操作失败，请重试' },
    'ant-design-vue': { Modal: { confirm(config) { const item = { ...config, destroyed: false, destroy() { this.destroyed = true; config.afterClose?.() } }; confirmations.push(item); return item } }, message: Object.fromEntries(['success', 'warning', 'error'].map(kind => [kind, text => notifications.push({ kind, text })])) },
  }, { localStorage, window }).default
  const scope = vue.effectScope(); let state, stopped = false
  if (resolveVueProps) {
    // 让 Vue 按编译出的组件声明处理缺省的 Boolean 属性，再执行真实 setup。
    const app = vue.createSSRApp({ ...component, setup(resolvedProps, context) {
      scope.run(() => { state = component.setup(resolvedProps, context) })
      return () => null
    } }, props)
    await renderToString(app)
  } else scope.run(() => { state = component.setup(props, { expose() {} }) })
  mounted.forEach(fn => fn()); await settle()
  const stop = () => { if (stopped) return; stopped = true; unmounts.forEach(fn => fn()); scope.stop() }
  return { state, auth, storage, window, requests, notifications, confirmations, stop }
}
const writes = h => h.requests.filter(item => item.method !== 'get')
const changeProject = async (h, id) => { h.storage.selected = id; h.window.dispatchEvent(new Event('edo:projects-changed')); h.auth.user.project_id = id; await settle() }

test('首次打开配置页等待当前项目授权就绪，校准暂不可用后恢复也会自动加载', async t => {
  const h = await harness({ authorizationState: 'checking' }); t.after(h.stop)
  assert.deepEqual(h.requests, [])
  h.auth.authorizationState = 'unavailable'; await settle()
  assert.equal(h.state.error.value, ''); assert.deepEqual(h.requests, [])
  h.auth.authorizationState = 'checking'
  h.auth.authorizationState = 'ready'; await settle()
  assert.equal(h.state.rows.value[0].project_id, 'project-a')
  assert.equal(h.requests.filter(request => request.path === '/delivery-variables').length, 1)
})

test('首轮目录请求遇到实时授权校准后自动重读一次，原回执不显示错误或覆盖新数据', async t => {
  for (const code of ['EDO_AUTHORIZATION_PENDING', 'EDO_AUTHORIZATION_CHANGED']) {
    const first = deferred(); let reads = 0
    const h = await harness({ get: request => request.path === '/delivery-variables' && ++reads === 1 ? first.promise : undefined }); t.after(h.stop)
    h.auth.authorizationState = 'checking'
    first.reject(Object.assign(new Error('旧授权下的目录读取已失效'), { code })); await settle()
    assert.equal(reads, 1); assert.equal(h.state.error.value, '')
    h.auth.authorizationState = 'ready'; await settle()
    assert.equal(reads, 2); assert.equal(h.state.rows.value[0].project_id, 'project-a'); assert.equal(h.state.error.value, '')
    assert.deepEqual(writes(h), [])
  }
})

test('连续两次目录重读遇到权限变化时保留已有内容并在刷新入口提示重试', async t => {
  let changes = 0
  const h = await harness({ get: request => {
    if (request.path === '/delivery-variables' && changes > 0) {
      changes--
      throw Object.assign(new Error('旧授权下的目录读取已失效'), { code: 'EDO_AUTHORIZATION_CHANGED' })
    }
  } }); t.after(h.stop)
  const rows = h.state.rows.value
  assert.equal(rows.length, 1)
  changes = 2
  await h.state.load(); await settle()
  assert.equal(h.requests.filter(request => request.path === '/delivery-variables').length, 3)
  assert.equal(h.state.rows.value, rows)
  assert.equal(h.state.loading.value, false)
  assert.equal(h.state.error.value, '')
  assert.match(h.state.refreshError.value, /请刷新列表/)
  assert.deepEqual(h.notifications, [{ kind: 'warning', text: h.state.refreshError.value }])

  await h.state.load(); await settle()
  assert.equal(h.requests.filter(request => request.path === '/delivery-variables').length, 4)
  assert.equal(h.state.refreshError.value, '')
  assert.deepEqual(writes(h), [])
})

test('真实 403 不随权限校准重试，也不会自动重放变更', async t => {
  const first = deferred(); let reads = 0
  const h = await harness({ get: request => request.path === '/delivery-variables' && ++reads === 1 ? first.promise : undefined }); t.after(h.stop)
  h.auth.authorizationState = 'checking'
  first.reject(Object.assign(new Error('没有执行此操作的权限'), { response: { status: 403 } })); await settle()
  h.auth.authorizationState = 'ready'; await settle()
  assert.equal(reads, 1); assert.notEqual(h.state.error.value, ''); assert.deepEqual(writes(h), [])
})

test('等待校准时切换项目会中止旧范围，只读取新项目并拦住旧编辑和确认', async t => {
  const h = await harness(); t.after(h.stop)
  const item = h.state.rows.value[0]
  h.state.edit(item)
  h.auth.authorizationState = 'checking'
  await h.state.save()
  assert.deepEqual(writes(h), [])
  h.auth.authorizationState = 'ready'; await settle()
  assert.deepEqual(writes(h), [], '授权恢复不能自动保存草稿')
  h.state.closeEditor()
  h.state.remove(item)
  const confirmation = h.confirmations[0]
  h.auth.authorizationState = 'checking'
  await confirmation.onOk()
  assert.deepEqual(writes(h), [], '正在核验时旧确认不能提交')
  const waiting = h.state.load()
  await changeProject(h, 'project-b')
  h.auth.authorizationState = 'ready'; await waiting; await settle()
  await confirmation.onOk()
  assert.deepEqual(writes(h), [])
  assert.deepEqual(h.requests.filter(request => request.path === '/delivery-variables').map(request => request.options.headers['X-EDO-Project-ID']), ['project-a', 'project-b'])
  assert.equal(h.state.rows.value[0].project_id, 'project-b')
})

test('真实 Vue 挂载不传配置类型时，独立入口与应用入口都显示配置文件和变量', async t => {
  for (const props of [{}, { applicationId: 'app-project-a' }]) {
    const entries = [
      variable('project-a', { id: 'config', kind: 'config_yaml', usage: 'deploy', application_id: props.applicationId || '' }),
      variable('project-a', { id: 'variable', usage: 'deploy', application_id: props.applicationId || '' }),
    ]
    const h = await harness({ props, resolveVueProps: true, get: request => request.path === '/delivery-variables' ? { data: { variables: entries } } : undefined })
    t.after(h.stop)
    assert.equal(h.state.unifiedMode.value, true)
    assert.deepEqual(h.state.filtered.value.map(item => item.id), ['config', 'variable'])
  }
})

test('配置编辑以保存正文为基线高亮差异，历史入口不修改配置',async t=>{
 const h=await harness({props:{configurationFiles:true},get:req=>req.path==='/delivery-variables'?{data:{variables:[variable('project-a',{name:'orders_settings',kind:'config_yaml',usage:'deploy',value:'port: 8080\n'})]}}:undefined});t.after(h.stop)
 h.state.edit(h.state.rows.value[0]);assert.equal(h.state.draftOriginal.value,'port: 8080\n');assert.equal(h.state.showDraftDiff.value,true)
 h.state.form.value='port: 9090\n';assert.equal(h.state.draftOriginal.value,'port: 8080\n')
 h.state.showHistory(h.state.rows.value[0]);assert.equal(h.state.open.value,false);assert.equal(h.state.form.value,'');assert.equal(h.state.historyTarget.value.name,'orders_settings');assert.deepEqual(writes(h),[])
 await changeProject(h,'project-b');assert.equal(h.state.historyTarget.value,undefined)
})

test('密钥原文需要独立读取权限，读取绑定版本且保留已输入的替换草稿',async t=>{
 const h=await harness({props:{configurationFiles:true},get:req=>req.path==='/delivery-variables'?{data:{variables:[variable('project-a',{kind:'config_env',usage:'deploy',is_secret:true,value:undefined})]}}:undefined,post:()=>({data:{revision:{version:3,value:'TOKEN=original\n',hidden:false}}})});t.after(h.stop)
 h.state.edit(h.state.rows.value[0]);await h.state.readOriginal();assert.equal(writes(h).length,0)
 h.auth.user.permissions.push('delivery_variable.reveal');await settle();h.state.edit(h.state.rows.value[0])
 h.state.replaceValue.value=true;h.state.form.value='TOKEN=draft\n';await h.state.readOriginal()
 assert.equal(h.state.draftOriginal.value,'TOKEN=original\n');assert.equal(h.state.form.value,'TOKEN=draft\n');assert.ok(writes(h)[0].path.endsWith('/revisions/3/reveal'))
 h.auth.user.permissions=['delivery_variable.read'];await settle();assert.equal(h.state.draftOriginal.value,undefined);assert.equal(h.state.form.value,'')
})

test('应用入口固定应用和环境，文本无需引用名，保存自动使用但不发布', async t => {
  const props=vue.reactive({applicationId:'app-project-a',applicationName:'应用 A'})
  const h=await harness({props});t.after(h.stop)
  h.state.environmentFilter.value='env-project-a';await settle();h.state.edit()
  assert.equal(h.state.form.application_id,props.applicationId);assert.equal(h.state.form.environment_id,'env-project-a')
  assert.equal(h.state.form.kind,'env_text');assert.ok(h.state.form.name)
  h.state.form.value='TOKEN=literal$token\nEMPTY=';await h.state.save()
  assert.equal(writes(h).length,1);assert.equal(writes(h)[0].path,'/delivery-variables')
  assert.equal(writes(h)[0].body.auto_deploy,true);assert.equal(writes(h)[0].body.application_id,props.applicationId)
  h.state.edit();h.state.form.value='旧环境草稿';h.state.environmentFilter.value='other';await settle();await h.state.save()
  assert.equal(h.state.form.value,'');assert.equal(h.state.open.value,false);assert.equal(writes(h).length,1)
})

test('应用切换清空草稿，其他应用或环境的条目不能从应用入口编辑', async t => {
  const props=vue.reactive({applicationId:'app-project-a'})
  const h=await harness({props,get:request=>request.path==='/delivery-variables'?{data:{variables:[variable('project-a',{application_id:'app-project-a',usage:'deploy'}),variable('project-a',{id:'other',application_id:'another-app',usage:'deploy'})]}}:undefined});t.after(h.stop)
  assert.equal(h.state.filtered.value.length,1);h.state.edit(h.state.rows.value[1]);assert.equal(h.state.open.value,false)
  h.state.edit();h.state.form.value='旧应用草稿';props.applicationId='another-app';await settle()
  assert.equal(h.state.open.value,false);assert.equal(h.state.form.value,'');assert.equal(h.state.filtered.value[0].id,'other')
})

test('变量页只读取当前项目并显示名称，读请求固定项目头且不额外执行写入', async t => {
  const h = await harness(); t.after(h.stop)
  assert.equal(h.state.projectName.value, '项目 A'); assert.equal(h.state.rows.value[0].project_id, 'project-a')
  assert.ok(h.requests.every(item => item.options.headers['X-EDO-Project-ID'] === 'project-a'))
  assert.deepEqual(writes(h), [])
})

test('未选项目、项目权限尚未对应、没有读取角色权限时不读取也不能修改', async t => {
  for (const options of [{ selected: '' }, { selected: 'project-b' }, { permissions: ['delivery_variable.update', 'delivery_variable.delete'] }]) {
    const h = await harness(options); t.after(h.stop)
    h.state.edit(); h.state.edit(variable()); h.state.remove(variable()); await h.state.save(); await h.state.load()
    assert.deepEqual(h.requests, []); assert.equal(h.state.open.value, false); assert.equal(h.confirmations.length, 0)
  }
})

test('只读角色能查看但不能新增编辑或删除，前端动作再次检查权限', async t => {
  const h = await harness({ permissions: ['delivery_variable.read'] }); t.after(h.stop)
  h.state.edit(); h.state.edit(h.state.rows.value[0]); h.state.remove(h.state.rows.value[0]); await h.state.save()
  assert.equal(h.state.can('create'), false); assert.equal(h.state.can('update'), false); assert.equal(h.state.can('delete'), false)
  assert.equal(h.state.open.value, false); assert.equal(h.confirmations.length, 0); assert.deepEqual(writes(h), [])
})

test('异常返回其他项目变量不会展示或成为编辑删除目标', async t => {
  const h = await harness({ get: request => request.path === '/delivery-variables' ? { data: { variables: [variable(), variable('project-b')] } } : undefined }); t.after(h.stop)
  assert.deepEqual(h.state.rows.value.map(item => item.project_id), ['project-a'])
  h.state.edit(variable('project-b')); h.state.remove(variable('project-b'))
  assert.equal(h.state.open.value, false); assert.equal(h.confirmations.length, 0)
})

test('编辑草稿期间切项目立即清空密钥、旧应用环境和筛选，不在新范围保存', async t => {
  const h = await harness(); t.after(h.stop)
  h.state.edit(h.state.rows.value[0]); h.state.form.is_secret = true; h.state.form.value = '仅旧项目的草稿'; h.state.search.value = '旧搜索'
  await changeProject(h, 'project-b'); await h.state.save()
  assert.equal(h.state.open.value, false); assert.equal(h.state.form.value, ''); assert.equal(h.state.form.name, ''); assert.equal(h.state.search.value, '')
  assert.equal(h.state.projectName.value, '项目 B'); assert.equal(h.state.applications.value[0].id, 'app-project-b'); assert.deepEqual(writes(h), [])
})

test('保存冻结原项目和版本，切换后中止请求且迟到结果不能关闭新草稿', async t => {
  const pending = deferred(), h = await harness({ put: () => pending.promise }); t.after(h.stop)
  h.state.edit(h.state.rows.value[0]); h.state.form.value = '原项目值'; const saving = h.state.save(); await h.state.save()
  assert.equal(writes(h).length, 1); const request = writes(h)[0]
  assert.equal(request.options.headers['X-EDO-Project-ID'], 'project-a'); assert.equal(request.body.expected_version, 3)
  await changeProject(h, 'project-b'); assert.equal(request.options.signal.aborted, true)
  h.state.edit(); h.state.form.kind = 'env'; h.state.form.name = 'B_ONLY'; h.state.form.value = '新项目草稿'
  pending.resolve({ data: {} }); await saving
  assert.equal(h.state.open.value, true); assert.equal(h.state.form.value, '新项目草稿'); assert.equal(h.notifications.length, 0)
})

test('本地选择已变化但事件尚未派发时也不能按旧权限发出修改', async t => {
  const h = await harness(); t.after(h.stop)
  h.state.edit(h.state.rows.value[0]); h.state.form.value = '旧值'; h.storage.selected = 'project-b'
  await h.state.save(); h.state.remove(h.state.rows.value[0]); assert.deepEqual(writes(h), []); assert.equal(h.confirmations.length, 0)
})

test('角色权限撤销立即关闭编辑并清空内容，已有保存的迟到失败不弹到新范围', async t => {
  const pending = deferred(), h = await harness({ post: () => pending.promise }); t.after(h.stop)
  h.state.edit(); h.state.form.kind = 'env'; h.state.form.name = 'KEY'; h.state.form.is_secret = true; h.state.form.value = '敏感草稿'; const saving = h.state.save()
  h.auth.user.permissions = ['delivery_variable.read']; await settle()
  assert.equal(h.state.open.value, false); assert.equal(h.state.form.value, ''); assert.equal(writes(h)[0].options.signal.aborted, true)
  pending.reject(new Error('旧请求错误')); await saving
  assert.equal(h.state.editorError.value, ''); assert.equal(h.notifications.length, 0)
})

test('删除确认绑定项目和角色，切项目/撤权销毁确认且旧确认回调不能删除', async t => {
  for (const update of [h => changeProject(h, 'project-b'), async h => { h.auth.user.permissions = ['delivery_variable.read']; await settle() }]) {
    const h = await harness(); t.after(h.stop); h.state.remove(h.state.rows.value[0]); const confirmation = h.confirmations[0]
    assert.match(confirmation.content, /项目 A/); await update(h); assert.equal(confirmation.destroyed, true)
    await confirmation.onOk(); assert.deepEqual(writes(h), [])
  }
})

test('删除确认后才执行且禁止双击，保留项目头和预期版本', async t => {
  const pending = deferred(), h = await harness({ del: () => pending.promise }); t.after(h.stop)
  h.state.remove(h.state.rows.value[0]); assert.deepEqual(writes(h), [])
  const removal = h.confirmations[0].onOk(); await h.confirmations[0].onOk()
  assert.equal(writes(h).length, 1); assert.equal(writes(h)[0].options.headers['X-EDO-Project-ID'], 'project-a'); assert.equal(writes(h)[0].options.params.version, 3)
  pending.resolve({ data: {} }); await removal; assert.equal(h.notifications[0].text, '变量已删除')
})

test('读取晚到不能把旧项目行覆盖新项目，权限撤销后不保留读取结果', async t => {
  const pending = deferred(), h = await harness({ get: request => request.path === '/delivery-variables' && request.options.headers['X-EDO-Project-ID'] === 'project-a' ? pending.promise : undefined }); t.after(h.stop)
  const old = h.requests.find(item => item.path === '/delivery-variables'); await changeProject(h, 'project-b')
  pending.resolve({ data: { variables: [variable()] } }); await settle()
  assert.equal(old.options.signal.aborted, true); assert.equal(h.state.rows.value[0].project_id, 'project-b')
  h.auth.user.permissions = []; await settle(); assert.deepEqual(h.state.rows.value, []); assert.deepEqual(h.state.applications.value, [])
})

test('服务端403清空已有列表、密钥草稿和确认，不把受限内容保留页面', async t => {
  const h = await harness({ put: () => { throw { response: { status: 403 } } } }); t.after(h.stop)
  h.state.edit(h.state.rows.value[0]); h.state.form.value = '不应残留'; await h.state.save()
  assert.equal(h.state.open.value, false); assert.equal(h.state.form.value, ''); assert.deepEqual(h.state.rows.value, []); assert.match(h.state.error.value, /当前项目操作失败/)
})

test('保存保留未替换密钥语义，普通值不丢失；成功只刷新当前项目', async t => {
  const h = await harness({ get: request => request.path === '/delivery-variables' ? { data: { variables: [variable('project-a', { is_secret: true, value: undefined })] } } : undefined }); t.after(h.stop)
  h.state.edit(h.state.rows.value[0]); assert.equal(h.state.form.value, ''); assert.equal(h.state.replaceValue.value, false)
  await h.state.save(); assert.equal(writes(h)[0].body.value, undefined); assert.equal(h.state.open.value, false)
  h.state.edit(); h.state.form.kind = 'env'; h.state.form.name = 'NORMAL'; h.state.form.value = '可见普通值'; await h.state.save()
  assert.equal(writes(h)[1].body.value, '可见普通值'); assert.ok(h.requests.every(item => item.options.headers['X-EDO-Project-ID'] === 'project-a'))
})

test('卸载取消所有读取，保存迟到响应不提示成功或恢复数据', async t => {
  const pending = deferred(), h = await harness({ post: () => pending.promise }); t.after(h.stop)
  h.state.edit(); h.state.form.kind = 'env'; h.state.form.name = 'KEY'; h.state.form.value = '草稿'; const saving = h.state.save(); h.stop()
  assert.equal(writes(h)[0].options.signal.aborted, true); assert.equal(h.state.form.value, '')
  pending.resolve({ data: {} }); await saving; assert.deepEqual(h.notifications, []); assert.deepEqual(h.state.rows.value, [])
})


test('env 文本默认用于部署，保存物理行与文件名，错误文本不会发送请求', async t => {
 const h = await harness(); t.after(h.stop)
 h.state.edit()
 assert.equal(h.state.form.kind, 'env_text'); assert.equal(h.state.form.usage, 'deploy'); assert.equal(h.state.form.is_secret, true)
 h.state.form.name = 'PROD_ENV'; h.state.form.value = 'TOKEN=a=b $literal\nEMPTY=\n'
 await h.state.save()
 assert.equal(writes(h)[0].body.kind, 'env_text'); assert.equal(writes(h)[0].body.value, 'TOKEN=a=b $literal\nEMPTY=\n')
 h.state.edit(); h.state.form.kind = 'env_file'; h.state.form.name = '.prod.env'; h.state.form.value = 'TOKEN=literal'
 await h.state.save()
 assert.equal(writes(h)[1].body.kind, 'env_file'); assert.equal(writes(h)[1].body.name, '.prod.env')
 h.state.edit(); h.state.form.name = 'INVALID'; h.state.form.value = 'TOKEN=private\nTOKEN=again'
 await h.state.save(); assert.equal(writes(h).length, 2); assert.match(h.state.editorError.value, /第 2 行/); assert.doesNotMatch(h.state.editorError.value, /private/)
})


test('配置文件原文保存且不自动关联，错误格式不会发送请求',async t=>{
 const h=await harness({props:{configurationFiles:true}});t.after(h.stop)
 h.state.edit();h.state.form.name='app.yaml';await settle();h.state.form.value='server: [broken';await h.state.save()
 assert.equal(writes(h).length,0);assert.match(h.state.editorError.value,/YAML/)
 h.state.form.value='server:\n  password: literal$token\n';await h.state.save()
 assert.equal(writes(h)[0].body.kind,'config_yaml');assert.equal(writes(h)[0].body.auto_deploy,false);assert.equal(writes(h)[0].body.value,'server:\n  password: literal$token\n')
 h.state.edit();h.state.form.name='app.toml';await settle();h.state.form.value='port = 8080';assert.equal(h.state.form.kind,'config_toml');await h.state.save();assert.equal(writes(h).length,2)
})

test('配置页不会把旧变量当成配置文件，按应用环境继续隔离',async t=>{
 const h=await harness({props:{configurationFiles:true,applicationId:'app-project-a'},get:request=>request.path==='/delivery-variables'?{data:{variables:[variable('project-a',{id:'config-a',name:'app.yaml',kind:'config_yaml',usage:'deploy',application_id:'app-project-a'}),variable('project-a',{id:'old-env',kind:'env_text',usage:'deploy',application_id:'app-project-a'}),variable('project-a',{id:'prod-config',kind:'config_yaml',usage:'deploy',application_id:'app-project-a',environment_id:'prod'})]}}:undefined});t.after(h.stop)
 assert.deepEqual(h.state.filtered.value.map(v=>v.id),['config-a']);h.state.environmentFilter.value='prod';await settle();assert.deepEqual(h.state.filtered.value.map(v=>v.id),['prod-config'])
})

test('合并模式统一展示配置文件和变量秘钥，分段筛选记住项目偏好且显式查询优先', async t => {
  const preferences = new Map()
  const dataset = projectID => [
    variable(projectID, { id: `cfg-${projectID}`, name: 'application.yaml', kind: 'config_yaml', usage: 'deploy', value: 'port: 8080\n' }),
    variable(projectID, { id: `var-${projectID}`, name: 'API_TOKEN', kind: 'env', usage: 'build', is_secret: true, value: undefined }),
  ]
  const h = await harness({ preferences, get: req => req.path === '/delivery-variables' ? { data: { variables: dataset(req.options.headers['X-EDO-Project-ID']) } } : undefined }); t.after(h.stop)
  assert.equal(h.state.unifiedMode.value, true)
  assert.equal(h.state.category.value, 'all')
  assert.equal(h.state.configCount.value, 1); assert.equal(h.state.variableCount.value, 1)
  assert.deepEqual(h.state.filtered.value.map(v => v.id), ['cfg-project-a', 'var-project-a'])

  h.state.selectCategory('config'); await settle()
  assert.deepEqual(h.state.filtered.value.map(v => v.id), ['cfg-project-a'])
  h.state.selectCategory('variable'); await settle()
  assert.deepEqual(h.state.filtered.value.map(v => v.id), ['var-project-a'])

  // 切到项目 B 不沿用项目 A 的偏好，切回项目 A 恢复项目 A 上次选择
  await changeProject(h, 'project-b')
  assert.equal(h.state.category.value, 'all')
  assert.deepEqual(h.state.filtered.value.map(v => v.id), ['cfg-project-b', 'var-project-b'])
  await changeProject(h, 'project-a')
  assert.equal(h.state.category.value, 'variable')

  // 显式 URL 查询参数优先于本地偏好
  const explicit = await harness({ preferences, search: '?category=config', get: req => req.path === '/delivery-variables' ? { data: { variables: dataset(req.options.headers['X-EDO-Project-ID']) } } : undefined }); t.after(explicit.stop)
  assert.equal(explicit.state.category.value, 'config')
  assert.deepEqual(explicit.state.filtered.value.map(v => v.id), ['cfg-project-a'])
})

test('合并模式新建可切换配置文件与变量秘钥，编辑时历史和读取原文仍仅限配置文件', async t => {
  const rows = [
    variable('project-a', { id: 'cfg-secret', name: 'runtime.env', kind: 'config_env', usage: 'deploy', is_secret: true, value: undefined }),
    variable('project-a', { id: 'var-secret', name: 'DB_PASSWORD', kind: 'env', usage: 'deploy', is_secret: true, value: undefined }),
  ]
  const h = await harness({
    permissions: [...codes, 'delivery_variable.reveal'],
    get: req => req.path === '/delivery-variables' ? { data: { variables: rows } } : undefined,
    post: req => req.path.endsWith('/reveal') ? { data: { revision: { version: 3, value: 'PORT=8080\n', hidden: false } } } : { data: {} },
  }); t.after(h.stop)

  // 新建时从变量切到配置文件，提交强制 deploy + auto_deploy=false
  h.state.edit()
  assert.equal(h.state.formIsConfig.value, false)
  h.state.switchEntryType('config'); await settle()
  assert.equal(h.state.formIsConfig.value, true)
  h.state.form.name = 'service.yaml'; await settle()
  assert.equal(h.state.form.kind, 'config_yaml')
  h.state.form.value = 'port: 8080\n'
  await h.state.save()
  assert.equal(writes(h).length, 1)
  assert.equal(writes(h)[0].body.kind, 'config_yaml')
  assert.equal(writes(h)[0].body.usage, 'deploy')
  assert.equal(writes(h)[0].body.auto_deploy, false)

  // 普通变量密钥不允许查看历史或读取原文
  h.state.showHistory(rows[1])
  assert.equal(h.state.historyTarget.value, undefined)
  h.state.edit(rows[1]); await h.state.readOriginal()
  assert.equal(writes(h).length, 1)
  assert.equal(h.state.draftOriginal.value, undefined)

  // 配置文件密钥允许查看历史和带权限读取原文
  h.state.edit(rows[0]); await h.state.readOriginal()
  assert.equal(writes(h).length, 2)
  assert.ok(writes(h)[1].path.endsWith('/cfg-secret/revisions/3/reveal'))
  assert.equal(h.state.draftOriginal.value, 'PORT=8080\n')
  h.state.showHistory(rows[0])
  assert.equal(h.state.historyTarget.value.id, 'cfg-secret')
})
