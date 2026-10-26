import assert from 'node:assert/strict'
import { test } from 'node:test'
import * as vue from 'vue'
import { componentHarness } from './acceptanceHarness.mjs'

const providers = [
  { code: 'cloudflare', name: 'Cloudflare', fields: [{ key: 'api_token', secret: true }] },
  { code: 'alidns', name: '阿里云 DNS', fields: [{ key: 'access_key_id' }, { key: 'access_key_secret', secret: true }] },
  { code: 'tencentcloud', name: '腾讯云 DNSPod', fields: [{ key: 'secret_key', secret: true, default: 'synthetic-secret-default' }, { key: 'region', default: 'ap-guangzhou' }] },
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

test('分类写入访问历史并保留多选，进入单一域名列表时清理页签参数', async t => {
  const h = view(t, { query: { provider: 'cloudflare', account: ['cf-a', 'cf-b'], view: 'accounts', keep: 'yes' } })
  assert.deepEqual(h.state.scopeAccounts.value, ['cf-a', 'cf-b'])
  await h.state.selectScope('alidns', ['ali-a'])
  assert.deepEqual(h.replacements.at(-1), { query: { provider: 'alidns', account: ['ali-a'], view: undefined, keep: 'yes' } })
  await h.state.selectScope('cloudflare', [])
  assert.equal(h.replacements.at(-1).query.account, '')
  await h.state.selectScope('', ['*'])
  assert.equal(h.replacements.at(-1).query.provider, undefined)
  assert.equal(h.replacements.at(-1).query.account, undefined)
  assert.equal(h.requests.length, 0)
})

test('添加入口先选择支持厂商，域名表单内新增账号带入当前厂商', t => {
  const h = view(t, { query: { provider: 'alidns' } })
  h.state.newAccount()
  assert.equal(h.state.accountForm.provider, '')
  assert.deepEqual(h.state.accountForm.config, {})
  assert.deepEqual(h.state.providerChoices.value.map(item => item.code), ['cloudflare', 'alidns', 'tencentcloud'])
  h.state.providerSearch.value = '阿里'
  assert.deepEqual(h.state.providerChoices.value.map(item => item.code), ['alidns'])
  h.state.changeProvider('tencentcloud')
  assert.equal(h.state.accountForm.provider, 'tencentcloud')
  assert.equal(h.state.accountForm.config.region, 'ap-guangzhou')
  assert.equal(h.state.accountForm.config.secret_key, '')
  h.state.newAccount(true)
  assert.equal(h.state.accountForm.provider, 'alidns')
  assert.equal(h.state.accountForm.config.access_key_secret, '')
  assert.equal(h.state.accountForm.config.region, undefined)
  assert.equal(h.state.providerSearch.value, '')
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

test('分类树只展示已配置厂商，搜索不会显示空厂商，停用账号仍保留', t => {
  const h = tree(t)
  assert.equal(h.state.branches.value.some(item => item.key === 'provider:tencentcloud'), false)
  h.state.search.value = '腾讯'
  assert.equal(h.state.branches.value.length, 0)
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

test('未保存账号不产生左侧分类，保存后出现厂商，删除最后账号后移除分类', async t => {
  const h = tree(t, { accounts: [], provider: '', selected: ['*'] })
  assert.deepEqual(h.state.treeData.value, [])
  assert.deepEqual(h.state.catalog.value, [])
  h.props.accounts = [{ id: 'new-ali', provider: 'alidns', name: '阿里云', is_active: true }]
  await settle()
  assert.deepEqual(h.state.catalog.value.map(item => item.code), ['alidns'])
  assert.deepEqual(h.state.treeData.value.map(item => item.key), ['all', 'provider:alidns'])
  h.props.accounts = []
  await settle()
  assert.deepEqual(h.state.treeData.value, [])
})

test('刷新后失效厂商范围使用替换历史恢复全部，清理页签并保留其他参数', async t => {
  const h = view(t, { query: { provider: 'tencentcloud', account: 'deleted', view: 'accounts', keep: 'yes' } })
  await h.state.refresh()
  assert.deepEqual(h.replacements.at(-1).query, { provider: undefined, account: undefined, view: undefined, keep: 'yes' })
})

test('刷新包含页签参数的链接仍保留有效厂商和账号选择', async t => {
  const h = view(t, { query: { provider: 'cloudflare', account: ['cf-a', 'cf-b'], view: 'accounts' } })
  await h.state.refresh()
  assert.deepEqual(h.replacements.at(-1).query, { provider: 'cloudflare', account: ['cf-a', 'cf-b'], view: undefined })
  assert.deepEqual(h.state.scopedDomains.value.map(item => item.id), ['domain-cf-a', 'domain-cf-b'])
})

test('左侧账号管理入口按操作权限分开控制，不对失效或加载中账号发出操作', t => {
  const h = tree(t, { canUpdate: true, canDelete: false })
  h.state.manageAccount('cf-a', 'edit')
  assert.deepEqual(h.events.at(-1), { name: 'manage', args: ['cf-a', 'edit'] })
  h.state.manageAccount('cf-a', 'remove')
  h.state.manageAccount('missing', 'edit')
  assert.equal(h.events.length, 1)
  h.props.canUpdate = false
  h.props.canDelete = true
  h.state.manageAccount('cf-a', 'toggle')
  assert.equal(h.events.length, 1)
  h.state.manageAccount('cf-a', 'remove')
  assert.deepEqual(h.events.at(-1), { name: 'manage', args: ['cf-a', 'remove'] })
  h.props.loading = true
  h.state.manageAccount('cf-a', 'remove')
  assert.equal(h.events.length, 2)
})

test('从左侧管理账号直接打开编辑，不改变域名范围，缺少权限不打开表单', t => {
  const h = view(t, { query: { provider: 'cloudflare', account: 'cf-a' } })
  h.state.manageAccount('ali-a', 'edit')
  assert.equal(h.state.accountOpen.value, true)
  assert.equal(h.state.editingAccountID.value, 'ali-a')
  assert.equal(h.state.accountForm.provider, 'alidns')
  assert.equal(h.state.accountForm.config.access_key_secret, '')
  assert.equal(h.replacements.length, 0)
  const readOnly = view(t, { codes: ['dns.read'] })
  readOnly.state.manageAccount('ali-a', 'edit')
  assert.equal(readOnly.state.accountOpen.value, false)
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

test('树节点选择支持全部已配置厂商、单个厂商与多个账号', t => {
  const h = tree(t)
  h.state.selectNode([], { node: { key: 'provider:alidns' } })
  assert.deepEqual(h.events.at(-1).args, ['alidns', ['*']])
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
  h.state.changeProvider('alidns')
  await h.state.saveAccount()
  assert.deepEqual(h.replacements.at(-1).query, { provider: 'alidns', account: ['new'], view: undefined })
  const failed = view(t, { api: { post: async () => { throw new Error('账号保存失败') } } })
  failed.state.newAccount()
  failed.state.changeProvider('cloudflare')
  failed.state.accountForm.config.api_token = 'synthetic-draft'
  await failed.state.saveAccount()
  assert.equal(failed.state.accountOpen.value, true)
  assert.equal(failed.state.accountForm.config.api_token, 'synthetic-draft')
  assert.equal(failed.state.saving.value, false)
})
