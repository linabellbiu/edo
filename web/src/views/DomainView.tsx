import { useCallback, useEffect, useMemo, useState } from 'react'

import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'

interface ProviderField {
  key: string
  label: string
  secret: boolean
  required: boolean
  default?: string
  placeholder?: string
  help?: string
  multiline?: boolean
}

interface DNSProvider {
  code: string
  name: string
  description: string
  fields: ProviderField[]
}

interface DNSAccount {
  id: string
  name: string
  provider: string
  config: Record<string, string>
  configured_secret_fields: string[]
  is_active: boolean
  updated_at: string
}

interface Domain {
  id: string
  account_id: string
  account_name: string
  provider: string
  name: string
  description: string
  is_active: boolean
  updated_at: string
}

interface DNSRecord {
  id: string
  name: string
  fqdn: string
  type: string
  value: string
  ttl: number
  read_only: boolean
}

type Tab = 'domains' | 'accounts'

const recordTypes = ['A', 'AAAA', 'CNAME', 'MX', 'TXT', 'CAA', 'SRV', 'NS', 'PTR', 'HTTPS', 'SVCB', 'TLSA', 'URI', 'NAPTR', 'SSHFP']
const emptyRecord = { name: '', type: 'A', value: '', ttl: 300 }

function providerShortName(name: string) {
  const ascii = name.match(/[A-Za-z0-9]+/g)?.join('')
  return (ascii || name).slice(0, 2).toUpperCase()
}

function ttlLabel(ttl: number) {
  if (ttl === 1) return '自动'
  if (ttl % 86400 === 0) return `${ttl / 86400} 天`
  if (ttl % 3600 === 0) return `${ttl / 3600} 小时`
  if (ttl % 60 === 0) return `${ttl / 60} 分钟`
  return `${ttl} 秒`
}

