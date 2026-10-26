import assert from 'node:assert/strict'
import { test } from 'node:test'
import * as vue from 'vue'
import { componentHarness } from './acceptanceHarness.mjs'

const providers = [
  { code: 'cloudflare', name: 'Cloudflare', fields: [{ key: 'api_token', secret: true }] },
  { code: 'alidns', name: '阿里云 DNS', fields: [{ key: 'region', default: 'cn-hangzhou' }, { key: 'access_key_secret', secret: true }] },
  { code: 'tencentcloud', name: '腾讯云 DNSPod', fields: [] },
]
const accounts = [
  { id: 'cf-a', provider: 'cloudflare', name: '生产账号', is_active: true },
  { id: 'cf-b', provider: 'cloudflare', name: '测试账号', is_active: true },
  { id: 'cf-old', provider: 'cloudflare', name: '已停用账号', is_active: false },
  { id: 'ali-a', provider: 'alidns', name: '生产账号', is_active: true },
]
const domains = accounts.map(account => ({ id: `domain-${account.id}`, provider: account.provider, account_id: account.id, name: `${account.id}.example.com` }))
const getData = path => ({ data: path === '/dns/providers' ? { providers } : path === '/dns/accounts' ? { accounts } : { domains } })
const settle = async () => { await vue.nextTick(); await new Promise(resolve => setImmediate(resolve)) }
function view(t, options = {}) {
  const h = componentHarness('../src/views/DomainView.vue', { codes: ['dns.read', 'dns.create', 'dns.update'], ...options, api: { get: async path => getData(path), ...options.api } })
  h.state.providers.value = structuredClone(providers)
  h.state.accounts.value = structuredClone(accounts)
  h.state.domains.value = structuredClone(domains)
  t.after(h.stop)
  return h
}
function tree(t, options = {}) {
  const props = vue.reactive({ providers: structuredClone(providers), accounts: structuredClone(accounts), provider: 'cloudflare', selected: ['*'], ...options })
  const h = componentHarness('../src/components/DNSProviderTree.vue', { props })
  t.after(h.stop)
  return { ...h, props }
}

test('厂商与账号范围同时筛选域名和账号，使用账号 ID 区分同名账号', t => {
  const h = view(t)
  assert.equal(h.state.scopedAccounts.value.length, 4)
  h.route.query = { provider: 'alidns' }
  assert.deepEqual(h.state.scopedAccounts.value.map(item => item.id), ['ali-a'])
  assert.deepEqual(h.state.scopedDomains.value.map(item => item.id), ['domain-ali-a'])
  h.route.query = { provider: 'cloudflare', account: ['cf-a', 'cf-b', 'cf-a'] }
  assert.deepEqual(h.state.scopeAccounts.value, ['cf-a', 'cf-b'])
  assert.deepEqual(h.state.scopedDomains.value.map(item => item.id), ['domain-cf-a', 'domain-cf-b'])
  h.route.query.account = ''
  assert.equal(h.state.scopedAccounts.value.length, 0)
  assert.equal(h.state.scopedDomains.value.length, 0)
})

test('分类和页签写入访问历史，刷新或返回可从 URL 恢复多选，不额外请求接口', async t => {
  const h = view(t, { query: { provider: 'cloudflare', account: ['cf-a', 'cf-b'], view: 'accounts', keep: 'yes' } })
  assert.equal(h.state.tab.value, 'accounts')
  await h.state.selectScope('alidns', ['ali-a'])
  assert.deepEqual(h.replacements.at(-1), { query: { provider: 'alidns', account: ['ali-a'], view: 'accounts', keep: 'yes' } })
  h.state.tab.value = 'domains'
  assert.deepEqual(h.replacements.at(-1).query.account, ['cf-a', 'cf-b'])
  assert.equal(h.replacements.at(-1).query.view, undefined)
  await h.state.selectScope('cloudflare', [])
  assert.equal(h.replacements.at(-1).query.account, '')
  await h.state.selectScope('', ['*'])
  assert.equal(h.replacements.at(-1).query.provider, undefined)
  assert.equal(h.replacements.at(-1).query.account, undefined)
  assert.equal(h.requests.length, 0)
})

test('新增账号带入当前厂商，域名优先使用所选范围内的启用账号', t => {
  const h = view(t, { query: { provider: 'alidns' } })
  h.state.newAccount()
  assert.equal(h.state.accountForm.provider, 'alidns')
  assert.equal(h.state.accountForm.config.region, 'cn-hangzhou')
  h.state.newDomain()
  assert.equal(h.state.domainForm.account_id, 'ali-a')
  h.route.query = { provider: 'cloudflare', account: 'cf-b' }
  h.state.newDomain()
  assert.equal(h.state.domainForm.account_id, 'cf-b')
  h.route.query.account = 'cf-old'
  h.state.newDomain()
  assert.equal(h.state.domainForm.account_id, '')
  assert.deepEqual(h.state.domainAccounts.value.map(item => item.id), ['cf-a', 'cf-b'])
})

test('筛选变化关闭域名详情，迟到的解析响应不能填入新范围', async t => {
  let finish
  const h = view(t, { api: { get: () => new Promise(resolve => { finish = resolve }) } })
  h.state.selectedID.value = 'domain-cf-a'
  h.state.detailOpen.value = true
  await settle()
  h.route.query = { provider: 'alidns' }
  await settle()
  finish({ data: { records: [{ id: 'old-record' }] } })
  await settle()
  assert.equal(h.state.detailOpen.value, false)
  assert.deepEqual(h.state.records.value, [])
})

