import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'

// 编译真实组件的脚本，仅隔离路由、网络和页面生命周期，不复制表单业务逻辑。
const filename = new URL('../src/views/DevOpsView.vue', import.meta.url)
const { descriptor } = parse(readFileSync(filename, 'utf8'), { filename: filename.pathname })
const compiled = compileScript(descriptor, { id: 'application-repository-test' })
const { outputText } = ts.transpileModule(compiled.content, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true },
})

function formHarness({ section = 'applications', repositoryCreate = true, credentialCreate = true } = {}) {
  const requests = []
  const notifications = []
  let failure
  const saved = { id: 'new-repository', name: 'new_repo', is_active: true, default_branch: 'main' }
  const client = {
    async post(path, payload) {
      requests.push({ method: 'post', path, payload })
      if (failure) throw failure
      return { data: { repository: saved } }
    },
    async put(path, payload) {
      requests.push({ method: 'put', path, payload })
      return { data: { repository: saved } }
    },
  }
  const modules = {
    vue: { ...vue, onMounted() {}, onBeforeUnmount() {}, watch() {} },
    'vue-router': { useRoute: () => ({ query: {} }), useRouter: () => ({}) },
    'vue-i18n': { useI18n: () => ({ t: key => key, locale: vue.ref('zh-CN') }) },
    'ant-design-vue': { Modal: {}, message: { success: value => notifications.push(value), error: value => notifications.push(value) } },
    '@/stores/auth': { useAuthStore: () => ({ canAny: permissions => permissions.some(permission => permission === 'repository.create' ? repositoryCreate : permission === 'credential.create' ? credentialCreate : true) }) },
    '@/api/client': { __esModule: true, default: client },
    '@/api/resources': { apiErrorMessage: () => '请求失败' },
  }
  const module = { exports: {} }
  new Function('require', 'module', 'exports', outputText)(name => modules[name] || {}, module, module.exports)
  const state = module.exports.default.setup({ section }, { expose() {} })
  return { state, requests, notifications, saved, fail: error => { failure = error } }
}

test('从应用打开及取消创建仓库，保留全部应用草稿', () => {
  const { state } = formHarness()
  Object.assign(state.appForm, { name: 'draft_app', description: '应用说明', repository_id: 'old-repo', workflow_template_id: 'flow', poll_interval_seconds: 10 })
  const draft = { ...state.appForm }
  state.formOpen.value = true
  state.createApplicationRepository()
  assert.equal(state.formSection.value, 'repositories')
  state.repoForm.name = 'new_repo'
  state.closeForm()
  assert.equal(state.formSection.value, 'applications')
  assert.equal(state.formOpen.value, true)
  assert.deepEqual({ ...state.appForm }, draft)
})

test('编辑应用时创建仓库必须 POST 新仓库，不能把应用 ID 当仓库 ID', async () => {
  const { state, requests, saved } = formHarness()
  state.editingID.value = 'existing-application'
  state.formOpen.value = true
  Object.assign(state.appForm, { name: 'draft_app', description: '未保存的说明', repository_id: 'old-repo' })
  state.createApplicationRepository()
  state.repoForm.name = saved.name
  state.repoForm.clone_url = 'https://example.com/team/repo.git'
  await state.save()
  assert.equal(requests.length, 1)
  assert.equal(requests[0].method, 'post')
  assert.equal(requests[0].path, '/repositories')
  assert.equal(state.editingID.value, 'existing-application')
  assert.equal(state.appForm.repository_id, saved.id)
  assert.equal(state.appForm.name, 'draft_app')
  assert.equal(state.appForm.description, '未保存的说明')
  assert.equal(state.repositoryOptions.value[0].value, saved.id)
  assert.equal(state.formSection.value, 'applications')
  assert.equal(state.formOpen.value, true)
})