export default function DomainView() {
  const user = useAuthStore((state) => state.user)
  const canManage = Boolean(user?.is_superuser || user?.permissions.includes('dns.manage'))
  const [tab, setTab] = useState<Tab>('domains')
  const [providers, setProviders] = useState<DNSProvider[]>([])
  const [accounts, setAccounts] = useState<DNSAccount[]>([])
  const [domains, setDomains] = useState<Domain[]>([])
  const [records, setRecords] = useState<DNSRecord[]>([])
  const [selectedDomainID, setSelectedDomainID] = useState('')
  const [query, setQuery] = useState('')
  const [loading, setLoading] = useState(true)
  const [recordsLoading, setRecordsLoading] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [testingID, setTestingID] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')

  const [accountFormOpen, setAccountFormOpen] = useState(false)
  const [editingAccountID, setEditingAccountID] = useState('')
  const [accountName, setAccountName] = useState('')
  const [accountProvider, setAccountProvider] = useState('cloudflare')
  const [accountConfig, setAccountConfig] = useState<Record<string, string>>({})

  const [domainFormOpen, setDomainFormOpen] = useState(false)
  const [editingDomainID, setEditingDomainID] = useState('')
  const [domainForm, setDomainForm] = useState({ account_id: '', name: '', description: '' })

  const [recordFormOpen, setRecordFormOpen] = useState(false)
  const [editingRecordID, setEditingRecordID] = useState('')
  const [recordForm, setRecordForm] = useState(emptyRecord)

  const selectedDomain = domains.find((domain) => domain.id === selectedDomainID)
  const selectedProvider = providers.find((provider) => provider.code === accountProvider)

  const refresh = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const [providerResponse, accountResponse, domainResponse] = await Promise.all([
        client.get<{ providers: DNSProvider[] }>('/dns/providers'),
        client.get<{ accounts: DNSAccount[] }>('/dns/accounts'),
        client.get<{ domains: Domain[] }>('/dns/domains'),
      ])
      setProviders(providerResponse.data.providers)
      setAccounts(accountResponse.data.accounts)
      setDomains(domainResponse.data.domains)
      setAccountProvider((current) => current || providerResponse.data.providers[0]?.code || '')
      setSelectedDomainID((current) => domainResponse.data.domains.some((item) => item.id === current)
        ? current : domainResponse.data.domains[0]?.id || '')
      setDomainForm((current) => ({ ...current, account_id: current.account_id || accountResponse.data.accounts.find((item) => item.is_active)?.id || '' }))
    } catch (loadError) {
      setError(apiErrorMessage(loadError))
    } finally {
      setLoading(false)
    }
  }, [])

  const loadRecords = useCallback(async (domainID: string) => {
    if (!domainID) {
      setRecords([])
      return
    }
    setRecordsLoading(true)
    setError('')
    try {
      const response = await client.get<{ records: DNSRecord[] }>(`/dns/domains/${domainID}/records`, { timeout: 35_000 })
      setRecords(response.data.records)
    } catch (loadError) {
      setRecords([])
      setError(apiErrorMessage(loadError))
    } finally {
      setRecordsLoading(false)
    }
  }, [])

  useEffect(() => { void refresh() }, [refresh])
  useEffect(() => {
    const domain = domains.find((item) => item.id === selectedDomainID)
    if (domain?.is_active) void loadRecords(selectedDomainID)
    else setRecords([])
  }, [domains, loadRecords, selectedDomainID])

  const filteredRecords = useMemo(() => {
    const normalized = query.trim().toLowerCase()
    if (!normalized) return records
    return records.filter((record) => `${record.name} ${record.fqdn} ${record.type} ${record.value}`.toLowerCase().includes(normalized))
  }, [query, records])

  function clearMessages() {
    setError('')
    setNotice('')
  }

  function openNewAccount() {
    const provider = providers[0]
    setEditingAccountID('')
    setAccountName('')
    setAccountProvider(provider?.code || '')
    setAccountConfig(Object.fromEntries((provider?.fields || []).filter((field) => field.default).map((field) => [field.key, field.default || ''])))
    setAccountFormOpen(true)
    setTab('accounts')
    clearMessages()
  }

  function editAccount(account: DNSAccount) {
    const provider = providers.find((item) => item.code === account.provider)
    const config = { ...account.config }
    provider?.fields.forEach((field) => {
      if (!(field.key in config)) config[field.key] = field.default || ''
    })
    setEditingAccountID(account.id)
    setAccountName(account.name)
    setAccountProvider(account.provider)
    setAccountConfig(config)
    setAccountFormOpen(true)
    clearMessages()
  }

  function closeAccountForm() {
    setAccountFormOpen(false)
    setEditingAccountID('')
  }

  function changeAccountProvider(code: string) {
    const provider = providers.find((item) => item.code === code)
    setAccountProvider(code)
    setAccountConfig(Object.fromEntries((provider?.fields || []).map((field) => [field.key, field.default || ''])))
  }

  async function submitAccount(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setSubmitting(true)
    clearMessages()
    try {
      const wasEditing = Boolean(editingAccountID)
      const payload = { name: accountName, provider: accountProvider, config: accountConfig, clear_secret_fields: [] }
      if (editingAccountID) await client.put(`/dns/accounts/${editingAccountID}`, payload)
      else await client.post('/dns/accounts', payload)
      closeAccountForm()
      setNotice(wasEditing ? 'DNS 厂商账号已更新。' : 'DNS 厂商账号已保存。')
      await refresh()
    } catch (submitError) {
      setError(apiErrorMessage(submitError))
    } finally {
      setSubmitting(false)
    }
  }

  async function toggleAccount(account: DNSAccount) {
    clearMessages()
    try {
      await client.patch(`/dns/accounts/${account.id}/status`, { active: !account.is_active })
      await refresh()
    } catch (actionError) { setError(apiErrorMessage(actionError)) }
  }

  async function removeAccount(account: DNSAccount) {
    if (!window.confirm(`确认删除 DNS 厂商账号“${account.name}”？使用中的账号不能删除。`)) return
    clearMessages()
    try {
      await client.delete(`/dns/accounts/${account.id}`)
      await refresh()
    } catch (actionError) { setError(apiErrorMessage(actionError)) }
  }

  function openNewDomain() {
    setEditingDomainID('')
    setDomainForm({ account_id: accounts.find((item) => item.is_active)?.id || '', name: '', description: '' })
    setDomainFormOpen(true)
    setTab('domains')
    clearMessages()
  }

  function editDomain(domain: Domain) {
    setEditingDomainID(domain.id)
    setDomainForm({ account_id: domain.account_id, name: domain.name, description: domain.description })
    setDomainFormOpen(true)
    clearMessages()
  }

  async function submitDomain(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setSubmitting(true)
    clearMessages()
    try {
      const wasEditing = Boolean(editingDomainID)
      const response = editingDomainID
        ? await client.put<{ domain: Domain }>(`/dns/domains/${editingDomainID}`, domainForm)
        : await client.post<{ domain: Domain }>('/dns/domains', domainForm)
      setDomainFormOpen(false)
      setEditingDomainID('')
      setNotice(wasEditing ? '域名配置已更新。' : '域名已添加，现在可以读取解析记录。')
      await refresh()
      setSelectedDomainID(response.data.domain.id)
    } catch (submitError) {
      setError(apiErrorMessage(submitError))
    } finally {
      setSubmitting(false)
    }
  }

  async function testDomain(domain: Domain) {
    setTestingID(domain.id)
    clearMessages()
    try {
      const response = await client.post<{ reachable: boolean; record_count: number }>(`/dns/domains/${domain.id}/test`, undefined, { timeout: 35_000 })
      setNotice(`已连接 ${domain.name}，读取到 ${response.data.record_count} 条解析记录。`)
      if (domain.id === selectedDomainID) await loadRecords(domain.id)
    } catch (actionError) {
      setError(apiErrorMessage(actionError))
    } finally { setTestingID('') }
  }

  async function toggleDomain(domain: Domain) {
    clearMessages()
    try {
      await client.patch(`/dns/domains/${domain.id}/status`, { active: !domain.is_active })
      await refresh()
    } catch (actionError) { setError(apiErrorMessage(actionError)) }
  }

  async function removeDomain(domain: Domain) {
    if (!window.confirm(`确认从 ZRT 移除“${domain.name}”？厂商侧 Zone 和解析记录不会被删除。`)) return
    clearMessages()
    try {
      await client.delete(`/dns/domains/${domain.id}`)
      setNotice('域名引用已从 ZRT 移除，厂商侧数据未改变。')
      await refresh()
    } catch (actionError) { setError(apiErrorMessage(actionError)) }
  }

  function openNewRecord() {
    setEditingRecordID('')
    setRecordForm(emptyRecord)
    setRecordFormOpen(true)
    clearMessages()
  }

  function editRecord(record: DNSRecord) {
    setEditingRecordID(record.id)
    setRecordForm({ name: record.name, type: record.type, value: record.value, ttl: record.ttl })
    setRecordFormOpen(true)
    clearMessages()
  }

  async function submitRecord(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!selectedDomainID) return
    setSubmitting(true)
    clearMessages()
    try {
      const wasEditing = Boolean(editingRecordID)
      if (editingRecordID) await client.put(`/dns/domains/${selectedDomainID}/records/${editingRecordID}`, recordForm, { timeout: 35_000 })
      else await client.post(`/dns/domains/${selectedDomainID}/records`, recordForm, { timeout: 35_000 })
      setRecordFormOpen(false)
      setEditingRecordID('')
      setNotice(wasEditing ? '解析记录已更新。' : '解析记录已创建。')
      await loadRecords(selectedDomainID)
    } catch (submitError) {
      setError(apiErrorMessage(submitError))
    } finally { setSubmitting(false) }
  }

  async function removeRecord(record: DNSRecord) {
    if (!selectedDomainID || !window.confirm(`确认删除 ${record.fqdn} 的 ${record.type} 记录？此操作会立即修改厂商侧 DNS。`)) return
    clearMessages()
    try {
      await client.delete(`/dns/domains/${selectedDomainID}/records/${record.id}`, { timeout: 35_000 })
      setNotice('解析记录已删除。')
      await loadRecords(selectedDomainID)
    } catch (actionError) { setError(apiErrorMessage(actionError)) }
  }

  return <section className="domain-page page-enter">
    <div className="page-heading modern-heading">
      <div><span className="section-label">MULTI-PROVIDER DNS</span><h2>域名解析</h2><p>统一管理不同 DNS 厂商的权威 Zone 和解析记录，所有凭据加密保存、变更写入审计日志。</p></div>
      <div className="heading-actions"><button className="icon-button" type="button" onClick={() => void refresh()} disabled={loading}>↻</button>{canManage && <button className="primary-button" type="button" onClick={tab === 'accounts' ? openNewAccount : openNewDomain}>{tab === 'accounts' ? '＋ 厂商账号' : '＋ 添加域名'}</button>}</div>
    </div>

    <div className="summary-strip dns-summary"><div><strong>{domains.length}</strong><span>已接入域名</span></div><div><strong>{accounts.length}</strong><span>厂商账号</span></div><div><strong>{providers.length}</strong><span>支持入口</span></div><div><strong>{records.length}</strong><span>当前解析记录</span></div></div>
    <div className="tab-bar"><button className={tab === 'domains' ? 'active' : ''} type="button" onClick={() => setTab('domains')}>域名与解析</button><button className={tab === 'accounts' ? 'active' : ''} type="button" onClick={() => setTab('accounts')}>DNS 厂商账号</button></div>
    {error && <div className="form-alert error system-alert" role="alert">{error}</div>}
    {notice && <div className="form-alert success system-alert" role="status">{notice}</div>}

    {tab === 'accounts' && <>
      {accountFormOpen && <form className="create-sheet dns-account-sheet" onSubmit={(event) => void submitAccount(event)}>
        <div className="sheet-header"><div><h3>{editingAccountID ? '编辑 DNS 厂商账号' : '添加 DNS 厂商账号'}</h3><p>敏感字段只写不回显；编辑时留空会保留原凭据。</p></div><button type="button" onClick={closeAccountForm}>×</button></div>
        <div className="form-grid">
          <label>账号名称<input required maxLength={128} value={accountName} onChange={(event) => setAccountName(event.target.value)} placeholder="例如：生产 Cloudflare" /></label>
          <label>DNS 厂商<select disabled={Boolean(editingAccountID)} value={accountProvider} onChange={(event) => changeAccountProvider(event.target.value)}>{providers.map((provider) => <option value={provider.code} key={provider.code}>{provider.name}</option>)}</select></label>
          {selectedProvider?.fields.map((field) => <label className={field.multiline ? 'span-2' : ''} key={field.key}>{field.label}{field.multiline
            ? <textarea rows={6} required={field.required && !editingAccountID} value={accountConfig[field.key] || ''} onChange={(event) => setAccountConfig({ ...accountConfig, [field.key]: event.target.value })} placeholder={field.secret && editingAccountID ? '留空表示保持原凭据' : field.placeholder} />
            : <input type={field.secret ? 'password' : 'text'} required={field.required && (!field.secret || !editingAccountID)} value={accountConfig[field.key] || ''} onChange={(event) => setAccountConfig({ ...accountConfig, [field.key]: event.target.value })} placeholder={field.secret && editingAccountID ? '留空表示保持原凭据' : field.placeholder} />}{field.help && <small className="field-help">{field.help}</small>}</label>)}
        </div>
        <div className="form-actions"><button className="secondary-button" type="button" onClick={closeAccountForm}>取消</button><button className="primary-button" type="submit" disabled={submitting}>{submitting ? '保存中…' : '保存账号'}</button></div>
      </form>}
      <div className="dns-account-grid">{accounts.map((account) => {
        const provider = providers.find((item) => item.code === account.provider)
        return <article className="dns-account-card modern-card" key={account.id}>
          <div className="dns-account-head"><span className="dns-provider-mark">{providerShortName(provider?.name || account.provider)}</span><div><h3>{account.name}</h3><p>{provider?.name || account.provider}</p></div><span className={`status-pill ${account.is_active ? 'status-ready' : ''}`}>{account.is_active ? '已启用' : '已停用'}</span></div>
          <p className="dns-provider-description">{provider?.description}</p>
          <div className="dns-config-summary"><span>{account.configured_secret_fields.length} 个加密凭据</span><span>{Object.keys(account.config).length} 项公开配置</span></div>
          {canManage && <div className="card-actions"><button type="button" onClick={() => editAccount(account)}>编辑</button><button type="button" onClick={() => void toggleAccount(account)}>{account.is_active ? '停用' : '启用'}</button><button className="danger-action" type="button" onClick={() => void removeAccount(account)}>删除</button></div>}
        </article>
      })}{!loading && accounts.length === 0 && <div className="empty-state modern-empty"><span className="empty-icon">⌁</span><h3>还没有 DNS 厂商账号</h3><p>先添加具有最小解析权限的厂商凭据。</p></div>}</div>
    </>}

    {tab === 'domains' && <>
      {domainFormOpen && <form className="create-sheet" onSubmit={(event) => void submitDomain(event)}>
        <div className="sheet-header"><div><h3>{editingDomainID ? '编辑域名' : '添加已有域名'}</h3><p>ZRT 只接入已有权威 Zone，不会购买、续费或删除厂商侧域名。</p></div><button type="button" onClick={() => setDomainFormOpen(false)}>×</button></div>
        <div className="form-grid">
          <label>DNS 厂商账号<select required value={domainForm.account_id} onChange={(event) => setDomainForm({ ...domainForm, account_id: event.target.value })}><option value="">请选择账号</option>{accounts.map((account) => <option disabled={!account.is_active} value={account.id} key={account.id}>{account.name}{account.is_active ? '' : '（已停用）'}</option>)}</select></label>
          <label>Zone 域名<input required maxLength={253} value={domainForm.name} onChange={(event) => setDomainForm({ ...domainForm, name: event.target.value })} placeholder="example.com" /></label>
          <label className="span-2">说明<textarea rows={3} maxLength={512} value={domainForm.description} onChange={(event) => setDomainForm({ ...domainForm, description: event.target.value })} placeholder="业务归属、变更窗口或联系人" /></label>
        </div>
        <div className="form-actions"><button className="secondary-button" type="button" onClick={() => setDomainFormOpen(false)}>取消</button><button className="primary-button" type="submit" disabled={submitting}>{submitting ? '保存中…' : '保存域名'}</button></div>
      </form>}

      {domains.length > 0 && <div className="domain-workspace">
        <aside className="domain-sidebar"><div className="domain-sidebar-title"><strong>域名列表</strong><span>{domains.length}</span></div>{domains.map((domain) => <button className={domain.id === selectedDomainID ? 'active' : ''} type="button" key={domain.id} onClick={() => { setSelectedDomainID(domain.id); setRecordFormOpen(false) }}><span className="domain-state-dot" data-active={domain.is_active} /><span><strong>{domain.name}</strong><small>{domain.account_name}</small></span><i>›</i></button>)}</aside>
        <div className="dns-record-panel">{selectedDomain && <>
          <div className="dns-record-heading"><div><div className="dns-domain-title"><h3>{selectedDomain.name}</h3><span className={`status-pill ${selectedDomain.is_active ? 'status-ready' : ''}`}>{selectedDomain.is_active ? '已启用' : '已停用'}</span></div><p>{selectedDomain.description || `${selectedDomain.account_name} · ${selectedDomain.provider}`}</p></div><div className="heading-actions">{canManage && <><button className="secondary-button" type="button" disabled={testingID === selectedDomain.id || !selectedDomain.is_active} onClick={() => void testDomain(selectedDomain)}>{testingID === selectedDomain.id ? '检查中…' : '检查连接'}</button><button className="secondary-button" type="button" onClick={() => editDomain(selectedDomain)}>配置</button><button className="primary-button" type="button" disabled={!selectedDomain.is_active} onClick={openNewRecord}>＋ 解析记录</button></>}</div></div>
          {recordFormOpen && <form className="dns-record-form" onSubmit={(event) => void submitRecord(event)}><div className="dns-record-form-fields"><label>主机记录<input required disabled={Boolean(editingRecordID)} value={recordForm.name} onChange={(event) => setRecordForm({ ...recordForm, name: event.target.value })} placeholder="@、www 或 _acme-challenge" /></label><label>类型<select disabled={Boolean(editingRecordID)} value={recordForm.type} onChange={(event) => setRecordForm({ ...recordForm, type: event.target.value })}>{recordTypes.map((type) => <option value={type} key={type}>{type}</option>)}</select></label><label>记录值<input required value={recordForm.value} onChange={(event) => setRecordForm({ ...recordForm, value: event.target.value })} placeholder={recordForm.type === 'MX' ? '10 mail.example.com.' : '请输入记录值'} /></label><label>TTL（秒）<input required type="number" min={1} max={604800} value={recordForm.ttl} onChange={(event) => setRecordForm({ ...recordForm, ttl: Number(event.target.value) })} /></label></div><div className="form-actions"><button className="secondary-button" type="button" onClick={() => setRecordFormOpen(false)}>取消</button><button className="primary-button" type="submit" disabled={submitting}>{submitting ? '提交中…' : editingRecordID ? '更新记录' : '创建记录'}</button></div></form>}
          <div className="dns-record-toolbar"><label><span>⌕</span><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索主机、类型或记录值" /></label><span>{recordsLoading ? '正在读取厂商数据…' : `${filteredRecords.length} 条记录`}</span></div>
          <div className="dns-record-table-wrap"><table className="dns-record-table"><thead><tr><th>主机记录</th><th>类型</th><th>记录值</th><th>TTL</th><th>操作</th></tr></thead><tbody>{filteredRecords.map((record) => <tr key={record.id}><td><strong>{record.name}</strong><small>{record.fqdn}</small></td><td><span className={`dns-type dns-type-${record.type.toLowerCase()}`}>{record.type}</span></td><td><code>{record.value || '""'}</code></td><td>{ttlLabel(record.ttl)}</td><td><div className="row-actions">{record.read_only ? <span className="dns-readonly">厂商维护</span> : canManage && <><button type="button" onClick={() => editRecord(record)}>编辑</button><button className="danger-action" type="button" onClick={() => void removeRecord(record)}>删除</button></>}</div></td></tr>)}</tbody></table>{!recordsLoading && filteredRecords.length === 0 && <div className="empty-state">{query ? '没有匹配的解析记录' : '当前 Zone 没有可显示的解析记录'}</div>}</div>
          {canManage && <div className="dns-domain-actions"><button type="button" onClick={() => void toggleDomain(selectedDomain)}>{selectedDomain.is_active ? '停用此域名' : '启用此域名'}</button><button className="danger-action" type="button" onClick={() => void removeDomain(selectedDomain)}>从 ZRT 移除</button></div>}
        </>}</div>
      </div>}
      {!loading && domains.length === 0 && <div className="empty-state modern-empty dns-empty"><span className="empty-icon">◎</span><h3>还没有接入域名</h3><p>{accounts.length ? '添加厂商账号下已有的 DNS Zone。' : '请先在“DNS 厂商账号”中保存凭据。'}</p></div>}
    </>}
  </section>
}
