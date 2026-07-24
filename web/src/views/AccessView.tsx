import { type FormEvent, useCallback, useEffect, useMemo, useState } from 'react'

import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'

interface Permission { code: string; group: string; description: string }
interface Role { id: string; name: string; display_name: string; description: string; permissions: string[]; updated_at: string }
interface PermissionOverrides { allow: string[]; deny: string[] }
interface ManagedUser {
  id: string; username: string; nickname: string; is_superuser: boolean; is_active: boolean
  role_ids: string[]; permission_overrides: PermissionOverrides; effective_permissions: string[]; last_login_at?: string
}
interface AuditLog { id: string; action: string; resource_type: string; resource_id?: string; result: string; client_ip: string; created_at: string }

const emptyRole = { name: '', display_name: '', description: '', permissions: [] as string[] }
const emptyUser = { username: '', nickname: '', password: '', role_ids: [] as string[] }

export default function AccessView() {
  const current = useAuthStore((state) => state.user)
  const allowed = useCallback((permission: string) => Boolean(current?.is_superuser || current?.permissions.includes(permission)), [current])
  const canManageUsers = allowed('user.manage')
  const canManageRoles = allowed('role.manage')
  const canReadUsers = allowed('user.read') || canManageUsers
  const canReadRoles = allowed('role.read') || canManageRoles || canManageUsers
  const canReadAudit = allowed('audit.read')
  const tabs = useMemo(() => [
    ...(canReadUsers ? [{ id: 'users', label: '用户' }] : []),
    ...(canReadRoles ? [{ id: 'roles', label: '角色与功能' }] : []),
    ...(canReadAudit ? [{ id: 'audit', label: '审计日志' }] : []),
  ], [canReadAudit, canReadRoles, canReadUsers])
  const [active, setActive] = useState(tabs[0]?.id || '')
  const [users, setUsers] = useState<ManagedUser[]>([])
  const [roles, setRoles] = useState<Role[]>([])
  const [permissions, setPermissions] = useState<Permission[]>([])
  const [audits, setAudits] = useState<AuditLog[]>([])
  const [editingUser, setEditingUser] = useState<ManagedUser | null>(null)
  const [userRoles, setUserRoles] = useState<string[]>([])
  const [userOverrides, setUserOverrides] = useState<Record<string, 'inherit' | 'allow' | 'deny'>>({})
  const [roleForm, setRoleForm] = useState(emptyRole)
  const [editingRoleID, setEditingRoleID] = useState('')
  const [userForm, setUserForm] = useState(emptyUser)
  const [showRoleForm, setShowRoleForm] = useState(false)
  const [showUserForm, setShowUserForm] = useState(false)
  const [loading, setLoading] = useState(true)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => { if (!tabs.some((tab) => tab.id === active)) setActive(tabs[0]?.id || '') }, [active, tabs])

  const refresh = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const [userResult, roleResult, permissionResult, auditResult] = await Promise.all([
        canReadUsers ? client.get<{ users: ManagedUser[] }>('/users?limit=200') : Promise.resolve(null),
        canReadRoles ? client.get<{ roles: Role[] }>('/roles') : Promise.resolve(null),
        canReadRoles ? client.get<{ permissions: Permission[] }>('/permissions') : Promise.resolve(null),
        canReadAudit ? client.get<{ audit_logs: AuditLog[] }>('/audit-logs?limit=100') : Promise.resolve(null),
      ])
      setUsers(userResult?.data.users || [])
      setRoles(roleResult?.data.roles || [])
      setPermissions(permissionResult?.data.permissions || [])
      setAudits(auditResult?.data.audit_logs || [])
    } catch (loadError) {
      setError(apiErrorMessage(loadError))
    } finally {
      setLoading(false)
    }
  }, [canReadAudit, canReadRoles, canReadUsers])

  useEffect(() => { void refresh() }, [refresh])

  const permissionGroups = useMemo(() => permissions.reduce<Record<string, Permission[]>>((groups, permission) => {
    if (!groups[permission.group]) groups[permission.group] = []
    groups[permission.group].push(permission)
    return groups
  }, {}), [permissions])

  function openUserEditor(user: ManagedUser) {
    const values: Record<string, 'inherit' | 'allow' | 'deny'> = {}
    permissions.forEach((permission) => { values[permission.code] = 'inherit' })
    user.permission_overrides.allow.forEach((permission) => { values[permission] = 'allow' })
    user.permission_overrides.deny.forEach((permission) => { values[permission] = 'deny' })
    setEditingUser(user)
    setUserRoles([...user.role_ids])
    setUserOverrides(values)
  }

  async function saveUserAccess() {
    if (!editingUser) return
    setSubmitting(true)
    setError('')
    try {
      const allow = Object.entries(userOverrides).filter(([, effect]) => effect === 'allow').map(([permission]) => permission)
      const deny = Object.entries(userOverrides).filter(([, effect]) => effect === 'deny').map(([permission]) => permission)
      await client.put(`/users/${editingUser.id}/roles`, { role_ids: userRoles })
      await client.put(`/users/${editingUser.id}/permissions`, { allow, deny })
      setEditingUser(null)
      await refresh()
    } catch (saveError) {
      setError(apiErrorMessage(saveError))
    } finally {
      setSubmitting(false)
    }
  }

  async function createUser(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setSubmitting(true)
    setError('')
    try {
      await client.post('/users', userForm)
      setUserForm(emptyUser)
      setShowUserForm(false)
      await refresh()
    } catch (createError) {
      setError(apiErrorMessage(createError))
    } finally {
      setSubmitting(false)
    }
  }

  async function toggleUser(user: ManagedUser) {
    setError('')
    try {
      await client.patch(`/users/${user.id}/status`, { active: !user.is_active })
      await refresh()
    } catch (toggleError) {
      setError(apiErrorMessage(toggleError))
    }
  }

  function editRole(role: Role) {
    setEditingRoleID(role.id)
    setRoleForm({ name: role.name, display_name: role.display_name, description: role.description || '', permissions: [...role.permissions] })
    setShowRoleForm(true)
  }

  async function saveRole(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setSubmitting(true)
    setError('')
    try {
      if (editingRoleID) await client.put(`/roles/${editingRoleID}`, roleForm)
      else await client.post('/roles', roleForm)
      setRoleForm(emptyRole)
      setEditingRoleID('')
      setShowRoleForm(false)
      await refresh()
    } catch (saveError) {
      setError(apiErrorMessage(saveError))
    } finally {
      setSubmitting(false)
    }
  }

  async function deleteRole(role: Role) {
    if (!window.confirm(`确认删除角色“${role.display_name}”？`)) return
    setError('')
    try {
      await client.delete(`/roles/${role.id}`)
      await refresh()
    } catch (deleteError) {
      setError(apiErrorMessage(deleteError))
    }
  }

  function toggleRolePermission(code: string) {
    setRoleForm((currentForm) => ({
      ...currentForm,
      permissions: currentForm.permissions.includes(code)
        ? currentForm.permissions.filter((item) => item !== code)
        : [...currentForm.permissions, code],
    }))
  }

  if (!tabs.length) return <section className="management-page"><div className="empty-state">当前账户没有身份与权限模块的查看权限</div></section>
  return <section className="access-page page-enter">
    <div className="page-heading modern-heading">
      <div><span className="section-label">完整 RBAC</span><h2>身份与权限</h2><p>角色定义通用功能权限，用户级允许或拒绝用于处理少量例外。</p></div>
      <div className="heading-actions"><button className="icon-button" type="button" onClick={() => void refresh()} disabled={loading}>↻</button>{active === 'users' && canManageUsers && <button className="primary-button" type="button" onClick={() => setShowUserForm(true)}>＋ 创建用户</button>}{active === 'roles' && canManageRoles && <button className="primary-button" type="button" onClick={() => { setEditingRoleID(''); setRoleForm(emptyRole); setShowRoleForm(true) }}>＋ 创建角色</button>}</div>
    </div>
    <div className="tab-bar">{tabs.map((tab) => <button type="button" className={active === tab.id ? 'active' : ''} key={tab.id} onClick={() => setActive(tab.id)}>{tab.label}</button>)}</div>
    {error && <div className="form-alert error system-alert" role="alert">{error}</div>}

    {active === 'users' && showUserForm && <form className="create-sheet" onSubmit={(event) => void createUser(event)}>
      <div className="sheet-header"><div><h3>创建用户</h3><p>创建后仍可单独调整角色和权限例外。</p></div><button type="button" onClick={() => setShowUserForm(false)}>×</button></div>
      <div className="form-grid"><label>用户名<input required maxLength={32} value={userForm.username} onChange={(event) => setUserForm({ ...userForm, username: event.target.value })} /></label><label>昵称<input maxLength={64} value={userForm.nickname} onChange={(event) => setUserForm({ ...userForm, nickname: event.target.value })} /></label><label className="span-2">初始密码<input required type="password" minLength={12} maxLength={128} value={userForm.password} onChange={(event) => setUserForm({ ...userForm, password: event.target.value })} /></label></div>
      <div className="role-choice-list">{roles.map((role) => <label key={role.id}><input type="checkbox" checked={userForm.role_ids.includes(role.id)} onChange={() => setUserForm((value) => ({ ...value, role_ids: value.role_ids.includes(role.id) ? value.role_ids.filter((id) => id !== role.id) : [...value.role_ids, role.id] }))} /><span><strong>{role.display_name}</strong><small>{role.description || role.name}</small></span></label>)}</div>
      <div className="form-actions"><button className="secondary-button" type="button" onClick={() => setShowUserForm(false)}>取消</button><button className="primary-button" disabled={submitting}>创建</button></div>
    </form>}

    {active === 'users' && <div className="access-user-list">{users.map((user) => <article className="access-user-card" key={user.id}>
      <div className="user-identity"><span>{(user.nickname || user.username).slice(0, 1)}</span><div><h3>{user.nickname || user.username}</h3><p>@{user.username}</p></div></div>
      <div className="user-role-summary">{user.is_superuser ? <b>超级管理员</b> : user.role_ids.map((roleID) => <span key={roleID}>{roles.find((role) => role.id === roleID)?.display_name || roleID}</span>)}{!user.is_superuser && user.role_ids.length === 0 && <span>未分配角色</span>}</div>
      <div className="user-permission-count"><strong>{user.is_superuser ? '全部' : user.effective_permissions.length}</strong><small>有效权限</small></div>
      <span className={`status-pill ${user.is_active ? 'status-succeeded' : 'status-canceled'}`}>{user.is_active ? '已启用' : '已停用'}</span>
      {canManageUsers && !user.is_superuser && <div className="card-actions"><button type="button" onClick={() => openUserEditor(user)}>配置权限</button><button type="button" onClick={() => void toggleUser(user)}>{user.is_active ? '停用' : '启用'}</button></div>}
    </article>)}</div>}

    {editingUser && <div className="access-editor-backdrop"><section className="access-editor">
      <div className="sheet-header"><div><h3>配置 {editingUser.nickname || editingUser.username}</h3><p>拒绝优先于允许；“继承角色”不会创建用户级规则。</p></div><button type="button" onClick={() => setEditingUser(null)}>×</button></div>
      <h4>角色</h4><div className="role-choice-list">{roles.map((role) => <label key={role.id}><input type="checkbox" checked={userRoles.includes(role.id)} onChange={() => setUserRoles((values) => values.includes(role.id) ? values.filter((id) => id !== role.id) : [...values, role.id])} /><span><strong>{role.display_name}</strong><small>{role.description || role.name}</small></span></label>)}</div>
      <h4>用户权限例外</h4><div className="permission-groups">{Object.entries(permissionGroups).map(([group, items]) => <fieldset key={group}><legend>{group}</legend>{items.map((permission) => <label key={permission.code}><span><strong>{permission.description}</strong><code>{permission.code}</code></span><select value={userOverrides[permission.code] || 'inherit'} onChange={(event) => setUserOverrides((values) => ({ ...values, [permission.code]: event.target.value as 'inherit' | 'allow' | 'deny' }))}><option value="inherit">继承角色</option><option value="allow">额外允许</option><option value="deny">显式拒绝</option></select></label>)}</fieldset>)}</div>
      <div className="form-actions"><button className="secondary-button" type="button" onClick={() => setEditingUser(null)}>取消</button><button className="primary-button" type="button" disabled={submitting} onClick={() => void saveUserAccess()}>保存权限</button></div>
    </section></div>}

    {active === 'roles' && showRoleForm && <form className="create-sheet role-editor-sheet" onSubmit={(event) => void saveRole(event)}>
      <div className="sheet-header"><div><h3>{editingRoleID ? '修改角色' : '创建角色'}</h3><p>按功能勾选最小权限，菜单与接口会使用同一份有效权限。</p></div><button type="button" onClick={() => setShowRoleForm(false)}>×</button></div>
      <div className="form-grid"><label>角色标识<input required value={roleForm.name} onChange={(event) => setRoleForm({ ...roleForm, name: event.target.value })} placeholder="release_operator" /></label><label>显示名称<input required value={roleForm.display_name} onChange={(event) => setRoleForm({ ...roleForm, display_name: event.target.value })} /></label><label className="span-2">说明<input value={roleForm.description} onChange={(event) => setRoleForm({ ...roleForm, description: event.target.value })} /></label></div>
      <div className="permission-groups role-permission-groups">{Object.entries(permissionGroups).map(([group, items]) => <fieldset key={group}><legend>{group}</legend>{items.map((permission) => <label key={permission.code}><input type="checkbox" checked={roleForm.permissions.includes(permission.code)} onChange={() => toggleRolePermission(permission.code)} /><span><strong>{permission.description}</strong><code>{permission.code}</code></span></label>)}</fieldset>)}</div>
      <div className="form-actions"><button className="secondary-button" type="button" onClick={() => setShowRoleForm(false)}>取消</button><button className="primary-button" disabled={submitting}>保存角色</button></div>
    </form>}

    {active === 'roles' && <div className="role-card-grid">{roles.map((role) => <article className="role-card" key={role.id}><div><span className="role-mark">R</span><h3>{role.display_name}</h3><code>{role.name}</code></div><p>{role.description || '未填写角色说明'}</p><div className="role-permission-tags">{role.permissions.slice(0, 8).map((permission) => <span key={permission}>{permission}</span>)}{role.permissions.length > 8 && <span>+{role.permissions.length - 8}</span>}</div>{canManageRoles && <div className="card-actions"><button type="button" onClick={() => editRole(role)}>编辑</button><button className="danger-action" type="button" onClick={() => void deleteRole(role)}>删除</button></div>}</article>)}</div>}

    {active === 'audit' && <div className="audit-table"><table><thead><tr><th>时间</th><th>动作</th><th>资源</th><th>结果</th><th>来源 IP</th></tr></thead><tbody>{audits.map((item) => <tr key={item.id}><td>{new Date(item.created_at).toLocaleString('zh-CN', { hour12: false })}</td><td><code>{item.action}</code></td><td>{item.resource_type}{item.resource_id ? ` / ${item.resource_id}` : ''}</td><td>{item.result}</td><td>{item.client_ip}</td></tr>)}</tbody></table></div>}
  </section>
}
