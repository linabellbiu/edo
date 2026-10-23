<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ChevronDown, ChevronRight, Search } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import type { KubeCluster } from '@/api/kubernetes'

const props = defineProps<{ clusters: KubeCluster[]; cluster: string; namespaces: string[]; selected: string[]; namespaced?: boolean; loading?: boolean; error?: string; disabled?: boolean }>()
const emit = defineEmits<{ cluster: [id: string, all?: boolean]; selection: [names: string[]]; retry: [] }>()
const { t } = useI18n()
const search = ref(''), expanded = ref(''),mobileOpen=ref(false)
const currentCluster=computed(()=>props.clusters.find(item=>item.id===props.cluster))
const namespaceEnabled = computed(() => props.namespaced !== false)
watch(() => props.cluster, id => { expanded.value = id }, { immediate: true })
watch(namespaceEnabled, () => { search.value = '' })
const all = computed(() => props.selected.includes('*'))
const needle = computed(() => search.value.trim().toLowerCase())
const visibleNamespaces = computed(() => namespaceEnabled.value ? props.namespaces.filter(name => !needle.value || name.toLowerCase().includes(needle.value) || props.clusters.find(item => item.id === props.cluster)?.name.toLowerCase().includes(needle.value)) : [])
const visibleClusters = computed(() => props.clusters.filter(item => !needle.value || item.name.toLowerCase().includes(needle.value) || item.id === props.cluster && visibleNamespaces.value.length))
function chooseCluster(id: string) {
  if (props.disabled) return
  expanded.value = id
  if (props.cluster !== id) emit('cluster', id)
}
function toggleAll(item: KubeCluster) {
  if (!namespaceEnabled.value || props.disabled || !item.is_active) return
  expanded.value=item.id
  if(item.id!==props.cluster) { emit('cluster',item.id,true);return }
  emit('selection', all.value ? [] : ['*'])
}
function toggleNamespace(name: string, checked: boolean) {
  if (!namespaceEnabled.value || props.disabled) return
  const selected = new Set(all.value ? props.namespaces : props.selected)
  if (checked) selected.add(name)
  else selected.delete(name)
  emit('selection', [...selected].sort())
}
</script>

<template>
  <aside class="kube-namespace-tree" :aria-label="t(namespaceEnabled?'k8s.clusterNamespace':'k8s.cluster')">
    <div class="scope-heading"><h3>{{ t(namespaceEnabled?'k8s.clusterNamespace':'k8s.cluster') }}</h3><button class="scope-toggle" :aria-expanded="mobileOpen" aria-controls="kube-scope-options" :disabled="disabled" @click="mobileOpen=!mobileOpen"><span>{{ currentCluster?.name||t('k8s.selectCluster') }}<small v-if="namespaceEnabled"> · {{ t(all?'k8s.allNamespaces':'k8s.namespaceSelected',{count:selected.length}) }}</small></span><ChevronDown :size="14" :class="{expanded:mobileOpen}" /></button></div><div id="kube-scope-options" class="scope-options" :class="{'is-open':mobileOpen}">
    <a-input v-model:value="search" allow-clear :placeholder="t(namespaceEnabled?'k8s.namespaceSearch':'k8s.clusterSearch')" :aria-label="t(namespaceEnabled?'k8s.namespaceSearch':'k8s.clusterSearch')"><template #prefix><Search :size="14" /></template></a-input>
    <div class="namespace-tree-scroll">
      <section v-for="item in visibleClusters" :key="item.id" class="namespace-tree-branch" :class="{active:item.id===cluster}">
        <div class="namespace-cluster-row" :class="{'cluster-only':!namespaceEnabled,selected:!namespaceEnabled&&item.id===cluster}">
          <button v-if="namespaceEnabled" class="namespace-expander" :aria-expanded="expanded===item.id" :aria-label="t(expanded===item.id?'k8s.collapseCluster':'k8s.expandCluster',{name:item.name})" :disabled="disabled||!item.is_active" @click="expanded===item.id?expanded='':chooseCluster(item.id)"><ChevronDown v-if="expanded===item.id" :size="14"/><ChevronRight v-else :size="14"/></button>
          <a-checkbox v-if="namespaceEnabled" :checked="item.id===cluster&&all" :indeterminate="item.id===cluster&&!all&&selected.length>0" :disabled="disabled||!item.is_active" :aria-label="t('k8s.selectClusterNamespaces',{name:item.name})" @change="toggleAll(item)"/>
          <a-radio v-else name="kube-workspace-cluster" :checked="item.id===cluster" :disabled="disabled||!item.is_active" :aria-label="item.name" @change="chooseCluster(item.id)" />
          <button class="namespace-cluster-name" :class="{inactive:!item.is_active}" :disabled="disabled||!item.is_active" :title="item.name" @click="chooseCluster(item.id)">{{ item.name }}</button>
        </div>
        <div v-if="namespaceEnabled&&expanded===item.id&&item.id===cluster" class="namespace-children">
          <a-spin v-if="loading" size="small" class="namespace-loading"/>
          <div v-if="error" class="namespace-error"><span>{{ error }}</span><a-button type="link" size="small" :disabled="disabled" @click="emit('retry')">{{ t('k8s.refresh') }}</a-button></div>
          <div v-for="name in visibleNamespaces" :key="name" class="namespace-tree-item" :class="{selected:all||selected.includes(name)}">
            <a-checkbox :checked="all||selected.includes(name)" :disabled="disabled" :aria-label="name" @change="toggleNamespace(name,$event.target.checked)"><span :title="name">{{ name }}</span></a-checkbox>
          </div>
          <p v-if="!loading&&!error&&!visibleNamespaces.length" class="namespace-empty">{{ t('k8s.noNamespaceMatches') }}</p>
        </div>
      </section>
      <p v-if="!visibleClusters.length" class="namespace-empty">{{ t(!clusters.length?'k8s.noCluster':namespaceEnabled?'k8s.noNamespaceMatches':'k8s.noClusterMatches') }}</p>
    </div>
    <p v-if="namespaceEnabled" class="namespace-selection-hint">{{ t(all?'k8s.allNamespaces':'k8s.namespaceSelected',{count:selected.length}) }}</p>
    </div>
  </aside>
