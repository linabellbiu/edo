<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { Modal, message } from 'ant-design-vue'
import { isAxiosError } from 'axios'
import {
  Building2, MoreHorizontal, Plus, RefreshCw, Search,
  ShieldCheck, UserRound, UsersRound,
} from 'lucide-vue-next'

import client from '@/api/client'
import { apiErrorMessage, isMutationAcknowledged, type ResourceRecord } from '@/api/resources'
import PageToolbar from '@/components/PageToolbar.vue'
import ResourceTable from '@/components/ResourceTable.vue'
import ClusterRbacDrawer from '@/components/ClusterRbacDrawer.vue'
import { useAuthStore } from '@/stores/auth'

const {t}=useI18n()
interface Permission {
 scope?:'project'|'platform'
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
  parent_id?: string
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
  visible_project_member_count?: number
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
  role_names: Record<string, string>
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

interface AuditPage {
  audit_logs: AuditLog[]
  has_more: boolean
  next_cursor: string
}

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

const users = ref<ManagedUser[]>([])
const departments = ref<Department[]>([])
const roles = ref<Role[]>([])
const permissions = ref<Permission[]>([])
const audits = ref<AuditLog[]>([])
const auditPageSize = 100
const auditCursorStack = ref<string[]>([''])
const auditNextCursor = ref('')
const auditHasMore = ref(false)
const auditLoading = ref(false)
const auditError = ref('')
const auditPage = computed(() => auditCursorStack.value.length)
let auditRevision = 0
let auditDisposed = false
let auditController: AbortController | undefined
const mutationIdentity = () => JSON.stringify([auth.user?.id, auth.user?.department_id, auth.user?.project_id,
  typeof localStorage === 'undefined' ? '' : localStorage.getItem('edo.project') ?? ''])
let mutationIdentityRevision = 0
watch(mutationIdentity, () => { mutationIdentityRevision++ }, { flush: 'sync' })
function beginMutation() {
  const identity = mutationIdentity(), revision = mutationIdentityRevision
  return () => !auditDisposed && revision === mutationIdentityRevision && identity === mutationIdentity()
}
function mutationError(error: unknown, current: () => boolean) {
  const acknowledged = isMutationAcknowledged(error)
  if (!current()) return acknowledged
  if (acknowledged) {
    message.info(apiErrorMessage(error))
    if (authorizationReady.value) void (active.value === 'audit' ? loadAuditPage(restoreAuditPosition()) : refresh())
  } else message.error(apiErrorMessage(error))
  return acknowledged
}
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
const userSelectOpen = reactive({ department: false, roles: false })
const departmentFormOpen = ref(false)
const userDepartmentOpen = ref(false)
const roleFormOpen = ref(false)
const accessOpen = ref(false)
const memberDrawerOpen = ref(false)
const clusterRbacOpen = ref(false),clusterRbacRoleID = ref('')
function openClusterRbac(role:Role){clusterRbacRoleID.value=role.id;clusterRbacOpen.value=true}
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
const departmentForm = reactive({ name: '', description: '',parent_id:'' })
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

const authorizationReady = computed(() => auth.authorizationState !== 'checking' && auth.authorizationState !== 'unavailable')
function changeUserSelectOpen(field: 'department' | 'roles', open: boolean) {
  userSelectOpen[field] = Boolean(open && userFormOpen.value && authorizationReady.value && (field !== 'department' || auth.user?.is_superuser))
}
function userSelectKeydown(field: 'department' | 'roles', event: KeyboardEvent) {
  if (event.key === 'Escape' && userSelectOpen[field]) {
    event.stopPropagation()
    event.preventDefault()
    userSelectOpen[field] = false
  }
}
watch([userFormOpen, () => auth.user?.is_superuser], ([open, superuser]) => {
  if (!open) userSelectOpen.department = userSelectOpen.roles = false
  else if (!superuser) userSelectOpen.department = false
}, { flush: 'sync' })
const knownPermission = (codes: string[]) => Boolean(auth.user?.is_superuser || codes.some(code => auth.permissions.has(code)))
const tabs = computed(() => [
  ...(knownPermission(['user.read', 'user.create', 'user.update', 'user.delete'])
    ? [{ key: 'users', label: '用户管理' }]
    : []),
  ...(knownPermission(['department.read', 'department.create', 'department.update', 'department.delete'])
    ? [{ key: 'departments', label: '部门管理' }]
    : []),
  ...(knownPermission(['role.read', 'role.create', 'role.update', 'role.delete'])
    ? [{ key: 'roles', label: '角色与权限' }]
    : []),
  ...(knownPermission(['audit.read']) ? [{ key: 'audit', label: '审计日志' }] : []),
])

const active = computed(() =>
  tabs.value.some((item) => item.key === route.query.view)
    ? String(route.query.view)
    : tabs.value[0]?.key || '',
)

const auditScope = computed(() => JSON.stringify([
  auth.user?.id,
  auth.user?.department_id,
  auth.user?.project_id,
  auth.user?.is_superuser,
  [...auth.permissions].sort(),
]))
const auditContext = computed(() => JSON.stringify([route.query.view, active.value, auditScope.value]))
interface AuditPosition { position: string; cursors: string[]; savedAt: number }
interface AuditPositions { scope: string; positions: AuditPosition[] }
const auditPositionKey = 'edo.audit.positions'
const auditPositionTTL = 30 * 60_000
const auditPositionLimit = 40, auditPositionCursorLimit = 1000, auditPositionByteLimit = 256 * 1024
let auditPositionsMemory: AuditPositions | undefined
let lastAuditScope = auditScope.value
const auditRouteWrites = new Map<string, number>()

function validAuditCursors(value: unknown): value is string[] {
  if (!Array.isArray(value) || !value.length || value.length > auditPositionCursorLimit || value[0] !== '') return false
  return value.slice(1).every((cursor, index) => typeof cursor === 'string' && /^[1-9]\d{0,18}$/.test(cursor)
    && BigInt(cursor) <= 9223372036854775807n && (index === 0 || BigInt(value[index]) > BigInt(cursor)))
}

function clearAuditPositions() {
  auditPositionsMemory = { scope: auditScope.value, positions: [] }
  try { window.sessionStorage?.removeItem(auditPositionKey) } catch { /* 不可用的会话存储不影响权限清场。 */ }
}

function readAuditPositions(): AuditPositions {
  const now = Date.now(), scope = auditScope.value
  if (!auditPositionsMemory || auditPositionsMemory.scope !== scope) {
    try {
      const raw = window.sessionStorage?.getItem(auditPositionKey)
      if (raw && new TextEncoder().encode(raw).byteLength <= auditPositionByteLimit) {
        const value = JSON.parse(raw)
        if (value?.scope === scope && Array.isArray(value.positions) && value.positions.length <= auditPositionLimit
          && Object.keys(value).sort().join() === 'positions,scope'
          && value.positions.every((item: AuditPosition) => item && Object.keys(item).sort().join() === 'cursors,position,savedAt'
            && typeof item.position === 'string' && /^[a-f0-9]{24}$/.test(item.position) && validAuditCursors(item.cursors)
            && Number.isSafeInteger(item.savedAt) && item.savedAt <= now)
          && new Set(value.positions.map((item: AuditPosition) => item.position)).size === value.positions.length) auditPositionsMemory = value
      }
    } catch { /* 损坏或禁用的存储只丢弃浏览位置，不保留审计正文。 */ }
    if (!auditPositionsMemory || auditPositionsMemory.scope !== scope) clearAuditPositions()
  }
  const cache = auditPositionsMemory!, retained = cache.positions.filter(item => now - item.savedAt < auditPositionTTL)
  if (retained.length !== cache.positions.length) {
    cache.positions = retained
    try {
      if (retained.length) window.sessionStorage?.setItem(auditPositionKey, JSON.stringify(cache))
      else window.sessionStorage?.removeItem(auditPositionKey)
    } catch { /* 过期位置在内存中同样不可恢复。 */ }
  }
  return auditPositionsMemory!
}

function replaceAuditPosition(position?: string) {
  if (route.query.audit_position === position) return
  const context = auditContext.value, key = JSON.stringify([context, position ?? null])
  const query = { ...route.query }
  if (position) query.audit_position = position
  else delete query.audit_position
  auditRouteWrites.set(key, (auditRouteWrites.get(key) || 0) + 1)
  void router.replace({ query, hash: route.hash }).catch(() => { /* 导航取消不改变已经鉴权取得的当前页。 */ }).finally(() => {
    const remaining = (auditRouteWrites.get(key) || 1) - 1
    if (remaining) auditRouteWrites.set(key, remaining)
    else auditRouteWrites.delete(key)
  })
}

function restoreAuditPosition(): string[] {
  const position = route.query.audit_position
  const entry = typeof position === 'string' && /^[a-f0-9]{24}$/.test(position)
    ? readAuditPositions().positions.find(item => item.position === position) : undefined
  if (!entry) { replaceAuditPosition(); return [''] }
  return [...entry.cursors]
}

function saveAuditPosition(cursors: string[]) {
  if (cursors.length === 1 || !validAuditCursors(cursors)) { replaceAuditPosition(); return }
  const cache = readAuditPositions(), existing = cache.positions.find(item => item.position === route.query.audit_position)
    || cache.positions.find(item => JSON.stringify(item.cursors) === JSON.stringify(cursors))
  let position = existing?.position
  if (!position) {
    try { position = Array.from(window.crypto.getRandomValues(new Uint8Array(12)), value => value.toString(16).padStart(2, '0')).join('') }
    catch { replaceAuditPosition(); return }
    if (cache.positions.some(item => item.position === position)) { replaceAuditPosition(); return }
  }
  const entry = { position, cursors: [...cursors], savedAt: Date.now() }
  const positions = [...cache.positions.filter(item => item.position !== position), entry].slice(-auditPositionLimit)
  const next = { scope: auditScope.value, positions }
  while (new TextEncoder().encode(JSON.stringify(next)).byteLength > auditPositionByteLimit && positions.length > 1) positions.shift()
  if (new TextEncoder().encode(JSON.stringify(next)).byteLength > auditPositionByteLimit) { replaceAuditPosition(); return }
  auditPositionsMemory = next
  try { window.sessionStorage?.setItem(auditPositionKey, JSON.stringify(next)) } catch { /* 当前组件仍保留有界游标，刷新时安全回到首页。 */ }
  replaceAuditPosition(position)
}

const canReadUsers = computed(() => canAny(['user.read', 'user.create', 'user.update', 'user.delete']))
const canAccessDepartments = computed(() => canAny([
  'department.read', 'department.create', 'department.update', 'department.delete', 'user.create', 'user.update',
]))
const canAccessRoles = computed(() => canAny([
  'role.read', 'role.create', 'role.update', 'role.delete', 'user.create', 'user.update',
]))
const activeDepartments = computed(() => departments.value.filter((item) => item.is_active))
const departmentOptions = computed(() => activeDepartments.value.map((item) => ({ value: item.id, label: item.name })))
const userRoleLabels = computed(() => new Map(users.value.map((item) => [item.id,
  item.role_ids.map((id) => ({ id, name: item.role_names?.[id]?.trim() || '未知角色' })),
])))
const userRoleOptions = computed(() => {
  const names = new Map<string, string>()
  userRoleLabels.value.forEach((items) => items.forEach(({ id, name }) => names.set(id, name)))
  return [...names].map(([value, label]) => ({ value, label }))
})
const delegableRoleOptions = computed(() => roles.value.map((item) => {
  const projectOnly=item.permissions.length>0&&item.permissions.every(code=>permissions.value.find(permission=>permission.code===code)?.scope==='project')
  const disabled = projectOnly || !item.permissions.every((permission) => canDelegatePermission(permission))
  return {
    value: item.id,
    label: projectOnly ? `${item.display_name}（${t('workspace.projectScope')}）` : disabled ? `${item.display_name}（超出可委派范围）` : item.display_name,
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
    const roleNames = userRoleLabels.value.get(item.id)?.map((role) => role.name).join(' ') || ''
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
  if (active.value === 'departments') return t('workspace.departmentHint')
  return '维护账户所属部门、状态、角色和用户级允许或拒绝例外。'
})

function canAny(required: string[]) {
  return authorizationReady.value && auth.canAny(required)
}

function canDelegatePermission(permission: string) {
  return authorizationReady.value && Boolean(auth.user?.is_superuser || auth.permissions.has(permission))
}

function canManageUserAccess(item: ManagedUser) {
  if (!authorizationReady.value) return false
  if (auth.user?.is_superuser) return true
  if (item.id === auth.user?.id) return false
  const rolesDelegable = item.role_ids.every((id) => roleMap.value.get(id)?.permissions.every(canDelegatePermission))
  return rolesDelegable && item.permission_overrides.allow.every(canDelegatePermission)
}

function canManageRolePermissions(item: Role) {
  return authorizationReady.value && Boolean(auth.user?.is_superuser || item.permissions.every(canDelegatePermission))
}

function userInitial(row: ResourceRecord) {
  const value = String(row.nickname || row.username || '?')
  return value.slice(0, 1).toLocaleUpperCase()
}

function auditResult(value: unknown) {
  if (value === 'succeeded') return { label: '成功', color: 'success' }
  if (value === 'failed') return { label: '失败', color: 'error' }
  if (value === 'denied') return { label: '拒绝', color: 'warning' }
  return { label: '未知', color: 'default' }
}

let accessRevision = 0
async function refresh() {
  if (!authorizationReady.value || auditDisposed) return
  if (active.value === 'audit') {
    await loadAuditPage([''])
    return
  }
  loading.value = true
  const revision = ++accessRevision, context = auditScope.value
  const current = () => !auditDisposed && authorizationReady.value && revision === accessRevision && context === auditScope.value
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
    const [userResponse, departmentResponse, roleResponse, permissionResponse] =
      await Promise.all([
        canLoadUsers ? loadAllUsers(current) : null,
        canLoadDepartments ? client.get<{ departments: Department[] }>('/departments') : null,
        canLoadRoles ? client.get<{ roles: Role[] }>('/roles') : null,
        canLoadPermissions ? client.get<{ permissions: Permission[] }>('/permissions') : null,
      ])
    if (!current()) return
    users.value = userResponse || []
    departments.value = departmentResponse?.data.departments || []
    roles.value = roleResponse?.data.roles || []
    permissions.value = permissionResponse?.data.permissions || []
  } catch (error) {
    if (current()) message.error(apiErrorMessage(error))
  } finally {
    if (revision === accessRevision) loading.value = false
  }
}

function resetAuditPagination(preservePosition = false) {
  auditRevision += 1
  auditController?.abort()
  auditController = undefined
  audits.value = []
  if (!preservePosition) auditCursorStack.value = ['']
  auditNextCursor.value = ''
  auditHasMore.value = false
  auditLoading.value = false
  auditError.value = ''
}

async function loadAuditPage(cursors: string[]) {
  if (auditDisposed || active.value !== 'audit' || !canAny(['audit.read'])) return
  auditController?.abort()
  const controller = new AbortController()
  auditController = controller
  const revision = ++auditRevision
  const context = auditContext.value
  const nextStack = [...cursors]
  const cursor = nextStack.at(-1) || ''
  const isCurrent = () => !auditDisposed && revision === auditRevision && !controller.signal.aborted
    && context === auditContext.value && active.value === 'audit' && canAny(['audit.read'])
  auditLoading.value = true
  auditError.value = ''
  try {
    const response = await client.get<AuditPage>(
      `/audit-logs?limit=${auditPageSize}${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`,
      { signal: controller.signal },
    )
    if (!isCurrent()) return
    const nextCursor = typeof response.data.next_cursor === 'string' ? response.data.next_cursor : ''
    audits.value = response.data.audit_logs || []
    auditCursorStack.value = nextStack
    auditNextCursor.value = nextCursor
    auditHasMore.value = Boolean(response.data.has_more && nextCursor)
    if (isCurrent()) saveAuditPosition(nextStack)
  } catch (error) {
    if (!isCurrent()) return
    if (isAxiosError(error) && (error.response?.status === 401 || error.response?.status === 403)) {
      clearAuditPositions()
      resetAuditPagination()
      replaceAuditPosition()
    }
    auditError.value = apiErrorMessage(error)
    message.error(auditError.value)
  } finally {
    if (revision === auditRevision) {
      auditLoading.value = false
      auditController = undefined
    }
  }
}

async function nextAuditPage() {
  if (auditLoading.value || !auditHasMore.value || !auditNextCursor.value) return
  await loadAuditPage([...auditCursorStack.value, auditNextCursor.value])
}

async function previousAuditPage() {
  if (auditLoading.value || auditCursorStack.value.length <= 1) return
  await loadAuditPage(auditCursorStack.value.slice(0, -1))
}

async function loadAllUsers(current: () => boolean = () => true) {
  const pageSize = 200
  const result: ManagedUser[] = []
  for (let offset = 0; ; offset += pageSize) {
    const response = await client.get<{ users: ManagedUser[] }>(`/users?limit=${pageSize}&offset=${offset}`)
    if (!current()) return []
    const page = response.data.users || []
    result.push(...page)
    if (page.length < pageSize) return result
  }
}

function selectTab(key: string) {
  const query: typeof route.query = { ...route.query, view: key }
  if (key !== 'audit') delete query.audit_position
  void router.replace({ query })
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
  if (!authorizationReady.value) return
  if (!userForm.department_id) {
    message.warning('请选择用户所属部门')
    return
  }
  const current = beginMutation()
  saving.value = true
  try {
    const response = await client.post<{ warning?: string }>('/users', userForm)
    if (!current()) return
    if (response.data.warning) message.warning(response.data.warning)
    else message.success('用户已创建')
    userFormOpen.value = false
    resetUser()
    await refresh()
  } catch (error) {
    mutationError(error, current)
  } finally {
    saving.value = false
  }
}

function resetDepartment() {
  Object.assign(departmentForm, { name: '', description: '',parent_id:'' })
  editingDepartmentID.value = ''
}

function editDepartment(item: Department) {
  Object.assign(departmentForm, { name: item.name, description: item.description || '',parent_id:item.parent_id||'' })
  editingDepartmentID.value = item.id
  departmentFormOpen.value = true
}

async function saveDepartment() {
  if (!authorizationReady.value) return
  if (!departmentForm.name.trim()) {
    message.warning('请输入部门名称')
    return
  }
  const current = beginMutation()
  saving.value = true
  try {
    if (editingDepartmentID.value) {
      await client.put(`/departments/${editingDepartmentID.value}`, departmentForm)
      if (!current()) return
      message.success('部门已更新')
    } else {
      await client.post('/departments', departmentForm)
      if (!current()) return
      message.success('部门已创建')
    }
    departmentFormOpen.value = false
    resetDepartment()
    await refresh()
  } catch (error) {
    mutationError(error, current)
  } finally {
    saving.value = false
  }
}

function removeDepartment(item: Department) {
  const current = beginMutation()
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
      if (!current() || !authorizationReady.value) return
      try {
        await client.delete(`/departments/${item.id}`)
        if (!current()) return
        message.success('部门已删除')
        await refresh()
      } catch (error) {
        if (!mutationError(error, current)) throw error
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
  if (!authorizationReady.value) return
  if (!editingUser.value || !userDepartmentForm.department_id) {
    message.warning('请选择用户所属部门')
    return
  }
  const current = beginMutation()
  saving.value = true
  try {
    await client.put(`/users/${editingUser.value.id}/department`, userDepartmentForm)
    if (!current()) return
    message.success('用户所属部门已更新')
    userDepartmentOpen.value = false
    await refresh()
  } catch (error) {
    mutationError(error, current)
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
  if (!authorizationReady.value) return
  const current = beginMutation()
  saving.value = true
  try {
    let warning = ''
    if (!editingRoleID.value) {
      const response = await client.post<{ warning?: string }>('/roles', roleForm)
      if (!current()) return
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
    if (!current()) return
    if (warning) message.warning(warning)
    else message.success(!editingRoleID.value ? '角色已创建' : roleEditorMode.value === 'basic' ? '角色基本信息已更新' : '角色权限已更新')
    roleFormOpen.value = false
    resetRole()
    await refresh()
  } catch (error) {
    mutationError(error, current)
  } finally {
    saving.value = false
  }
}

function removeRole(item: Role) {
  const current = beginMutation()
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
      if (!current() || !authorizationReady.value) return
      try {
        const response = await client.delete<{ warning?: string }>(`/roles/${item.id}`)
        if (!current()) return
        if (response.data.warning) message.warning(response.data.warning)
        else message.success('角色已删除')
        await refresh()
      } catch (error) {
        if (!mutationError(error, current)) throw error
      }
    },
  })
}

function removeUser(item: ManagedUser) {
  const current = beginMutation()
  Modal.confirm({
    title: `删除用户“${item.nickname || item.username}”？`,
    content: '账户、个人 Git 令牌和权限配置会被删除；部门业务资源和审计记录会保留。若个人令牌仍被仓库引用，需要先为仓库更换凭据。',
    okText: '删除',
    okType: 'danger',
    cancelText: '取消',
    async onOk() {
      if (!current() || !authorizationReady.value) return
      try {
        await client.delete(`/users/${item.id}`)
        if (!current()) return
        message.success('用户已删除')
        await refresh()
      } catch (error) {
        if (!mutationError(error, current)) throw error
      }
    },
  })
}

function toggleUser(item: ManagedUser) {
  const current = beginMutation()
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
      if (!current() || !authorizationReady.value) return
      try {
        await client.patch(`/users/${item.id}/status`, { active: !item.is_active })
        if (!current()) return
        message.success(`用户已${action}`)
        await refresh()
      } catch (error) {
        if (!mutationError(error, current)) throw error
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
  if (!authorizationReady.value) return
  if (!editingUser.value) return
  const current = beginMutation()
  saving.value = true
  try {
    const allow = Object.entries(accessForm.effects)
      .filter(([, effect]) => effect === 'allow')
      .map(([permission]) => permission)
    const deny = Object.entries(accessForm.effects)
      .filter(([, effect]) => effect === 'deny')
      .map(([permission]) => permission)
    await client.put(`/users/${editingUser.value.id}/access`, { role_ids: accessForm.role_ids, allow, deny })
    if (!current()) return
    message.success('用户权限已更新')
    accessOpen.value = false
    await refresh()
  } catch (error) {
    mutationError(error, current)
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
  (value, previous) => {
    if (!authorizationReady.value) return
    if (value && route.query.view !== value) selectTab(value)
    if (previous === 'audit' && value && value !== 'audit') void refresh()
  },
  { immediate: true },
)
function closeAccessEditors() {
  userFormOpen.value = departmentFormOpen.value = roleFormOpen.value = accessOpen.value = false
  userDepartmentOpen.value = memberDrawerOpen.value = clusterRbacOpen.value = false
  editingUser.value = null
  Modal.destroyAll()
}
watch(() => JSON.stringify([auditContext.value, route.query.audit_position ?? null, auth.authorizationState, mutationIdentity()]), () => {
  const scopeChanged = lastAuditScope !== auditScope.value
  lastAuditScope = auditScope.value
  if (scopeChanged) clearAuditPositions()
  const selectedProject = typeof localStorage === 'undefined' ? '' : localStorage.getItem('edo.project') ?? ''
  // 同一身份的短暂核验只暂停操作；真正失权、断线或切换范围仍关闭旧编辑上下文。
  if (scopeChanged || !auth.user || selectedProject !== (auth.user.project_id ?? '') || auth.authorizationState === 'unavailable') closeAccessEditors()
  if (!authorizationReady.value) {
    resetAuditPagination(!scopeChanged)
    accessRevision++
    loading.value = false
    users.value = []; departments.value = []; roles.value = []; permissions.value = []
    userSelectOpen.department = userSelectOpen.roles = false
    memberDrawerOpen.value = clusterRbacOpen.value = false
    Modal.destroyAll()
    return
  }
  const internalWrite = JSON.stringify([auditContext.value, route.query.audit_position ?? null])
  if (!scopeChanged && auditRouteWrites.has(internalWrite)) return
  resetAuditPagination()
  if (active.value === 'audit' && canAny(['audit.read'])) {
    if (scopeChanged) replaceAuditPosition()
    void loadAuditPage(scopeChanged ? [''] : restoreAuditPosition())
  } else {
    replaceAuditPosition()
    void refresh()
  }
}, { flush: 'sync' })
onBeforeUnmount(() => {
  auditDisposed = true
  accessRevision++
  resetAuditPagination()
})
onMounted(() => {
  if (active.value === 'audit') return loadAuditPage(restoreAuditPosition())
  replaceAuditPosition()
  return refresh()
})
</script>

<template>
  <section>
    <PageToolbar :description="toolbarDescription">
      <a-button :loading="loading || auditLoading" :disabled="!authorizationReady" @click="refresh">
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
      :disabled="!authorizationReady"
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
        <em>{{ canAccessDepartments ? '组织归属' : '无权查看' }}</em>
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
      <a-select v-model:value="userRoleID" allow-clear placeholder="全部角色" :options="userRoleOptions" />
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
            <a-tag v-for="role in userRoleLabels.get(String(row.id)) || []" :key="role.id">
              {{ role.name }}
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
          <template v-if="canReadUsers && value !== undefined">
            <a-button type="link" @click="openMembers('role', row as Role)">{{t('workspace.platformCount',{count:value||0})}}</a-button>
            <span v-if="row.visible_project_member_count" class="field-hint">{{t('workspace.projectCount',{count:row.visible_project_member_count})}}</span>
          </template>
          <span v-else>无权查看</span>
        </template>
        <template #cell-permissions="{ value }">
          {{ Array.isArray(value) ? value.length : 0 }} 项
        </template>
        <template #detail-permissions="{ value }">
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
          <a-button v-if="canAny(['kubernetes.rbac.read','kubernetes.rbac.update'])" type="link" @click="openClusterRbac(row as Role)">集群授权</a-button>
          <a-button
            v-if="canAny(['role.delete'])"
            type="link"
            danger
            :disabled="Boolean(row.in_use)"
            :title="row.in_use ? '仍被成员或集群授权使用，不能删除' : '删除角色'"
            @click="removeRole(row as Role)"
          >删除</a-button>
        </template>
      </ResourceTable>

      <template v-else-if="active === 'audit'">
        <a-alert v-if="auditError" type="error" :message="auditError" show-icon class="audit-error" />
        <ResourceTable :rows="audits" :columns="auditColumns" :loading="auditLoading" :pagination="false">
          <template #cell-result="{ value }">
            <a-tag :color="auditResult(value).color">{{ auditResult(value).label }}</a-tag>
          </template>
        </ResourceTable>
        <div class="audit-pagination" aria-label="审计日志分页">
          <span>第 {{ auditPage }} 页 · 本页 {{ audits.length }} 条（每页 {{ auditPageSize }} 条）</span>
          <a-space>
            <a-button :disabled="!authorizationReady || auditLoading || auditPage <= 1" @click="previousAuditPage">上一页</a-button>
            <a-button :disabled="!authorizationReady || auditLoading || !auditHasMore" @click="nextAuditPage">下一页</a-button>
          </a-space>
        </div>
      </template>
    </div>

    <a-modal
      v-model:open="userFormOpen"
      wrap-class-name="access-user-modal"
      title="创建用户"
      :confirm-loading="saving"
      :ok-button-props="{ disabled: !authorizationReady }"
      ok-text="创建"
      cancel-text="取消"
      @ok="createUser"
    >
      <a-form :model="userForm" name="access-user" layout="vertical" :disabled="!authorizationReady">
        <a-form-item name="username" label="用户名" required>
          <a-input v-model:value="userForm.username" :maxlength="32" />
        </a-form-item>
        <a-form-item name="nickname" label="昵称">
          <a-input v-model:value="userForm.nickname" :maxlength="64" />
        </a-form-item>
        <a-form-item name="department_id" label="所属部门" required>
          <a-select
            v-model:value="userForm.department_id"
            placeholder="请选择部门"
            :disabled="!authorizationReady || !auth.user?.is_superuser"
            :options="departmentOptions"
            :open="userSelectOpen.department"
            @dropdown-visible-change="(open: boolean) => changeUserSelectOpen('department', open)"
            @input-key-down="(event: KeyboardEvent) => userSelectKeydown('department', event)"
          />
          <small class="field-hint">普通管理员只能在自己的部门创建用户。</small>
        </a-form-item>
        <a-form-item name="password" label="初始密码" required>
          <a-input-password v-model:value="userForm.password" minlength="12" :maxlength="128" />
        </a-form-item>
        <a-form-item name="role_ids" :label="t('workspace.platformRoles')">
          <a-select
            v-model:value="userForm.role_ids"
            mode="multiple"
            :disabled="!authorizationReady"
            placeholder="可选择多个角色"
            :options="delegableRoleOptions"
            :open="userSelectOpen.roles"
            @dropdown-visible-change="(open: boolean) => changeUserSelectOpen('roles', open)"
            @input-key-down="(event: KeyboardEvent) => userSelectKeydown('roles', event)"
          />
          <small class="field-hint">{{t('workspace.platformRoleHint')}}</small>
        </a-form-item>
      </a-form>
    </a-modal>

    <a-modal
      v-model:open="departmentFormOpen"
      :title="editingDepartmentID ? '编辑部门' : '创建部门'"
      :confirm-loading="saving"
      :ok-button-props="{ disabled: !authorizationReady }"
      :ok-text="editingDepartmentID ? '保存' : '创建'"
      cancel-text="取消"
      @ok="saveDepartment"
    >
      <a-form :model="departmentForm" name="access-department" layout="vertical" :disabled="!authorizationReady">
        <a-form-item name="name" label="部门名称" required>
          <a-input v-model:value="departmentForm.name" :maxlength="64" />
        </a-form-item>
        <a-form-item name="description" label="说明">
          <a-textarea v-model:value="departmentForm.description" :rows="3" :maxlength="255" />
        </a-form-item>
      </a-form>
    </a-modal>

    <a-modal
      v-model:open="userDepartmentOpen"
      :title="`调整 ${editingUser?.nickname || editingUser?.username || ''} 的部门`"
      :confirm-loading="saving"
      :ok-button-props="{ disabled: !authorizationReady }"
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
      <a-form layout="vertical" :disabled="!authorizationReady">
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
      <a-form layout="vertical" :disabled="!authorizationReady">
        <section v-if="roleEditorMode !== 'permissions'" class="role-basic-form">
          <a-form-item label="角色标识（技术字段）" required>
            <a-input v-model:value="roleForm.name" :disabled="!authorizationReady || Boolean(editingRoleID)" placeholder="例如：release_manager" />
          </a-form-item>
          <a-form-item label="角色名称" required>
            <a-input v-model:value="roleForm.display_name" placeholder="例如：发布管理员" />
          </a-form-item>
          <a-form-item label="说明">
            <a-textarea v-model:value="roleForm.description" :rows="3" placeholder="例如：负责应用构建和发布" />
          </a-form-item>
        </section>

        <section v-if="roleEditorMode !== 'basic'" class="permission-editor">
          <a-alert
            type="info"
            show-icon
            message="修改后，对已分配此角色的用户立即生效。项目角色请在项目成员中分配。"
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
                <small><a-tag>{{t(resource.items[0]?.scope==='project'?'workspace.projectScope':'workspace.platformScope')}}</a-tag>{{ resource.items.filter((item) => roleForm.permissions.includes(item.code)).length }} / {{ resource.items.length }}</small>
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

        <a-button class="drawer-save" type="primary" block :loading="saving" :disabled="!authorizationReady" @click="saveRole">
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
        <header><div><h3>{{t('workspace.platformRoles')}}</h3><small>{{t('workspace.platformRoleHint')}}</small></div><a-tag color="blue">{{ accessForm.role_ids.length }} 个</a-tag></header>
        <a-checkbox-group v-model:value="accessForm.role_ids" :options="delegableRoleOptions" :disabled="!authorizationReady" />
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
                  :disabled="!authorizationReady"
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
      <a-button class="drawer-save" type="primary" block :loading="saving" :disabled="!authorizationReady" @click="saveAccess">保存角色与权限</a-button>
    </a-drawer>

    <ClusterRbacDrawer v-model:open="clusterRbacOpen" :role-id="clusterRbacRoleID" />
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
:global(.access-user-modal .ant-select-selection-placeholder) {
  color: var(--edo-muted);
}
:global(.access-user-modal .ant-select-single.ant-select-open .ant-select-selection-item) {
  color: var(--edo-muted);
}
:global(.access-user-modal .ant-modal-footer .ant-btn-primary:not(:disabled)) {
  --access-user-action-bg: color-mix(in srgb, var(--edo-primary) 84%, var(--edo-text));
  color: var(--edo-surface);
  background: var(--access-user-action-bg);
  border-color: var(--access-user-action-bg);
}
:global(.access-user-modal .ant-modal-footer .ant-btn-primary:not(:disabled):hover) {
  --access-user-action-bg: color-mix(in srgb, var(--edo-primary) 76%, var(--edo-text));
}
:global(.access-user-modal .ant-modal-footer .ant-btn-primary:not(:disabled):active) {
  --access-user-action-bg: color-mix(in srgb, var(--edo-primary) 70%, var(--edo-text));
}
.audit-error {
  margin-bottom: 12px;
}

.audit-pagination {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-top: 12px;
  color: var(--edo-muted);
}

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
