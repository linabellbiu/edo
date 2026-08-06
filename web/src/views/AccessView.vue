<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Modal, message } from 'ant-design-vue'
import {
  Building2, MoreHorizontal, Plus, RefreshCw, Search,
  ShieldCheck, UserRound, UsersRound,
} from 'lucide-vue-next'

import client from '@/api/client'
import { apiErrorMessage, type ResourceRecord } from '@/api/resources'
import PageToolbar from '@/components/PageToolbar.vue'
import ResourceTable from '@/components/ResourceTable.vue'
import { useAuthStore } from '@/stores/auth'

interface Permission {
  code: string
  group: string
  resource: string
  resource_name: string
  action: string
  action_name: string
  name: string
  description: string
  dangerous: boolean
}

interface PermissionResource {
  key: string
  name: string
  items: Permission[]
}

interface PermissionGroup {
  name: string
  resources: PermissionResource[]
}

interface Department extends ResourceRecord {
  id: string
  name: string
  description: string
  is_active: boolean
  member_count: number
  is_default: boolean
  created_at: string
  updated_at: string
}

interface Role extends ResourceRecord {
  id: string
  name: string
  display_name: string
  description: string
  permissions: string[]
  in_use: boolean
  visible_member_count?: number
  updated_at: string
}

interface ManagedUser extends ResourceRecord {
  id: string
  username: string
  nickname: string
  department_id: string
  department_name: string
  is_superuser: boolean
  is_active: boolean
  role_ids: string[]
  permission_overrides: { allow: string[]; deny: string[] }
  effective_permissions: string[]
  last_login_at?: string
}

interface AuditLog extends ResourceRecord {
  id: string
  action: string
  resource_type: string
  resource_id?: string
  result: string
  client_ip: string
  created_at: string
}

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

const users = ref<ManagedUser[]>([])
const departments = ref<Department[]>([])
const roles = ref<Role[]>([])
const permissions = ref<Permission[]>([])
const audits = ref<AuditLog[]>([])
const loading = ref(false)
const saving = ref(false)
const userQuery = ref('')
const userStatus = ref<'all' | 'active' | 'inactive'>('all')
const userDepartmentID = ref('')
const userRoleID = ref('')
const departmentQuery = ref('')
const roleQuery = ref('')
const rolePermissionQuery = ref('')
const rolePermissionOnlySelected = ref(false)
const accessPermissionQuery = ref('')
const accessOnlyChanged = ref(false)

const userFormOpen = ref(false)
const departmentFormOpen = ref(false)
const userDepartmentOpen = ref(false)
const roleFormOpen = ref(false)
const accessOpen = ref(false)
const memberDrawerOpen = ref(false)
const editingDepartmentID = ref('')
const editingRoleID = ref('')
const editingUser = ref<ManagedUser | null>(null)
const roleEditorMode = ref<'create' | 'basic' | 'permissions'>('create')
const memberContext = reactive({ kind: 'role' as 'role' | 'department', id: '', name: '' })

const userForm = reactive({
  username: '',
  nickname: '',
  password: '',
  department_id: '',
  role_ids: [] as string[],
})
const departmentForm = reactive({ name: '', description: '' })
const userDepartmentForm = reactive({ department_id: '' })
const roleForm = reactive({
  name: '',
  display_name: '',
  description: '',
  permissions: [] as string[],
})
const accessForm = reactive({
  role_ids: [] as string[],
  effects: {} as Record<string, 'inherit' | 'allow' | 'deny'>,
})

const tabs = computed(() => [
  ...(auth.canAny(['user.read', 'user.create', 'user.update', 'user.delete'])
    ? [{ key: 'users', label: '用户管理' }]
    : []),
  ...(auth.canAny(['department.read', 'department.create', 'department.update', 'department.delete'])
    ? [{ key: 'departments', label: '部门管理' }]
    : []),
  ...(auth.canAny(['role.read', 'role.create', 'role.update', 'role.delete'])
    ? [{ key: 'roles', label: '角色与权限' }]
    : []),
  ...(auth.canAny(['audit.read']) ? [{ key: 'audit', label: '审计日志' }] : []),
])

const active = computed(() =>
  tabs.value.some((item) => item.key === route.query.view)
    ? String(route.query.view)
    : tabs.value[0]?.key || '',
)

const canReadUsers = computed(() => canAny(['user.read', 'user.create', 'user.update', 'user.delete']))
const canAccessDepartments = computed(() => canAny([
  'department.read', 'department.create', 'department.update', 'department.delete', 'user.create', 'user.update',
]))
const canAccessRoles = computed(() => canAny([
  'role.read', 'role.create', 'role.update', 'role.delete', 'user.create', 'user.update',
]))
const activeDepartments = computed(() => departments.value.filter((item) => item.is_active))
const departmentOptions = computed(() => activeDepartments.value.map((item) => ({ value: item.id, label: item.name })))
const roleOptions = computed(() => roles.value.map((item) => ({ value: item.id, label: item.display_name })))
const delegableRoleOptions = computed(() => roles.value.map((item) => {
  const disabled = !item.permissions.every((permission) => canDelegatePermission(permission))
  return {
    value: item.id,
    label: disabled ? `${item.display_name}（超出可委派范围）` : item.display_name,
    disabled,
  }
}))
const roleMap = computed(() => new Map(roles.value.map((item) => [item.id, item])))

const filteredUsers = computed(() => {
  const keyword = userQuery.value.trim().toLocaleLowerCase()
  return users.value.filter((item) => {
    if (userStatus.value === 'active' && !item.is_active) return false
    if (userStatus.value === 'inactive' && item.is_active) return false
    if (userDepartmentID.value && item.department_id !== userDepartmentID.value) return false
    if (userRoleID.value && !item.role_ids.includes(userRoleID.value)) return false
    if (!keyword) return true
    const roleNames = item.role_ids.map((id) => roleMap.value.get(id)?.display_name || '').join(' ')
    return `${item.username} ${item.nickname} ${item.department_name} ${roleNames}`.toLocaleLowerCase().includes(keyword)
  })
})

const filteredDepartments = computed(() => {
  const keyword = departmentQuery.value.trim().toLocaleLowerCase()
  if (!keyword) return departments.value
  return departments.value.filter((item) => `${item.name} ${item.description}`.toLocaleLowerCase().includes(keyword))
})

