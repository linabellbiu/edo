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
  <aside class="kube-namespace-tree" :class="{'is-disabled':disabled}" :aria-label="t(namespaceEnabled?'k8s.clusterNamespace':'k8s.cluster')">
    <div class="scope-heading"><h3>{{ t(namespaceEnabled?'k8s.clusterNamespace':'k8s.cluster') }}</h3><button type="button" class="scope-toggle" :aria-expanded="mobileOpen" aria-controls="kube-scope-options" :disabled="disabled" @click="mobileOpen=!mobileOpen"><span>{{ currentCluster?.name||t('k8s.selectCluster') }}<small v-if="namespaceEnabled"> · {{ t(all?'k8s.allNamespaces':'k8s.namespaceSelected',{count:selected.length}) }}</small></span><ChevronDown aria-hidden="true" :size="14" :class="{expanded:mobileOpen}" /></button></div><div id="kube-scope-options" class="scope-options" :class="{'is-open':mobileOpen}">
    <a-input v-model:value="search" :disabled="disabled" allow-clear :placeholder="t(namespaceEnabled?'k8s.namespaceSearch':'k8s.clusterSearch')" :aria-label="t(namespaceEnabled?'k8s.namespaceSearch':'k8s.clusterSearch')"><template #prefix><Search aria-hidden="true" :size="14" /></template></a-input>
    <div class="namespace-tree-scroll">
      <section v-for="item in visibleClusters" :key="item.id" class="namespace-tree-branch" :class="{active:item.id===cluster}">
        <div class="namespace-cluster-row" :class="{'cluster-only':!namespaceEnabled,selected:!namespaceEnabled&&item.id===cluster,'is-disabled':disabled||!item.is_active}">
          <button v-if="namespaceEnabled" type="button" class="namespace-expander" :aria-expanded="expanded===item.id" :aria-label="t(expanded===item.id?'k8s.collapseCluster':'k8s.expandCluster',{name:item.name})" :disabled="disabled||!item.is_active" @click="expanded===item.id?expanded='':chooseCluster(item.id)"><ChevronDown aria-hidden="true" v-if="expanded===item.id" :size="14"/><ChevronRight aria-hidden="true" v-else :size="14"/></button>
          <label v-if="namespaceEnabled" class="namespace-cluster-checkbox" :class="{'is-disabled':disabled||!item.is_active}"><input type="checkbox" :checked="item.id===cluster&&all" :indeterminate.prop="item.id===cluster&&!all&&selected.length>0" :aria-checked="item.id===cluster&&!all&&selected.length>0?'mixed':undefined" :disabled="disabled||!item.is_active" :aria-label="t('k8s.selectClusterNamespaces',{name:item.name})" @change="toggleAll(item)" /></label>
          <a-radio v-else name="kube-workspace-cluster" :checked="item.id===cluster" :disabled="disabled||!item.is_active" :aria-label="item.name" @change="chooseCluster(item.id)" />
          <button type="button" class="namespace-cluster-name" :class="{inactive:!item.is_active}" :disabled="disabled||!item.is_active" :title="item.name" @click="chooseCluster(item.id)">{{ item.name }}</button>
        </div>
        <div v-if="namespaceEnabled&&expanded===item.id&&item.id===cluster" class="namespace-children">
          <a-spin v-if="loading" size="small" class="namespace-loading"/>
          <div v-if="error" class="namespace-error" role="alert"><span>{{ error }}</span><a-button type="link" size="small" :disabled="disabled" @click="emit('retry')">{{ t('k8s.refresh') }}</a-button></div>
          <div v-for="name in visibleNamespaces" :key="name" class="namespace-tree-item" :class="{selected:all||selected.includes(name),'is-disabled':disabled}">
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
.kube-namespace-tree{display:flex;flex-direction:column;gap:10px;min-width:0;align-self:start;padding:12px;border:1px solid var(--kube-border);border-radius:6px;background:var(--kube-surface);color:var(--kube-text);position:sticky;top:104px;max-height:calc(100dvh - 132px)}
h3{margin:0;font-size:13px;font-weight:600;color:var(--kube-text)}
.scope-heading{display:flex;align-items:center;gap:8px;min-height:28px;padding-bottom:8px;border-bottom:1px solid var(--kube-border)}
.scope-options{display:flex;flex-direction:column;gap:10px;min-height:0;flex:1}.scope-toggle{display:none}
.namespace-tree-scroll{overflow:auto;min-height:100px;scrollbar-width:thin;flex:1;scroll-padding:6px}
.namespace-cluster-row{display:flex;align-items:center;gap:6px;min-height:36px;border-radius:4px;padding:2px 3px}
.namespace-cluster-row.cluster-only{padding:4px 7px}
.namespace-tree-branch.active>.namespace-cluster-row:not(.is-disabled){background:var(--kube-selected)}
.namespace-cluster-row.cluster-only.selected:not(.is-disabled){background:var(--kube-selected)}
.namespace-cluster-checkbox{display:grid;place-items:center;width:24px;min-height:28px;flex-shrink:0;border-radius:3px;cursor:pointer}
.namespace-cluster-checkbox:not(.is-disabled):hover{background:var(--kube-hover)}
.namespace-cluster-checkbox.is-disabled{cursor:not-allowed}
.namespace-cluster-checkbox input{width:16px;height:16px;margin:0;accent-color:var(--kube-link);cursor:inherit}
.namespace-cluster-checkbox input:focus-visible{outline:2px solid var(--kube-focus);outline-offset:3px}
.namespace-expander,.namespace-cluster-name{border:0;background:none;color:var(--kube-text);cursor:pointer;font:inherit;text-align:left;border-radius:3px}
.namespace-expander{display:grid;place-items:center;width:24px;min-height:28px;padding:2px;color:var(--kube-muted);flex-shrink:0}
.namespace-cluster-name{padding:4px 2px;min-height:28px;font-weight:600;font-size:13px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;flex:1}
.namespace-tree-branch.active>.namespace-cluster-row:not(.is-disabled) .namespace-cluster-name{color:var(--kube-link)}
.namespace-cluster-name:not(:disabled):hover,.namespace-expander:not(:disabled):hover{background:var(--kube-hover);color:var(--kube-link)}
.namespace-cluster-name:disabled,.namespace-expander:disabled,.scope-toggle:disabled{cursor:not-allowed;color:var(--kube-muted);opacity:.55}
.namespace-cluster-name:focus-visible,.namespace-expander:focus-visible,.scope-toggle:focus-visible{outline:2px solid var(--kube-focus);outline-offset:1px}
.namespace-children{padding-left:24px;padding-top:4px}
.namespace-tree-item{display:flex;align-items:center;min-height:34px;padding:4px 6px;border-radius:4px;color:var(--kube-muted);font-size:13px}
.namespace-tree-item :deep(.ant-checkbox-wrapper)>span:last-child{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.namespace-tree-item:not(.selected):not(.is-disabled):hover{background:var(--kube-hover)}
.namespace-tree-item.selected{color:var(--kube-link);background:var(--kube-selected)}
.namespace-tree-item:not(.is-disabled):focus-within{outline:2px solid var(--kube-focus);outline-offset:-2px}
.namespace-tree-item.is-disabled{color:var(--kube-muted);background:var(--kube-surface-soft);cursor:not-allowed}
.namespace-tree-item :deep(.ant-checkbox-wrapper){display:flex;align-items:center;line-height:1.6;width:100%;min-width:0;color:inherit;font-size:13px}
.namespace-selection-hint{margin:0;border-top:1px solid var(--kube-border);padding-top:8px;font-size:12px;color:var(--kube-muted)}
.namespace-empty,.namespace-error{font-size:12px;color:var(--kube-muted);overflow-wrap:anywhere;padding:8px 0;margin:0;line-height:1.6}
.namespace-loading{padding:12px}.namespace-error{color:var(--kube-danger)}
@media(max-width:1000px){.kube-namespace-tree{position:static;max-height:280px}.namespace-tree-scroll{max-height:180px;min-height:0}.scope-heading{flex-wrap:wrap}.scope-toggle{display:flex;align-items:center;justify-content:space-between;flex:1;gap:8px;min-height:28px;min-width:160px;padding:3px 5px;border:1px solid var(--kube-border);border-radius:4px;background:var(--kube-surface-soft);color:var(--kube-text);font:inherit;font-size:12px;text-align:right;cursor:pointer}.scope-toggle>span{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.scope-toggle small{font:inherit;color:var(--kube-muted)}.scope-toggle svg{flex-shrink:0;transition:transform .12s}.scope-toggle svg.expanded{transform:rotate(180deg)}.scope-options:not(.is-open){display:none}}
@media(prefers-reduced-motion:reduce){.scope-toggle svg{transition:none}}
</style>
