import assert from 'node:assert/strict'
import { test } from 'node:test'
import axios from 'axios'
import { loadFrontend, componentHarness } from './acceptanceHarness.mjs'

function harness() {
  let project = 'project-a'
  const requests = []
  const module = loadFrontend('../src/api/client.ts', { axios: { __esModule: true, default: axios } }, {
    localStorage: { getItem: key => key === 'edo.project' ? project : null },
  })
  const client = module.default
  client.defaults.adapter = async config => {
    requests.push(config)
    const data = config.url === '/auth/me' ? { user: { id: 'member', project_id: config.headers.get('X-EDO-Project-ID'), permissions: ['workflow.update'] } } : {}
    return { data, status: 200, statusText: 'OK', headers: {}, config }
  }
  return { module, client, requests, select: value => { project = value } }
}

test('普通请求使用当前项目，显式固定项目头与空项目头不会被选择器覆盖', async () => {
  const h = harness()
  await h.client.get('/applications')
  assert.equal(h.requests[0].headers.get('X-EDO-Project-ID'), 'project-a')
  await h.client.post('/delivery-variables', {}, { headers: { 'x-edo-project-id': 'project-a' } })
  h.select('')
  await h.client.get('/delivery-variables', { headers: { 'X-EDO-Project-ID': '' } })
  assert.equal(h.requests[1].headers.get('X-EDO-Project-ID'), 'project-a')
  assert.equal(h.requests[2].headers.get('X-EDO-Project-ID'), '')
  h.select('project-b')
  await assert.rejects(h.client.post('/delivery-variables', {}, { headers: { 'X-EDO-Project-ID': 'project-a' } }), { code: 'EDO_PROJECT_CHANGED' })
  assert.equal(h.requests.length, 3)
})

test('旧项目业务响应晚到时不能回写到新项目，包含成功和失败响应', async () => {
  for (const success of [true, false]) {
    const h = harness()
    await h.module.getCurrentUser()
    let finish
    h.client.defaults.adapter = config => new Promise((resolve, reject) => { finish = () => success ? resolve({ data: { applications: [{ id: 'project-a-app' }] }, status: 200, headers: {}, statusText: 'OK', config }) : reject(new axios.AxiosError('权限不足', 'ERR_BAD_REQUEST', config)) })
    const pending = h.client.get('/applications')
    await new Promise(done => setImmediate(done))
    h.select('project-b'); finish()
    await assert.rejects(pending, { code: 'EDO_PROJECT_CHANGED' })
  }
})

test('已获取项目权限后切项目，所有旧页面业务读写均不能发往新项目', async () => {
  const h = harness()
  await h.module.getCurrentUser()
  h.select('project-b')
  for (const method of ['get', 'post', 'put', 'patch', 'delete']) {
    await assert.rejects(h.client[method]('/applications', {}), error => error.code === 'EDO_PROJECT_CHANGED')
  }
  assert.equal(h.requests.length, 1)
  await h.client.get('/projects')
  await h.module.getCurrentUser()
  await h.client.post('/applications', {})
  assert.equal(h.requests.at(-1).headers.get('X-EDO-Project-ID'), 'project-b')
})

test('固定到其他项目的请求不能借用当前项目已加载权限', async () => {
  const h = harness()
  await h.module.getCurrentUser()
  await assert.rejects(h.client.put('/workflow-templates/template', {}, { headers: { 'X-EDO-Project-ID': 'project-b' } }), { code: 'EDO_PROJECT_CHANGED' })
  assert.equal(h.requests.length, 1)
})

test('旧项目身份响应晚到时拒绝使用，不覆盖当前项目授权上下文', async () => {
  const h = harness()
  let resolve
  h.client.defaults.adapter = config => new Promise(done => { resolve = () => done({ data: { user: { id: 'member', project_id: 'project-a' } }, status: 200, statusText: 'OK', headers: {}, config }) })
  const old = h.module.getCurrentUser()
  await new Promise(done => setImmediate(done))
  h.select('project-b'); resolve()
  await assert.rejects(old, { code: 'EDO_PROJECT_CHANGED' })
})

test('项目切换错误提供可恢复提示，不能误报网络不通', async () => {
  const h = harness()
  await h.module.getCurrentUser(); h.select('project-b')
  const resources = loadFrontend('../src/api/resources.ts', { axios: { __esModule: true, default: axios }, '@/api/client': { __esModule: true, default: h.client } })
  await assert.rejects(h.client.post('/build-plans', {}), error => resources.apiErrorMessage(error) === '项目已切换，请刷新页面后重试')
})

test('当前项目失效后的自动选择也重建整个页面，不能把旧表单留给新项目', async t => {
  const storage = new Map([['edo.project', 'project-a']])
  let profileRequests = 0
  const h = componentHarness('../src/components/ProjectSelector.vue', {
    codes: ['application.create'],
    modules: { '@/api/client': { __esModule: true, default: { get: async () => ({ data: { projects: [{ id: 'project-b', name: '项目 B', is_active: true }] } }) }, getCurrentUser: async () => { profileRequests++; return { id: 'operator', project_id: 'project-b' } } } },
    globals: { localStorage: { getItem: key => storage.get(key), setItem: (key, value) => storage.set(key, value), removeItem: key => storage.delete(key) } },
  })
  t.after(h.stop)
  h.auth.user.project_id = 'project-a'
  await h.state.reload()
  assert.equal(storage.get('edo.project'), 'project-b')
  assert.deepEqual(h.locations, ['/projects'])
  assert.equal(profileRequests, 0)
  assert.equal(h.auth.user.project_id, 'project-a')
})