const filteredRoles = computed(() => {
  const keyword = roleQuery.value.trim().toLocaleLowerCase()
  if (!keyword) return roles.value
  return roles.value.filter((item) => `${item.display_name} ${item.name} ${item.description}`.toLocaleLowerCase().includes(keyword))
})

const associatedUsers = computed(() => users.value.filter((item) => memberContext.kind === 'role'
  ? item.role_ids.includes(memberContext.id)
  : item.department_id === memberContext.id))

const accessRolePermissionCodes = computed(() => {
  const values = new Set<string>()
  accessForm.role_ids.forEach((id) => roleMap.value.get(id)?.permissions.forEach((permission) => values.add(permission)))
  return values
})

const accessEffectivePermissionCodes = computed(() => {
  const values = new Set(accessRolePermissionCodes.value)
  Object.entries(accessForm.effects).forEach(([permission, effect]) => {
    if (effect === 'allow') values.add(permission)
    if (effect === 'deny') values.delete(permission)
  })
  return values
})

const accessOverrideCount = computed(() => Object.values(accessForm.effects).filter((effect) => effect !== 'inherit').length)

const permissionGroups = computed<PermissionGroup[]>(() => {
  const groups = new Map<string, Map<string, PermissionResource>>()
  for (const permission of permissions.value) {
    const groupName = permission.group || '其他'
    const resourceKey = permission.resource || permission.code.split('.')[0] || 'other'
    const group = groups.get(groupName) ?? new Map<string, PermissionResource>()
    const resource = group.get(resourceKey) ?? {
      key: resourceKey,
      name: permission.resource_name || resourceKey,
      items: [],
    }
    resource.items.push(permission)
    group.set(resourceKey, resource)
    groups.set(groupName, group)
  }
  return Array.from(groups, ([name, resources]) => ({
    name,
    resources: Array.from(resources.values()),
  }))
})

function filterPermissionGroups(query: string, include: (permission: Permission) => boolean) {
  const keyword = query.trim().toLocaleLowerCase()
  return permissionGroups.value.map((group) => ({
    ...group,
    resources: group.resources.map((resource) => ({
      ...resource,
      items: resource.items.filter((item) => {
        if (!include(item)) return false
        if (!keyword) return true
        return `${item.name} ${item.description} ${item.code} ${item.resource_name} ${item.action_name}`
          .toLocaleLowerCase().includes(keyword)
      }),
    })).filter((resource) => resource.items.length > 0),
  })).filter((group) => group.resources.length > 0)
}

const visibleRolePermissionGroups = computed(() => filterPermissionGroups(
  rolePermissionQuery.value,
  (permission) => !rolePermissionOnlySelected.value || roleForm.permissions.includes(permission.code),
))

const visibleAccessPermissionGroups = computed(() => filterPermissionGroups(
  accessPermissionQuery.value,
  (permission) => !accessOnlyChanged.value || accessForm.effects[permission.code] !== 'inherit',
))

const userColumns = [
  { key: 'username', label: '用户' },
  { key: 'department_name', label: '部门' },
  { key: 'role_ids', label: '角色' },
  { key: 'effective_permissions', label: '有效权限' },
  { key: 'is_active', label: '状态' },
  { key: 'last_login_at', label: '最后登录' },
]
const departmentColumns = [
  { key: 'name', label: '部门名称' },
  { key: 'description', label: '说明' },
  { key: 'member_count', label: '成员' },
  { key: 'is_default', label: '类型' },
  { key: 'updated_at', label: '更新时间' },
]
const roleColumns = [
  { key: 'display_name', label: '名称' },
  { key: 'name', label: '技术标识' },
  { key: 'description', label: '说明' },
  { key: 'visible_member_count', label: '关联用户' },
  { key: 'permissions', label: '权限数量' },
  { key: 'updated_at', label: '更新时间' },
]
const auditColumns = [
  { key: 'action', label: '动作' },
  { key: 'resource_type', label: '资源' },
  { key: 'resource_id', label: '资源 ID' },
  { key: 'result', label: '结果' },
  { key: 'client_ip', label: '来源 IP' },
  { key: 'created_at', label: '时间' },
]

const toolbarDescription = computed(() => {
  if (active.value === 'audit') return '查看身份、资源、操作结果和请求来源，不展示任何密钥内容。'
  if (active.value === 'roles') return '按查看、创建、修改、删除和执行等动作组合可复用的角色权限。'
  if (active.value === 'departments') return '部门用于隔离业务数据；普通用户只能访问本部门成员创建的资源。'
  return '维护账户所属部门、状态、角色和用户级允许或拒绝例外。'
})

function canAny(required: string[]) {
  return auth.canAny(required)
}

function canDelegatePermission(permission: string) {
  return Boolean(auth.user?.is_superuser || auth.permissions.has(permission))
}

function canManageUserAccess(item: ManagedUser) {
  if (auth.user?.is_superuser) return true
  if (item.id === auth.user?.id) return false
  const rolesDelegable = item.role_ids.every((id) => roleMap.value.get(id)?.permissions.every(canDelegatePermission))
  return rolesDelegable && item.permission_overrides.allow.every(canDelegatePermission)
}

function canManageRolePermissions(item: Role) {
  return Boolean(auth.user?.is_superuser || item.permissions.every(canDelegatePermission))
}

function userInitial(row: ResourceRecord) {
  const value = String(row.nickname || row.username || '?')
  return value.slice(0, 1).toLocaleUpperCase()
}

async function refresh() {
  loading.value = true
  try {
    const canLoadUsers = canAny(['user.read', 'user.create', 'user.update', 'user.delete'])
    const canLoadDepartments = canAny([
      'department.read',
      'department.create',
      'department.update',
      'department.delete',
      'user.create',
      'user.update',
    ])
    const canLoadRoles = canAny([
      'role.read',
      'role.create',
      'role.update',
      'role.delete',
      'user.create',
      'user.update',
    ])
    const canLoadPermissions = canAny(['role.read', 'role.create', 'role.update', 'role.delete', 'user.update'])
    const [userResponse, departmentResponse, roleResponse, permissionResponse, auditResponse] =
      await Promise.all([
        canLoadUsers ? loadAllUsers() : null,
        canLoadDepartments ? client.get<{ departments: Department[] }>('/departments') : null,
        canLoadRoles ? client.get<{ roles: Role[] }>('/roles') : null,
        canLoadPermissions ? client.get<{ permissions: Permission[] }>('/permissions') : null,
        canAny(['audit.read'])
          ? client.get<{ audit_logs: AuditLog[] }>('/audit-logs?limit=100')
          : null,
      ])
    users.value = userResponse || []
    departments.value = departmentResponse?.data.departments || []
    roles.value = roleResponse?.data.roles || []
    permissions.value = permissionResponse?.data.permissions || []
    audits.value = auditResponse?.data.audit_logs || []
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    loading.value = false
  }
}