test('刷新只接受最新响应，旧响应不会重置厂商或账号列表', async t => {
  const pending = []
  const h = view(t, { query: { provider: 'alidns' }, api: { get: path => new Promise(resolve => pending.push({ path, resolve })) } })
  const old = h.state.refresh(), latest = h.state.refresh()
  for (const item of pending.slice(3)) item.resolve(getData(item.path))
  await latest
  for (const item of pending.slice(0, 3)) item.resolve({ data: { providers: [], accounts: [], domains: [] } })
  await old
  assert.equal(h.state.providers.value.length, 3)
  assert.deepEqual(h.state.scopedAccounts.value.map(item => item.id), ['ali-a'])
  assert.equal(h.state.loading.value, false)
})

test('分类树搜索按厂商和账号名称匹配，保留空厂商及停用账号', t => {
  const h = tree(t)
  assert.equal(h.state.branches.value.find(item => item.key === 'provider:tencentcloud').count, 0)
  h.state.search.value = '阿里'
  assert.deepEqual(h.state.branches.value.map(item => item.key), ['provider:alidns'])
  h.state.search.value = '生产账号'
  assert.deepEqual(h.state.branches.value.map(item => item.key), ['provider:cloudflare', 'provider:alidns'])
  assert.equal(h.state.branches.value[0].count, 3)
  h.state.search.value = '已停用'
  assert.equal(h.state.branches.value[0].children[0].inactive, true)
  h.state.search.value = '不存在'
  assert.equal(h.state.branches.value.length, 0)
})

test('搜索时取消一个账号仍保留未显示账号，半选基于完整厂商范围', t => {
  const h = tree(t)
  h.state.search.value = '生产账号'
  h.state.checkNode([], { node: { key: 'account:cf-a' }, checked: false })
  assert.deepEqual(h.events.at(-1), { name: 'selection', args: ['cloudflare', ['cf-b', 'cf-old']] })
  h.props.selected = ['cf-b']
  assert.deepEqual(h.state.checkedKeys.value, { checked: ['account:cf-b'], halfChecked: ['provider:cloudflare'] })
  h.state.checkNode([], { node: { key: 'provider:cloudflare' }, checked: true })
  assert.deepEqual(h.events.at(-1).args, ['cloudflare', ['*']])
})

test('树节点选择支持全部厂商、单个厂商与多个账号，空厂商也可选中', t => {
  const h = tree(t)
  h.state.selectNode([], { node: { key: 'provider:tencentcloud' } })
  assert.deepEqual(h.events.at(-1).args, ['tencentcloud', ['*']])
  h.state.checkNode([], { node: { key: 'account:ali-a' }, checked: true })
  assert.deepEqual(h.events.at(-1).args, ['alidns', ['ali-a']])
  h.state.selectNode([], { node: { key: 'all' } })
  assert.deepEqual(h.events.at(-1).args, ['', ['*']])
  h.state.checkNode([], { node: { key: 'provider:cloudflare' }, checked: false })
  assert.deepEqual(h.events.at(-1).args, ['cloudflare', []])
})

test('域名表单内新增账号只回填账号，不清空域名草稿或自动保存域名', async t => {
  const created = { id: 'new-cf', provider: 'cloudflare', name: '新账号', is_active: true }
  const h = view(t, { query: { provider: 'alidns' }, api: { post: async () => ({ data: { account: created } }), get: async path => path === '/dns/accounts' ? { data: { accounts: [...accounts, created] } } : getData(path) } })
  h.state.newDomain()
  h.state.domainForm.name = 'draft.example.com'
  h.state.newAccount(true)
  h.state.accountForm.name = created.name
  h.state.accountForm.config.access_key_secret = 'synthetic-secret'
  await h.state.saveAccount()
  assert.equal(h.state.domainForm.name, 'draft.example.com')
  assert.equal(h.state.domainForm.account_id, created.id)
  assert.equal(h.state.domainAccounts.value.find(item => item.id === created.id)?.name, created.name)
  assert.equal(h.state.domainOpen.value, true)
  assert.equal(h.requests.filter(item => item.method !== 'get').length, 1)
  assert.equal(h.requests.find(item => item.method === 'post').path, '/dns/accounts')
  assert.deepEqual(h.state.accountForm.config, {})
  assert.equal(h.replacements.length, 0)
})

test('保存当前范围外的域名后定位其账号分类，范围内保存不产生额外导航', async t => {
  const domain = domains.find(item => item.account_id === 'cf-a')
  const h = view(t, { query: { provider: 'alidns', account: 'ali-a' }, api: { post: async () => ({ data: { domain } }) } })
  await h.state.saveDomain()
  assert.deepEqual(h.replacements.at(-1).query, { provider: 'cloudflare', account: ['cf-a'], view: undefined })
  const inScope = view(t, { query: { provider: 'cloudflare' }, api: { post: async () => ({ data: { domain } }) } })
  await inScope.state.saveDomain()
  assert.equal(inScope.replacements.length, 0)
})

test('新账号保存成功后定位账号分类，失败保留输入以便修改', async t => {
  const h = view(t, { query: { provider: 'alidns' }, api: { post: async () => ({ data: { account: { id: 'new', provider: 'alidns' } } }) } })
  h.state.newAccount()
  await h.state.saveAccount()
  assert.deepEqual(h.replacements.at(-1).query, { provider: 'alidns', account: ['new'], view: 'accounts' })
  const failed = view(t, { api: { post: async () => { throw new Error('账号保存失败') } } })
  failed.state.newAccount()
  failed.state.accountForm.config.api_token = 'synthetic-draft'
  await failed.state.saveAccount()
  assert.equal(failed.state.accountOpen.value, true)
  assert.equal(failed.state.accountForm.config.api_token, 'synthetic-draft')
  assert.equal(failed.state.saving.value, false)
})
