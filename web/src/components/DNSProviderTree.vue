<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ChevronDown, MoreHorizontal, Plus, Search } from 'lucide-vue-next'

const props = defineProps<{
  providers: Array<{ code: string; name: string }>
  accounts: Array<{ id: string; name: string; provider: string; is_active: boolean }>
  provider: string
  selected: string[]
  loading?: boolean
  canCreate?: boolean
  canUpdate?: boolean
  canDelete?: boolean
}>()
const emit = defineEmits<{ selection: [provider: string, accounts: string[]]; add: []; manage: [accountID: string, action: 'edit' | 'toggle' | 'remove'] }>()
const search = ref(''), expanded = ref<string[]>([]), mobileOpen = ref(false)
const catalog = computed(() => {
  const rows = props.providers.filter(item => props.accounts.some(account => account.provider === item.code)).map(item => ({ code: item.code, name: item.name }))
  for (const account of props.accounts) {
    if (!rows.some(item => item.code === account.provider)) rows.push({ code: account.provider, name: account.provider })
  }
  return rows
})
const all = computed(() => props.selected.includes('*'))
const selectionLabel = computed(() => {
  if (!props.provider) return '全部厂商'
  const name = catalog.value.find(item => item.code === props.provider)?.name || props.provider
  return `${name} · ${all.value ? '全部账号' : `已选 ${props.selected.length} 个账号`}`
})
const branches = computed(() => {
  const needle = search.value.trim().toLowerCase()
  return catalog.value.flatMap(provider => {
    const accounts = props.accounts.filter(item => item.provider === provider.code)
    const matches = !needle || `${provider.name} ${provider.code}`.toLowerCase().includes(needle)
    const children = accounts.filter(item => matches || item.name.toLowerCase().includes(needle)).map(item => ({
      key: `account:${item.id}`, accountID: item.id, title: item.name, inactive: !item.is_active,
    }))
    return matches || children.length ? [{ key: `provider:${provider.code}`, title: provider.name, count: accounts.length, children }] : []
  })
})
const treeData = computed(() => catalog.value.length ? [{ key: 'all', title: '全部厂商', count: props.accounts.length, checkable: false }, ...branches.value] : [])
const expandedKeys = computed(() => search.value.trim() ? branches.value.map(item => item.key) : expanded.value)
const checkedKeys = computed(() => {
  if (!props.provider) return { checked: [], halfChecked: [] }
  const accounts = props.accounts.filter(item => item.provider === props.provider)
  const selected = accounts.filter(item => all.value || props.selected.includes(item.id))
  const complete = all.value || accounts.length > 0 && selected.length === accounts.length
  const key = `provider:${props.provider}`
  return { checked: [...(complete ? [key] : []), ...selected.map(item => `account:${item.id}`)], halfChecked: !complete && selected.length ? [key] : [] }
})
const selectedKeys = computed(() => !props.provider ? ['all'] : !all.value && props.selected.length === 1 ? [`account:${props.selected[0]}`] : [`provider:${props.provider}`])
watch(() => props.provider, provider => {
  const key = `provider:${provider}`
  if (provider && !expanded.value.includes(key)) expanded.value.push(key)
}, { immediate: true })

function selectNode(_keys: unknown, event: { node: { key?: string | number } }) {
  const key = String(event.node.key)
  if (key === 'all') emit('selection', '', ['*'])
  else if (key.startsWith('provider:')) emit('selection', key.slice(9), ['*'])
  else {
    const account = props.accounts.find(item => `account:${item.id}` === key)
    if (account) emit('selection', account.provider, [account.id])
  }
}
function checkNode(_keys: unknown, event: { node: { key?: string | number }; checked?: boolean }) {
  const key = String(event.node.key)
  if (key.startsWith('provider:')) {
    emit('selection', key.slice(9), event.checked ? ['*'] : [])
    return
  }
  const account = props.accounts.find(item => `account:${item.id}` === key)
  if (!account) return
  const selected = new Set(account.provider === props.provider ? all.value ? props.accounts.filter(item => item.provider === props.provider).map(item => item.id) : props.selected : [])
  if (event.checked) selected.add(account.id)
  else selected.delete(account.id)
  emit('selection', account.provider, [...selected])
}
function expandNodes(keys: Array<string | number>) { expanded.value = keys.map(String) }
function manageAccount(id: string, action: 'edit' | 'toggle' | 'remove') {
  if (props.loading || !props.accounts.some(item => item.id === id)) return
  if (action === 'remove' ? !props.canDelete : !props.canUpdate) return
  emit('manage', id, action)
}
</script>