async function loadAllUsers() {
  const pageSize = 200
  const result: ManagedUser[] = []
  for (let offset = 0; ; offset += pageSize) {
    const response = await client.get<{ users: ManagedUser[] }>(`/users?limit=${pageSize}&offset=${offset}`)
    const page = response.data.users || []
    result.push(...page)
    if (page.length < pageSize) return result
  }
}

function selectTab(key: string) {
  void router.replace({ query: { ...route.query, view: key } })
}

function resetUser() {
  Object.assign(userForm, {
    username: '',
    nickname: '',
    password: '',
    department_id: auth.user?.is_superuser
      ? activeDepartments.value[0]?.id || ''
      : auth.user?.department_id || '',
    role_ids: [],
  })
}

async function createUser() {
  if (!userForm.department_id) {
    message.warning('请选择用户所属部门')
    return
  }
  saving.value = true
  try {
    const response = await client.post<{ warning?: string }>('/users', userForm)
    if (response.data.warning) message.warning(response.data.warning)
    else message.success('用户已创建')
    userFormOpen.value = false
    resetUser()
    await refresh()
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    saving.value = false
  }
}

function resetDepartment() {
  Object.assign(departmentForm, { name: '', description: '' })
  editingDepartmentID.value = ''
}

function editDepartment(item: Department) {
  Object.assign(departmentForm, { name: item.name, description: item.description || '' })
  editingDepartmentID.value = item.id
  departmentFormOpen.value = true
}

async function saveDepartment() {
  if (!departmentForm.name.trim()) {
    message.warning('请输入部门名称')
    return
  }
  saving.value = true
  try {
    if (editingDepartmentID.value) {
      await client.put(`/departments/${editingDepartmentID.value}`, departmentForm)
      message.success('部门已更新')
    } else {
      await client.post('/departments', departmentForm)
      message.success('部门已创建')
    }
    departmentFormOpen.value = false
    resetDepartment()
    await refresh()
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    saving.value = false
  }
}

function removeDepartment(item: Department) {
  Modal.confirm({
    title: `删除部门“${item.name}”？`,
    content: item.member_count > 0
      ? `该部门仍有 ${item.member_count} 名成员，请先调整成员所属部门。`
      : '仅没有成员、业务资源和历史记录的部门可以删除；删除后无法恢复。',
    okText: '删除',
    okType: 'danger',
    cancelText: '取消',
    okButtonProps: { disabled: item.member_count > 0 },
    async onOk() {
      try {
        await client.delete(`/departments/${item.id}`)
        message.success('部门已删除')
        await refresh()
      } catch (error) {
        message.error(apiErrorMessage(error))
      }
    },
  })
}

function openUserDepartment(item: ManagedUser) {
  editingUser.value = item
  userDepartmentForm.department_id = item.department_id || ''
  userDepartmentOpen.value = true
}

async function saveUserDepartment() {
  if (!editingUser.value || !userDepartmentForm.department_id) {
    message.warning('请选择用户所属部门')
    return
  }
  saving.value = true
  try {
    await client.put(`/users/${editingUser.value.id}/department`, userDepartmentForm)
    message.success('用户所属部门已更新')
    userDepartmentOpen.value = false
    await refresh()
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    saving.value = false
  }
}

function resetRole() {
  Object.assign(roleForm, {
    name: '',
    display_name: '',
    description: '',
    permissions: [],
  })
  editingRoleID.value = ''
}

function loadRoleForm(item: Role) {
  Object.assign(roleForm, {
    name: item.name,
    display_name: item.display_name,
    description: item.description || '',
    permissions: [...item.permissions],
  })
  editingRoleID.value = item.id
}

function createRole() {
  resetRole()
  roleEditorMode.value = 'create'
  rolePermissionQuery.value = ''
  rolePermissionOnlySelected.value = false
  roleFormOpen.value = true
}

function editRoleBasic(item: Role) {
  loadRoleForm(item)
  roleEditorMode.value = 'basic'
  roleFormOpen.value = true
}

function editRolePermissions(item: Role) {
  loadRoleForm(item)
  roleEditorMode.value = 'permissions'
  rolePermissionQuery.value = ''
  rolePermissionOnlySelected.value = false
  roleFormOpen.value = true
}

async function saveRole() {
  saving.value = true
  try {
    let warning = ''
    if (!editingRoleID.value) {
      const response = await client.post<{ warning?: string }>('/roles', roleForm)
      warning = response.data.warning || ''
    } else if (roleEditorMode.value === 'basic') {
      await client.patch(`/roles/${editingRoleID.value}/basic`, {
        name: roleForm.name,
        display_name: roleForm.display_name,
        description: roleForm.description,
      })
    } else {
      await client.put(`/roles/${editingRoleID.value}/permissions`, { permissions: roleForm.permissions })
    }
    if (warning) message.warning(warning)
    else message.success(!editingRoleID.value ? '角色已创建' : roleEditorMode.value === 'basic' ? '角色基本信息已更新' : '角色权限已更新')
    roleFormOpen.value = false
    resetRole()
    await refresh()
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    saving.value = false
  }
}

function removeRole(item: Role) {
  const visibleCount = item.visible_member_count
  Modal.confirm({
    title: `删除角色“${item.display_name}”？`,
    content: item.in_use
      ? visibleCount === undefined || visibleCount === 0
        ? '该角色仍关联用户，请先调整关联用户的角色。'
        : `该角色仍关联 ${visibleCount} 名当前可见用户，请先调整这些用户的角色。`
      : '角色当前未关联用户；删除后无法恢复。',
    okText: '删除',
    okType: 'danger',
    cancelText: '取消',
    okButtonProps: { disabled: item.in_use },
    async onOk() {
      try {
        const response = await client.delete<{ warning?: string }>(`/roles/${item.id}`)
        if (response.data.warning) message.warning(response.data.warning)
        else message.success('角色已删除')
        await refresh()
      } catch (error) {
        message.error(apiErrorMessage(error))
      }
    },
  })
}