test('仓库创建失败保留仓库输入和应用草稿，允许返回应用', async () => {
  const { state, fail } = formHarness()
  state.appForm.name = 'draft_app'
  state.createApplicationRepository()
  state.repoForm.name = 'new_repo'
  fail(new Error('test failure'))
  await state.save()
  assert.equal(state.formSection.value, 'repositories')
  assert.equal(state.repoForm.name, 'new_repo')
  assert.equal(state.appForm.name, 'draft_app')
  assert.equal(state.saving.value, false)
  state.closeForm()
  assert.equal(state.formSection.value, 'applications')
})

test('缺少仓库创建权限时不能进入或提交仓库子表单', async () => {
  const { state, requests } = formHarness({ repositoryCreate: false })
  state.createApplicationRepository()
  assert.equal(state.creatingApplicationRepository.value, false)
  state.creatingApplicationRepository.value = true
  assert.equal(state.canSaveCurrent.value, false)
  await state.save()
  assert.equal(requests.length, 0)
})

test('Git 凭据加号继承仓库平台和认证类型，保存后只回填 Git 凭据', () => {
  const { state } = formHarness()
  state.appForm.name = 'draft_app'
  state.createApplicationRepository()
  Object.assign(state.repoForm, { name: 'repo_draft', provider: 'gitlab', auth_type: 'ssh_key', clone_url: 'git@example.com:team/app.git', api_credential_id: 'existing-api' })
  state.createRepositoryCredential('git')
  assert.deepEqual({ ...state.credentialCreation }, { open: true, target: 'git', provider: 'gitlab', authType: 'ssh_key' })
  state.repositoryCredentialSaved({ id: 'ssh-credential', name: 'ssh', provider: 'gitlab', auth_type: 'ssh_key', secret_hint: 'configured' })
  assert.equal(state.repoForm.credential_id, 'ssh-credential')
  assert.equal(state.repoForm.api_credential_id, 'existing-api')
  assert.equal(state.cloneCredentialOptions.value[0].value, 'ssh-credential')
  assert.equal(state.repoForm.name, 'repo_draft')
  assert.equal(state.appForm.name, 'draft_app')
})

test('平台 API 加号仅创建 Token，保存后不替换 Git SSH 凭据', () => {
  const { state } = formHarness({ section: 'repositories' })
  Object.assign(state.repoForm, { provider: 'gitea', auth_type: 'ssh_key', credential_id: 'existing-ssh' })
  state.createRepositoryCredential('api')
  assert.equal(state.credentialCreation.authType, 'token')
  state.repositoryCredentialSaved({ id: 'api-token', name: 'api', provider: 'gitea', auth_type: 'token', secret_hint: 'configured' })
  assert.equal(state.repoForm.api_credential_id, 'api-token')
  assert.equal(state.repoForm.credential_id, 'existing-ssh')
  assert.equal(state.apiCredentialOptions.value[0].value, 'api-token')
})

test('取消凭据创建不改变仓库选择，仓库上下文变化后不回填过时凭据', () => {
  const { state } = formHarness({ section: 'repositories' })
  Object.assign(state.repoForm, { auth_type: 'token', credential_id: 'original' })
  state.createRepositoryCredential('git')
  state.closeForm()
  assert.equal(state.credentialCreation.open, false)
  assert.equal(state.repoForm.credential_id, 'original')
  state.createRepositoryCredential('git')
  state.repoForm.provider = 'gitlab'
  state.repositoryCredentialSaved({ id: 'old-context', name: 'api', provider: 'github', auth_type: 'token', secret_hint: '' })
  assert.equal(state.repoForm.credential_id, 'original')
})

test('没有凭据创建权限时不能打开加号表单', () => {
  const { state } = formHarness({ section: 'repositories', credentialCreate: false })
  state.repoForm.auth_type = 'token'
  state.createRepositoryCredential('git')
  assert.equal(state.credentialCreation.open, false)
  state.createRepositoryCredential('api')
  assert.equal(state.credentialCreation.open, false)
})