</template>

<style scoped>
.kube-namespace-tree{display:flex;flex-direction:column;gap:12px;min-width:0;align-self:start;padding:16px 12px;border:1px solid var(--edo-border);border-radius:7px;background:var(--edo-surface);position:sticky;top:104px;max-height:calc(100vh - 132px)}
h3{margin:0 2px;font-size:13px;font-weight:600;color:var(--edo-muted)}
.namespace-tree-scroll{overflow:auto;min-height:100px;scrollbar-width:thin;flex:1}
.namespace-cluster-row{display:flex;align-items:center;gap:7px;min-height:38px}
.namespace-cluster-row.cluster-only{padding:4px 7px;border-radius:4px}.namespace-cluster-row.cluster-only.selected{background:var(--edo-primary-soft)}
.namespace-expander,.namespace-cluster-name{border:0;background:none;color:var(--edo-text);cursor:pointer;padding:4px 0;font:inherit;text-align:left}
.namespace-expander{display:grid;place-items:center;width:16px;color:var(--edo-muted);flex-shrink:0}
.namespace-cluster-name{font-weight:600;font-size:13px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;flex:1}
.namespace-cluster-name:disabled,.namespace-expander:disabled{cursor:not-allowed;color:var(--edo-muted)}
.namespace-children{padding-left:29px}
.namespace-tree-item{display:flex;align-items:center;gap:9px;min-height:34px;padding:4px 5px;border-radius:4px;cursor:pointer;color:var(--edo-muted);font-size:13px}
.namespace-tree-item :deep(.ant-checkbox-wrapper)>span:last-child{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.namespace-tree-item:hover,.namespace-tree-item:focus-within{background:var(--edo-surface-soft)}
.namespace-tree-item.selected{color:var(--edo-text);background:color-mix(in srgb,var(--edo-primary) 6%,var(--edo-surface))}
.namespace-tree-item :deep(.ant-checkbox-wrapper){display:flex;align-items:center;line-height:1.6;width:100%;min-width:0;color:inherit;font-size:13px}
.namespace-selection-hint{margin:0;border-top:1px solid var(--edo-border);padding-top:10px;font-size:12px;color:var(--edo-muted)}
.namespace-empty,.namespace-error{font-size:12px;color:var(--edo-muted);overflow-wrap:anywhere;padding:8px 0;margin:0}
.namespace-loading{padding:12px}.namespace-error{color:var(--kube-danger)}
@media(max-width:1000px){.kube-namespace-tree{position:static;max-height:260px}.namespace-tree-scroll{max-height:170px;min-height:0}}
</style>

<style scoped>
.scope-heading{display:flex;align-items:center;gap:10px}.scope-options{display:flex;flex-direction:column;gap:12px;min-height:0;flex:1}.scope-toggle{display:none}.scope-toggle:focus-visible{outline:2px solid var(--edo-primary);outline-offset:3px}@media(max-width:1000px){.kube-namespace-tree{padding:12px}.scope-heading{flex-wrap:wrap}.scope-toggle{display:flex;align-items:center;justify-content:space-between;flex:1;gap:8px;min-width:180px;border:0;background:none;color:var(--edo-text);font:inherit;font-size:12px;text-align:right;cursor:pointer}.scope-toggle>span{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.scope-toggle small{font:inherit;color:var(--edo-muted)}.scope-toggle svg{flex-shrink:0;transition:transform .12s}.scope-toggle svg.expanded{transform:rotate(180deg)}.scope-options:not(.is-open){display:none}}
</style>