function removeUser(item: ManagedUser) {
  Modal.confirm({
    title: `删除用户“${item.nickname || item.username}”？`,
    content: '账户、个人 Git 令牌和权限配置会被删除；部门业务资源和审计记录会保留。若个人令牌仍被仓库引用，需要先为仓库更换凭据。',
    okText: '删除',
    okType: 'danger',
    cancelText: '取消',
    async onOk() {
      try {
        await client.delete(`/users/${item.id}`)
        message.success('用户已删除')
        await refresh()
      } catch (error) {
        message.error(apiErrorMessage(error))
        throw error
      }
    },
  })
}

function toggleUser(item: ManagedUser) {
  const action = item.is_active ? '停用' : '启用'
  Modal.confirm({
    title: `${action}用户“${item.nickname || item.username}”？`,
    content: item.is_active
      ? '停用后该用户将无法继续登录，现有会话也会失效。'
      : '启用后该用户可以再次使用已配置的登录方式访问 EDO。',
    okText: action,
    okType: item.is_active ? 'danger' : 'primary',
    cancelText: '取消',
    async onOk() {
      try {
        await client.patch(`/users/${item.id}/status`, { active: !item.is_active })
        message.success(`用户已${action}`)
        await refresh()
      } catch (error) {
        message.error(apiErrorMessage(error))
        throw error
      }
    },
  })
}

function openMembers(kind: 'role' | 'department', item: Role | Department) {
  if (!canReadUsers.value) {
    message.warning('需要用户查看权限才能查看关联成员')
    return
  }
  Object.assign(memberContext, { kind, id: item.id, name: kind === 'role' ? (item as Role).display_name : item.name })
  memberDrawerOpen.value = true
}

function handleUserAction(item: ManagedUser, key: string) {
  if (key === 'department') openUserDepartment(item)
  else if (key === 'toggle') toggleUser(item)
  else if (key === 'delete') removeUser(item)
}

function editAccess(item: ManagedUser) {
  editingUser.value = item
  accessPermissionQuery.value = ''
  accessOnlyChanged.value = false
  accessForm.role_ids = [...item.role_ids]
  accessForm.effects = {}
  permissions.value.forEach((permission) => {
    accessForm.effects[permission.code] = 'inherit'
  })
  item.permission_overrides.allow.forEach((permission) => {
    accessForm.effects[permission] = 'allow'
  })
  item.permission_overrides.deny.forEach((permission) => {
    accessForm.effects[permission] = 'deny'
  })
  accessOpen.value = true
}

async function saveAccess() {
  if (!editingUser.value) return
  saving.value = true
  try {
    const allow = Object.entries(accessForm.effects)
      .filter(([, effect]) => effect === 'allow')
      .map(([permission]) => permission)
    const deny = Object.entries(accessForm.effects)
      .filter(([, effect]) => effect === 'deny')
      .map(([permission]) => permission)
    await client.put(`/users/${editingUser.value.id}/access`, { role_ids: accessForm.role_ids, allow, deny })
    message.success('用户权限已更新')
    accessOpen.value = false
    await refresh()
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    saving.value = false
  }
}

function roleResourceChecked(resource: PermissionResource) {
  const delegable = resource.items.filter((item) => canDelegatePermission(item.code))
  return delegable.length > 0 && delegable.every((item) => roleForm.permissions.includes(item.code))
}

function roleResourceIndeterminate(resource: PermissionResource) {
  const delegable = resource.items.filter((item) => canDelegatePermission(item.code))
  const count = delegable.filter((item) => roleForm.permissions.includes(item.code)).length
  return count > 0 && count < delegable.length
}

function toggleRoleResource(resource: PermissionResource, checked: boolean) {
  const selected = new Set(roleForm.permissions)
  resource.items.filter((item) => canDelegatePermission(item.code))
    .forEach((item) => checked ? selected.add(item.code) : selected.delete(item.code))
  roleForm.permissions = Array.from(selected)
}

function toggleRolePermission(code: string, checked: boolean) {
  const selected = new Set(roleForm.permissions)
  if (checked) selected.add(code)
  else selected.delete(code)
  roleForm.permissions = Array.from(selected)
}

function permissionTitle(permission: Permission) {
  return permission.name || `${permission.action_name || permission.action}${permission.resource_name || permission.resource}`
}

function effectivePermissionSummary(value: unknown) {
  if (!Array.isArray(value)) return '全部'
  if (value.includes('*')) return '全部'
  return `${value.length} 项`
}

watch(
  active,
  (value) => {
    if (value && route.query.view !== value) selectTab(value)
  },
  { immediate: true },
)
onMounted(refresh)
</script>