<template>
  <aside class="dns-provider-tree" aria-label="DNS 厂商与账号">
    <div class="dns-tree-heading"><h3>域名厂商</h3><button v-if="canCreate" class="dns-tree-add" type="button" :disabled="loading || !providers.length" aria-label="添加域名厂商" @click="emit('add')"><Plus :size="14" />添加</button></div>
    <button v-if="catalog.length" class="dns-tree-toggle" type="button" :aria-expanded="mobileOpen" aria-controls="dns-provider-options" @click="mobileOpen = !mobileOpen"><span>{{ selectionLabel }}</span><ChevronDown :size="14" /></button>
    <div id="dns-provider-options" class="dns-tree-options" :class="{ 'is-open': mobileOpen || !catalog.length }">
      <a-input v-if="catalog.length" v-model:value="search" allow-clear placeholder="搜索厂商或账号" aria-label="搜索厂商或账号"><template #prefix><Search :size="14" /></template></a-input>
      <a-spin :spinning="Boolean(loading)">
        <a-tree block-node checkable check-strictly :disabled="loading" :tree-data="treeData" :checked-keys="checkedKeys" :selected-keys="selectedKeys" :expanded-keys="expandedKeys" @select="selectNode" @check="checkNode" @expand="expandNodes">
          <template #title="{ title, count, inactive, accountID }"><span class="dns-tree-title" :title="String(title)"><span>{{ title }}</span><small v-if="inactive">已停用</small><span v-if="count !== undefined" class="dns-tree-count">{{ count }}</span><a-dropdown v-if="accountID && (canUpdate || canDelete)" :trigger="['click']" placement="bottomRight"><button class="dns-tree-manage" type="button" :disabled="loading" :aria-label="`管理账号：${title}`" @click.stop @keydown.stop><MoreHorizontal :size="15" /></button><template #overlay><a-menu><a-menu-item v-if="canUpdate" key="edit" @click="manageAccount(accountID, 'edit')">编辑账号</a-menu-item><a-menu-item v-if="canUpdate" key="toggle" @click="manageAccount(accountID, 'toggle')">{{ inactive ? '启用账号' : '停用账号' }}</a-menu-item><a-menu-item v-if="canDelete" key="remove" danger @click="manageAccount(accountID, 'remove')">删除账号</a-menu-item></a-menu></template></a-dropdown></span></template>
        </a-tree>
      </a-spin>
      <p v-if="!loading && !catalog.length" class="dns-tree-empty">尚未配置域名厂商</p>
      <p v-else-if="search.trim() && !branches.length" class="dns-tree-empty">没有匹配的厂商或账号</p>
    </div>
  </aside>
</template>

<style scoped>
.dns-provider-tree{position:sticky;top:104px;align-self:start;min-width:0;max-height:calc(100dvh - 132px);padding:16px 12px;border:1px solid var(--edo-border);border-radius:7px;background:var(--edo-surface);color:var(--edo-text);display:flex;flex-direction:column;gap:12px}
.dns-tree-heading{display:flex;align-items:center;justify-content:space-between;gap:10px}.dns-tree-heading h3{margin:0;font-size:13px;font-weight:600;color:var(--edo-muted)}.dns-tree-add{display:inline-flex;align-items:center;gap:3px;flex-shrink:0;padding:2px 0;border:0;background:none;color:var(--edo-primary);font:inherit;font-size:13px;cursor:pointer}.dns-tree-add:disabled{color:var(--edo-muted);cursor:not-allowed}.dns-tree-add:focus-visible{outline:2px solid var(--edo-primary);outline-offset:3px;border-radius:3px}.dns-tree-options{display:flex;flex-direction:column;gap:12px;min-height:0;overflow:auto;scrollbar-width:thin}.dns-tree-options>.ant-input-affix-wrapper{flex-shrink:0}
.dns-provider-tree :deep(.ant-tree){background:transparent;color:var(--edo-text);font-size:13px}.dns-provider-tree :deep(.ant-tree .ant-tree-treenode){width:100%;padding:3px 0}.dns-provider-tree :deep(.ant-tree-node-content-wrapper){min-width:0;flex:1}.dns-provider-tree :deep(.ant-tree-node-selected){background:var(--edo-primary-soft)!important}.dns-provider-tree :deep(.ant-tree-title){display:block}.dns-provider-tree :deep(.ant-tree-indent-unit){width:18px}
.dns-tree-title{display:flex;align-items:center;gap:7px;min-width:0}.dns-tree-title>span:first-child{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;flex:1}.dns-tree-title small,.dns-tree-count{font-size:12px;color:var(--edo-muted);flex-shrink:0}.dns-tree-count{min-width:18px;text-align:center;border-radius:4px;background:var(--edo-surface-soft)}.dns-tree-empty{margin:0;color:var(--edo-muted);font-size:13px}.dns-tree-toggle{display:none}
.dns-tree-manage{display:flex;align-items:center;justify-content:center;flex-shrink:0;width:22px;height:22px;padding:0;border:0;border-radius:4px;background:none;color:var(--edo-muted);cursor:pointer}.dns-tree-manage:hover{color:var(--edo-primary);background:var(--edo-primary-soft)}.dns-tree-manage:focus-visible{outline:2px solid var(--edo-primary);outline-offset:1px}.dns-tree-manage:disabled{cursor:not-allowed;opacity:.5}
@media(max-width:1000px){.dns-provider-tree{position:static;max-height:320px;padding:12px}.dns-tree-heading{flex-wrap:wrap}.dns-tree-toggle{display:flex;align-items:center;justify-content:space-between;flex:1;min-width:160px;gap:8px;padding:0;border:0;background:none;color:var(--edo-text);font:inherit;font-size:13px;text-align:right;cursor:pointer}.dns-tree-toggle>span{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.dns-tree-toggle>svg{flex-shrink:0}.dns-tree-options:not(.is-open){display:none}}
</style>