<template>
  <section>
    <PageToolbar :description="toolbarDescription">
      <a-button :loading="loading" @click="refresh">
        <RefreshCw :size="15" />刷新
      </a-button>
      <a-button
        v-if="active === 'users' && canAny(['user.create'])"
        type="primary"
        @click="resetUser(); userFormOpen = true"
      >
        <Plus :size="15" />创建用户
      </a-button>
      <a-button
        v-if="active === 'departments' && auth.user?.is_superuser && canAny(['department.create'])"
        type="primary"
        @click="resetDepartment(); departmentFormOpen = true"
      >
        <Plus :size="15" />创建部门
      </a-button>
      <a-button
        v-if="active === 'roles' && canAny(['role.create'])"
        type="primary"
        @click="createRole"
      >
        <Plus :size="15" />创建角色
      </a-button>
    </PageToolbar>

    <a-segmented
      :value="active"
      :options="tabs.map((item) => ({ value: item.key, label: item.label }))"
      class="edo-page-tabs"
      aria-label="访问控制页面"
      @change="(value: string) => selectTab(value)"
    />

    <div v-if="active !== 'audit'" class="access-summary">
      <article>
        <span><UsersRound :size="18" /></span>
        <div><strong>{{ canReadUsers ? users.length : '—' }}</strong><small>用户</small></div>
        <em>{{ canReadUsers ? `${users.filter((item) => item.is_active).length} 个启用` : '无权查看' }}</em>
      </article>
      <article>
        <span><Building2 :size="18" /></span>
        <div><strong>{{ canAccessDepartments ? departments.length : '—' }}</strong><small>部门</small></div>
        <em>{{ canAccessDepartments ? '数据隔离边界' : '无权查看' }}</em>
      </article>
      <article>
        <span><ShieldCheck :size="18" /></span>
        <div><strong>{{ canAccessRoles ? roles.length : '—' }}</strong><small>角色</small></div>
        <em>{{ canAccessRoles ? `${roles.reduce((total, item) => total + item.permissions.length, 0)} 项授权` : '无权查看' }}</em>
      </article>
    </div>

    <div v-if="active === 'users'" class="access-filter-bar vben-card">
      <a-input v-model:value="userQuery" allow-clear placeholder="搜索用户名、昵称、部门或角色">
        <template #prefix><Search :size="15" /></template>
      </a-input>
      <a-segmented
        v-model:value="userStatus"
        :options="[{ value: 'all', label: '全部' }, { value: 'active', label: '启用' }, { value: 'inactive', label: '停用' }]"
      />
      <a-select v-model:value="userDepartmentID" allow-clear placeholder="全部部门" :options="departmentOptions" />
      <a-select v-model:value="userRoleID" allow-clear placeholder="全部角色" :options="roleOptions" />
      <span>显示 {{ filteredUsers.length }} / {{ users.length }} 人</span>
    </div>
    <div v-else-if="active === 'departments'" class="access-filter-bar compact vben-card">
      <a-input v-model:value="departmentQuery" allow-clear placeholder="搜索部门名称或说明">
        <template #prefix><Search :size="15" /></template>
      </a-input>
      <span>显示 {{ filteredDepartments.length }} / {{ departments.length }} 个部门</span>
    </div>
    <div v-else-if="active === 'roles'" class="access-filter-bar compact vben-card">
      <a-input v-model:value="roleQuery" allow-clear placeholder="搜索角色名称、标识或说明">
        <template #prefix><Search :size="15" /></template>
      </a-input>
      <span>显示 {{ filteredRoles.length }} / {{ roles.length }} 个角色</span>
    </div>

    <div class="vben-card">
      <ResourceTable
        v-if="active === 'users'"
        :rows="filteredUsers"
        :columns="userColumns"
        :loading="loading"
        empty-text="没有符合筛选条件的用户"
      >
        <template #cell-username="{ row }">
          <div class="user-identity">
            <span>{{ userInitial(row) }}</span>
            <div><strong>{{ row.nickname || row.username }}</strong><small>{{ row.username }}</small></div>
          </div>
        </template>
        <template #cell-department_name="{ row, value }">
          <a-tag v-if="row.is_superuser" color="purple">全部部门</a-tag>
          <span v-else>{{ value || '未分配' }}</span>
        </template>
        <template #cell-role_ids="{ row }">
          <div class="tag-list">
            <a-tag v-if="row.is_superuser" color="purple">超级管理员</a-tag>
            <a-tag v-for="id in row.role_ids" :key="String(id)">
              {{ roles.find((role) => role.id === id)?.display_name || id }}
            </a-tag>
          </div>
        </template>
        <template #cell-effective_permissions="{ value }">
          {{ effectivePermissionSummary(value) }}
        </template>
        <template #actions="{ row }">
          <template v-if="!row.is_superuser && (auth.user?.is_superuser || row.id !== auth.user?.id)">
            <a-button
              v-if="canAny(['user.update'])"
              type="link"
              :disabled="!canManageUserAccess(row as ManagedUser)"
              :title="canManageUserAccess(row as ManagedUser) ? '配置角色与权限' : '目标用户当前权限超出你的可委派范围'"
              @click="editAccess(row as ManagedUser)"
            >配置权限</a-button>
            <a-dropdown v-if="canAny(['user.update', 'user.delete'])" :trigger="['click']">
              <a-button type="link">更多<MoreHorizontal :size="15" /></a-button>
              <template #overlay>
                <a-menu @click="handleUserAction(row as ManagedUser, String($event.key))">
                  <a-menu-item v-if="auth.user?.is_superuser && canAny(['user.update'])" key="department">调整部门</a-menu-item>
                  <a-menu-item v-if="canAny(['user.update'])" key="toggle">{{ row.is_active ? '停用用户' : '启用用户' }}</a-menu-item>
                  <a-menu-divider v-if="canAny(['user.delete'])" />
                  <a-menu-item v-if="canAny(['user.delete'])" key="delete" danger>删除用户</a-menu-item>
                </a-menu>
              </template>
            </a-dropdown>
          </template>
        </template>
      </ResourceTable>

      <ResourceTable
        v-else-if="active === 'departments'"
        :rows="filteredDepartments"
        :columns="departmentColumns"
        :loading="loading"
        empty-text="没有符合筛选条件的部门"
      >
        <template #cell-member_count="{ row, value }">
          <a-button v-if="canReadUsers" type="link" @click="openMembers('department', row as Department)">{{ value || 0 }} 人</a-button>
          <span v-else>{{ value || 0 }} 人</span>
        </template>
        <template #cell-is_default="{ value }"><a-tag :color="value ? 'blue' : 'default'">{{ value ? '默认部门' : '业务部门' }}</a-tag></template>
        <template #actions="{ row }">
          <a-button
            v-if="canAny(['department.update'])"
            type="link"
            @click="editDepartment(row as Department)"
          >编辑</a-button>
          <a-button
            v-if="canAny(['department.delete'])"
            type="link"
            danger
            :disabled="row.is_default"
            :title="row.is_default ? '默认部门不能删除' : '删除部门'"
            @click="removeDepartment(row as Department)"
          >删除</a-button>
        </template>
      </ResourceTable>

      <ResourceTable
        v-else-if="active === 'roles'"
        :rows="filteredRoles"
        :columns="roleColumns"
        :loading="loading"
        empty-text="没有符合筛选条件的角色"
      >
        <template #cell-visible_member_count="{ row, value }">
          <a-button v-if="canReadUsers && value !== undefined" type="link" @click="openMembers('role', row as Role)">{{ value || 0 }} 人</a-button>
          <span v-else>无权查看</span>
        </template>
        <template #cell-permissions="{ value }">
          {{ Array.isArray(value) ? value.length : 0 }} 项
        </template>
        <template #actions="{ row }">
          <a-button
            v-if="canAny(['role.update'])"
            type="link"
            @click="editRoleBasic(row as Role)"
          >基本信息</a-button>
          <a-button
            v-if="canAny(['role.update'])"
            type="link"
            :disabled="!canManageRolePermissions(row as Role)"
            :title="canManageRolePermissions(row as Role) ? '配置角色权限' : '该角色包含超出你可委派范围的权限'"
            @click="editRolePermissions(row as Role)"
          >权限配置</a-button>
          <a-button
            v-if="canAny(['role.delete'])"
            type="link"
            danger
            :disabled="Boolean(row.in_use)"
            :title="row.in_use ? '仍有关联用户，不能删除' : '删除角色'"
            @click="removeRole(row as Role)"
          >删除</a-button>
        </template>
      </ResourceTable>

      <ResourceTable v-else :rows="audits" :columns="auditColumns" :loading="loading">
        <template #cell-result="{ value }">
          <a-tag :color="value === 'success' ? 'success' : 'error'">{{ value }}</a-tag>
        </template>
      </ResourceTable>
    </div>

    <a-modal
      v-model:open="userFormOpen"
      title="创建用户"
      :confirm-loading="saving"
      ok-text="创建"
      cancel-text="取消"
      @ok="createUser"
    >
      <a-form layout="vertical">
        <a-form-item label="用户名" required>
          <a-input v-model:value="userForm.username" maxlength="32" />
        </a-form-item>
        <a-form-item label="昵称">
          <a-input v-model:value="userForm.nickname" maxlength="64" />
        </a-form-item>
        <a-form-item label="所属部门" required>
          <a-select
            v-model:value="userForm.department_id"
            placeholder="请选择部门"
            :disabled="!auth.user?.is_superuser"
            :options="departmentOptions"
          />
          <small class="field-hint">普通管理员只能在自己的部门创建用户。</small>
        </a-form-item>
        <a-form-item label="初始密码" required>
          <a-input-password v-model:value="userForm.password" minlength="12" maxlength="128" />
        </a-form-item>
        <a-form-item label="角色">
          <a-select
            v-model:value="userForm.role_ids"
            mode="multiple"
            placeholder="可选择多个角色"
            :options="delegableRoleOptions"
          />
          <small class="field-hint">多个角色的允许权限会合并，用户级明确拒绝仍然优先。</small>
        </a-form-item>
      </a-form>
    </a-modal>

    <a-modal
      v-model:open="departmentFormOpen"
      :title="editingDepartmentID ? '编辑部门' : '创建部门'"
      :confirm-loading="saving"
      :ok-text="editingDepartmentID ? '保存' : '创建'"
      cancel-text="取消"
      @ok="saveDepartment"
    >
      <a-form layout="vertical">
        <a-form-item label="部门名称" required>
          <a-input v-model:value="departmentForm.name" maxlength="64" />
        </a-form-item>
        <a-form-item label="说明">
          <a-textarea v-model:value="departmentForm.description" :rows="3" maxlength="255" />
        </a-form-item>
      </a-form>
    </a-modal>

    <a-modal
      v-model:open="userDepartmentOpen"
      :title="`调整 ${editingUser?.nickname || editingUser?.username || ''} 的部门`"
      :confirm-loading="saving"
      ok-text="保存"
      cancel-text="取消"
      @ok="saveUserDepartment"
    >
      <a-alert
        type="warning"
        show-icon
        message="调整部门只影响用户之后创建的资源；历史资源仍归原部门，避免数据意外转移。用户的现有会话会立即失效。"
        class="form-alert"
      />
      <a-form layout="vertical">
        <a-form-item label="所属部门" required>
          <a-select
            v-model:value="userDepartmentForm.department_id"
            placeholder="请选择部门"
            :options="departmentOptions"
          />
        </a-form-item>
      </a-form>
    </a-modal>

    <a-drawer
      v-model:open="roleFormOpen"
      :title="roleEditorMode === 'create' ? '创建角色' : roleEditorMode === 'basic' ? `编辑角色 · ${roleForm.display_name}` : `权限配置 · ${roleForm.display_name}`"
      width="760"
    >
      <a-form layout="vertical">
        <section v-if="roleEditorMode !== 'permissions'" class="role-basic-form">
          <a-form-item label="角色标识（技术字段）" required>
            <a-input v-model:value="roleForm.name" :disabled="Boolean(editingRoleID)" placeholder="例如：release_manager" />
          </a-form-item>
          <a-form-item label="角色名称" required>
            <a-input v-model:value="roleForm.display_name" placeholder="例如：发布管理员" />
          </a-form-item>
          <a-form-item label="说明">
            <a-textarea v-model:value="roleForm.description" :rows="3" placeholder="说明该角色的职责和适用成员" />
          </a-form-item>
        </section>

        <section v-if="roleEditorMode !== 'basic'" class="permission-editor">
          <a-alert
            type="info"
            show-icon
            message="角色是全局复用的权限集合，部门只控制业务数据范围。权限变更会立即影响关联用户的后续请求。"
          />
          <div class="permission-editor-toolbar">
            <a-input v-model:value="rolePermissionQuery" allow-clear placeholder="搜索权限名称、说明或代码">
              <template #prefix><Search :size="15" /></template>
            </a-input>
            <a-switch v-model:checked="rolePermissionOnlySelected" checked-children="只看已选" un-checked-children="全部权限" />
            <span>已选 {{ roleForm.permissions.length }} 项</span>
          </div>
          <a-empty v-if="visibleRolePermissionGroups.length === 0" description="没有符合条件的权限" />
          <section v-for="group in visibleRolePermissionGroups" :key="group.name" class="permission-group">
            <h3>{{ group.name }}</h3>
            <div v-for="resource in group.resources" :key="resource.key" class="permission-resource">
              <header>
                <a-checkbox
                  :checked="roleResourceChecked(resource)"
                  :indeterminate="roleResourceIndeterminate(resource)"
                  :disabled="!resource.items.some((item) => canDelegatePermission(item.code))"
                  @change="toggleRoleResource(resource, Boolean($event.target.checked))"
                >{{ resource.name }}</a-checkbox>
                <small>{{ resource.items.filter((item) => roleForm.permissions.includes(item.code)).length }} / {{ resource.items.length }}</small>
              </header>
              <div class="permission-options">
                <a-checkbox
                  v-for="item in resource.items"
                  :key="item.code"
                  :checked="roleForm.permissions.includes(item.code)"
                  :disabled="!canDelegatePermission(item.code)"
                  :class="{ dangerous: item.dangerous }"
                  @change="toggleRolePermission(item.code, Boolean($event.target.checked))"
                >
                  <span class="permission-name">
                    {{ permissionTitle(item) }}
                    <a-tag v-if="item.dangerous" color="error">高风险</a-tag>
                  </span>
                  <small>{{ item.description }}</small>
                  <code>{{ item.code }}</code>
                </a-checkbox>
              </div>
            </div>
          </section>
        </section>

        <a-button class="drawer-save" type="primary" block :loading="saving" @click="saveRole">
          {{ roleEditorMode === 'permissions' ? '保存权限' : roleEditorMode === 'basic' ? '保存基本信息' : '创建角色' }}
        </a-button>
      </a-form>
    </a-drawer>

    <a-drawer
      v-model:open="accessOpen"
      :title="`配置 ${editingUser?.nickname || editingUser?.username || ''}`"
      width="800"
    >
      <a-alert
        type="info"
        show-icon
        message="角色和用户权限例外会原子保存；用户级拒绝优先于角色允许，“继承”不会创建用户规则。"
      />
      <section class="access-role-section">
        <header><div><h3>角色</h3><small>可组合多个职责角色，允许权限取并集。</small></div><a-tag color="blue">{{ accessForm.role_ids.length }} 个</a-tag></header>
        <a-checkbox-group v-model:value="accessForm.role_ids" :options="delegableRoleOptions" />
      </section>
      <div class="access-impact-summary">
        <article><strong>{{ accessRolePermissionCodes.size }}</strong><span>角色继承</span></article>
        <article><strong>{{ accessOverrideCount }}</strong><span>用户例外</span></article>
        <article><strong>{{ accessEffectivePermissionCodes.size }}</strong><span>最终有效</span></article>
      </div>
      <a-collapse class="override-collapse">
        <a-collapse-panel key="overrides" :header="`用户权限例外（${accessOverrideCount}）`">
          <div class="permission-editor-toolbar">
            <a-input v-model:value="accessPermissionQuery" allow-clear placeholder="搜索权限名称、说明或代码">
              <template #prefix><Search :size="15" /></template>
            </a-input>
            <a-switch v-model:checked="accessOnlyChanged" checked-children="只看例外" un-checked-children="全部权限" />
          </div>
          <a-empty v-if="visibleAccessPermissionGroups.length === 0" description="没有符合条件的权限例外" />
          <section v-for="group in visibleAccessPermissionGroups" :key="group.name" class="override-group">
            <h3>{{ group.name }}</h3>
            <div v-for="resource in group.resources" :key="resource.key" class="override-resource">
              <h4>{{ resource.name }}</h4>
              <div v-for="item in resource.items" :key="item.code" class="override-row" :class="accessForm.effects[item.code]">
                <span>
                  <b>
                    {{ permissionTitle(item) }}
                    <a-tag v-if="item.dangerous" color="error">高风险</a-tag>
                  </b>
                  <small>{{ item.description }}</small>
                  <code>{{ item.code }}</code>
                </span>
                <a-segmented
                  v-model:value="accessForm.effects[item.code]"
                  :options="[
                    { label: '继承', value: 'inherit' },
                    { label: '允许', value: 'allow', disabled: !canDelegatePermission(item.code) },
                    { label: '拒绝', value: 'deny' },
                  ]"
                />
              </div>
            </div>
          </section>
        </a-collapse-panel>
      </a-collapse>
      <a-button class="drawer-save" type="primary" block :loading="saving" @click="saveAccess">保存角色与权限</a-button>
    </a-drawer>

    <a-drawer v-model:open="memberDrawerOpen" :title="`${memberContext.name} · 关联成员`" width="620">
      <div class="member-drawer-heading">
        <span><UsersRound :size="18" /></span>
        <div><strong>{{ associatedUsers.length }} 名用户</strong><small>{{ memberContext.kind === 'role' ? '正在使用此角色' : '当前归属此部门' }}</small></div>
      </div>
      <a-empty v-if="associatedUsers.length === 0" description="暂无关联用户" />
      <div v-else class="member-list">
        <article v-for="item in associatedUsers" :key="item.id">
          <span class="member-avatar"><UserRound :size="17" /></span>
          <div>
            <strong>{{ item.nickname || item.username }}</strong>
            <small>{{ item.username }} · {{ item.department_name || '未分配部门' }}</small>
          </div>
          <a-tag :color="item.is_active ? 'success' : 'default'">{{ item.is_active ? '启用' : '停用' }}</a-tag>
          <a-button
            v-if="!item.is_superuser && canAny(['user.update']) && canManageUserAccess(item)"
            type="link"
            @click="memberDrawerOpen = false; editAccess(item)"
          >配置权限</a-button>
        </article>
      </div>
    </a-drawer>
  </section>
</template>

<style scoped>
.access-summary {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  overflow: hidden;
  margin-bottom: 12px;
  border: 1px solid var(--edo-border);
  border-radius: 9px;
  background: var(--edo-border);
  gap: 1px;
}

.access-summary article {
  display: grid;
  grid-template-columns: 38px auto 1fr;
  align-items: center;
  min-width: 0;
  padding: 13px 16px;
  background: var(--edo-surface);
  gap: 10px;
}

.access-summary article > span,
.member-drawer-heading > span,
.member-avatar {
  display: grid;
  place-items: center;
  width: 36px;
  height: 36px;
  border-radius: 9px;
  color: var(--edo-primary);
  background: var(--edo-primary-soft);
}

.access-summary strong,
.access-summary small,
.access-summary em {
  display: block;
}

.access-summary strong {
  color: var(--edo-text);
  font-size: 19px;
  line-height: 1.1;
}

.access-summary small,
.access-summary em {
  color: var(--edo-muted);
  font-size: 11px;
}

.access-summary em {
  overflow: hidden;
  font-style: normal;
  text-align: right;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.access-filter-bar {
  display: grid;
  grid-template-columns: minmax(230px, 1fr) auto 180px 180px auto;
  align-items: center;
  margin-bottom: 12px;
  padding: 12px;
  gap: 9px;
}

.access-filter-bar.compact {
  grid-template-columns: minmax(260px, 520px) 1fr;
}

.access-filter-bar > span {
  color: var(--edo-muted);
  font-size: 12px;
  text-align: right;
  white-space: nowrap;
}

.user-identity {
  display: flex;
  align-items: center;
  gap: 9px;
}

.user-identity > span {
  display: grid;
  flex: 0 0 32px;
  place-items: center;
  width: 32px;
  height: 32px;
  border-radius: 50%;
  color: var(--edo-primary);
  background: var(--edo-primary-soft);
  font-size: 12px;
  font-weight: 700;
}

.user-identity strong,
.user-identity small {
  display: block;
}

.user-identity small,
.field-hint {
  color: var(--edo-muted);
  font-size: 11px;
}

.tag-list {
  display: flex;
  flex-wrap: wrap;
  gap: 3px;
}

.form-alert {
  margin-bottom: 18px;
}

.permission-catalog {
  display: block;
}

.permission-editor {
  min-width: 0;
}

.permission-editor-toolbar {
  display: grid;
  grid-template-columns: minmax(220px, 1fr) auto auto;
  align-items: center;
  margin: 14px 0;
  gap: 10px;
}

.permission-editor-toolbar > span {
  color: var(--edo-muted);
  font-size: 12px;
  white-space: nowrap;
}

.permission-group,
.override-group {
  margin: 16px 0;
  padding: 15px;
  border: 1px solid var(--edo-border);
  border-radius: 9px;
}

.permission-group > h3,
.override-group > h3 {
  margin: 0 0 13px;
  font-size: 15px;
}

.permission-resource,
.override-resource {
  padding: 12px 0;
  border-top: 1px solid var(--edo-border);
}

.permission-resource > header,
.override-resource h4 {
  margin: 0 0 10px;
  color: var(--edo-muted);
  font-size: 12px;
  font-weight: 600;
}

.permission-resource > header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.permission-resource > header small {
  font-weight: 400;
}

.permission-options {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
}

.permission-options :deep(.ant-checkbox-wrapper) {
  min-width: 0;
  margin-inline-start: 0;
  padding: 10px;
  border: 1px solid var(--edo-border);
  border-radius: 7px;
  align-items: flex-start;
}

.permission-options :deep(.ant-checkbox-wrapper.dangerous) {
  border-color: color-mix(in srgb, #ff4d4f 28%, var(--edo-border));
}

.permission-name,
.permission-options small,
.permission-options code,
.override-row span b,
.override-row span small,
.override-row span code {
  display: block;
}

.permission-name,
.override-row b {
  color: var(--edo-text);
}

.permission-options small,
.override-row small {
  margin-top: 3px;
  color: var(--edo-muted);
  font-size: 12px;
}

.permission-options code,
.override-row code {
  margin-top: 4px;
  color: var(--edo-muted);
  font-size: 10px;
  opacity: 0.72;
}

.permission-name :deep(.ant-tag),
.override-row b :deep(.ant-tag) {
  margin-inline-start: 6px;
}

.override-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 14px;
  padding: 10px 0;
  border-top: 1px dashed var(--edo-border);
}

.override-row.allow {
  border-left: 3px solid #28a875;
  padding-left: 10px;
}

.override-row.deny {
  border-left: 3px solid #ef5865;
  padding-left: 10px;
}

.access-role-section {
  margin-top: 18px;
  padding: 16px;
  border: 1px solid var(--edo-border);
  border-radius: 9px;
  background: var(--edo-surface-soft);
}

.access-role-section > header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  margin-bottom: 14px;
}

.access-role-section h3,
.access-role-section small {
  display: block;
  margin: 0;
}

.access-role-section small {
  color: var(--edo-muted);
  font-size: 12px;
}

.access-impact-summary {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  margin: 14px 0;
  border: 1px solid var(--edo-border);
  border-radius: 9px;
}

.access-impact-summary article {
  padding: 12px 14px;
  border-right: 1px solid var(--edo-border);
}

.access-impact-summary article:last-child {
  border-right: 0;
}

.access-impact-summary strong,
.access-impact-summary span {
  display: block;
}

.access-impact-summary strong {
  font-size: 18px;
}

.access-impact-summary span {
  color: var(--edo-muted);
  font-size: 11px;
}

.override-collapse {
  margin-bottom: 16px;
}

.drawer-save {
  margin-top: 16px;
}

.member-drawer-heading {
  display: flex;
  align-items: center;
  margin-bottom: 16px;
  padding: 14px;
  border: 1px solid var(--edo-border);
  border-radius: 9px;
  gap: 10px;
}

.member-drawer-heading strong,
.member-drawer-heading small,
.member-list strong,
.member-list small {
  display: block;
}

.member-drawer-heading small,
.member-list small {
  color: var(--edo-muted);
  font-size: 11px;
}

.member-list {
  display: grid;
  gap: 8px;
}

.member-list article {
  display: grid;
  grid-template-columns: 36px minmax(0, 1fr) auto auto;
  align-items: center;
  padding: 11px 12px;
  border: 1px solid var(--edo-border);
  border-radius: 8px;
  gap: 10px;
}

.member-avatar {
  width: 34px;
  height: 34px;
}

h3 {
  margin: 24px 0 12px;
}

@media (max-width: 650px) {
  .access-summary,
  .access-filter-bar,
  .access-filter-bar.compact,
  .permission-editor-toolbar {
    grid-template-columns: 1fr;
  }

  .access-filter-bar > span {
    text-align: left;
  }

  .access-summary article {
    grid-template-columns: 38px auto 1fr;
  }

  .permission-options {
    grid-template-columns: 1fr;
  }

  .override-row {
    align-items: flex-start;
    flex-direction: column;
  }

  .member-list article {
    grid-template-columns: 34px minmax(0, 1fr) auto;
  }

  .member-list article :deep(.ant-btn) {
    grid-column: 2 / -1;
    justify-self: start;
  }
}
</style>
