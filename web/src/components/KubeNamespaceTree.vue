<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ChevronDown, ChevronRight, RefreshCw, Search } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import type { KubeCluster } from '@/api/kubernetes'

const props = defineProps<{ clusters: KubeCluster[]; cluster: string; namespaces: string[]; selected: string[]; selectedClusters?: string[]; selections?: Record<string,string[]>; namespaceCatalogs?: Record<string,{names:string[];ready?:boolean;error?:string;loading?:boolean}>; namespaced?: boolean; ready?: boolean; loading?: boolean; error?: string; disabled?: boolean }>()
const emit = defineEmits<{ cluster: [id: string, checked: boolean]; selection: [names: string[], clusterID?:string]; expand: [id:string]; retry: [id:string] }>()
const { t } = useI18n()
const search = ref(''), expanded = ref(''),mobileOpen=ref(false)
const namespaceEnabled = computed(() => props.namespaced !== false)
const chosen = computed(() => props.selectedClusters ?? (props.cluster ? [props.cluster] : []))
const currentCluster=computed(()=>props.clusters.find(item=>item.id===props.cluster))
const selectionOf=(id:string)=>chosen.value.includes(id)?props.selections?.[id]??(id===props.cluster?props.selected:[]):[]
const allOf=(id:string)=>selectionOf(id).includes('*')
const partialOf=(id:string)=>!allOf(id)&&selectionOf(id).length>0
const all = computed(() => allOf(props.cluster))
const selectionSummary=computed(()=>chosen.value.length>1?t('k8s.clustersSelected',{count:chosen.value.length}):currentCluster.value?.name||t('k8s.selectCluster'))
let initializedExpanded=false
watch(() => props.cluster, id => { if(!initializedExpanded&&id){expanded.value=id;initializedExpanded=true} }, { immediate: true })
watch(namespaceEnabled, () => { search.value = '' })
const needle = computed(() => search.value.trim().toLowerCase())
const expandedNamespaces = computed(() => expanded.value===props.cluster?props.namespaces:props.namespaceCatalogs?.[expanded.value]?.names??[])
const expandedError = computed(() => expanded.value===props.cluster?props.error:props.namespaceCatalogs?.[expanded.value]?.error)
const expandedReady = computed(() => expanded.value===props.cluster?props.ready:props.namespaceCatalogs?.[expanded.value]?.ready??false)
const visibleNamespaces = computed(() => namespaceEnabled.value ? expandedNamespaces.value.filter(name => !needle.value || name.toLowerCase().includes(needle.value) || props.clusters.find(item => item.id === expanded.value)?.name.toLowerCase().includes(needle.value)) : [])
const visibleClusters = computed(() => props.clusters.filter(item => !needle.value || item.name.toLowerCase().includes(needle.value) || item.id === expanded.value && visibleNamespaces.value.length))
function toggleExpanded(id:string) {
  if(!namespaceEnabled.value||props.disabled||!props.clusters.some(item=>item.id===id&&item.is_active))return
  expanded.value=expanded.value===id?'':id
  emit('expand',expanded.value)
}
function chooseCluster(id: string) {
  if (props.disabled || !props.clusters.some(item=>item.id===id&&item.is_active)) return
  emit('cluster', id, !chosen.value.includes(id))
}
function toggleAll(item: KubeCluster) {
  if (!namespaceEnabled.value || props.disabled || !item.is_active) return
  emit('selection', allOf(item.id) ? [] : ['*'],item.id)
}
function toggleNamespace(name: string, checked: boolean) {
  if (!namespaceEnabled.value || props.disabled) return
  const id=expanded.value||props.cluster
  if(!props.clusters.some(item=>item.id===id&&item.is_active)||!expandedNamespaces.value.includes(name))return
  const selected = new Set(allOf(id) ? expandedNamespaces.value : selectionOf(id))
  if (checked) selected.add(name)
  else selected.delete(name)
  emit('selection', [...selected].sort(),id)
}
function refreshNamespaces(id:string) {
  if (props.disabled || !namespaceEnabled.value || !props.clusters.some(item=>item.id===id&&item.is_active)) return
  emit('retry',id)
}
</script>

<template>
  <aside class="kube-namespace-tree" :class="{'is-disabled':disabled}" :aria-label="t(namespaceEnabled?'k8s.clusterNamespace':'k8s.cluster')">
    <div class="scope-heading"><h3>{{ t(namespaceEnabled?'k8s.clusterNamespace':'k8s.cluster') }}</h3><button type="button" class="scope-toggle" :aria-expanded="mobileOpen" aria-controls="kube-scope-options" :disabled="disabled" @click="mobileOpen=!mobileOpen"><span>{{ selectionSummary }}<small v-if="namespaceEnabled&&chosen.length<=1"> · {{ chosen.length>1?t('k8s.clustersSelected',{count:chosen.length}):t(all?'k8s.allNamespaces':'k8s.namespaceSelected',{count:selectionOf(cluster).length}) }}</small></span><ChevronDown aria-hidden="true" :size="14" :class="{expanded:mobileOpen}" /></button></div><div id="kube-scope-options" class="scope-options" :class="{'is-open':mobileOpen}">
    <a-input v-model:value="search" :disabled="disabled" allow-clear :placeholder="t(namespaceEnabled?'k8s.namespaceSearch':'k8s.clusterSearch')" :aria-label="t(namespaceEnabled?'k8s.namespaceSearch':'k8s.clusterSearch')"><template #prefix><Search aria-hidden="true" :size="14" /></template></a-input>
    <div class="namespace-tree-scroll">
      <section v-for="item in visibleClusters" :key="item.id" class="namespace-tree-branch" :class="{active:chosen.includes(item.id)}">
        <div class="namespace-cluster-row" :class="{'cluster-only':!namespaceEnabled,selected:!namespaceEnabled&&chosen.includes(item.id),'is-disabled':disabled||!item.is_active}">
          <button v-if="namespaceEnabled" type="button" class="namespace-expander" :aria-expanded="expanded===item.id" :aria-label="t(expanded===item.id?'k8s.collapseCluster':'k8s.expandCluster',{name:item.name})" :disabled="disabled||!item.is_active" @click="toggleExpanded(item.id)"><ChevronDown aria-hidden="true" v-if="expanded===item.id" :size="14"/><ChevronRight aria-hidden="true" v-else :size="14"/></button>
          <label class="namespace-cluster-checkbox" :class="{'is-disabled':disabled||!item.is_active}"><input type="checkbox" :checked="namespaceEnabled?allOf(item.id):chosen.includes(item.id)" :indeterminate.prop="namespaceEnabled&&partialOf(item.id)" :aria-checked="namespaceEnabled&&partialOf(item.id)?'mixed':undefined" :disabled="disabled||!item.is_active" :aria-label="namespaceEnabled?t('k8s.selectClusterNamespaces',{name:item.name}):item.name" @change="namespaceEnabled?toggleAll(item):chooseCluster(item.id)" /></label>
          <button v-if="namespaceEnabled" type="button" class="namespace-cluster-name" :class="{inactive:!item.is_active}" :disabled="disabled||!item.is_active" :title="item.name" :aria-expanded="expanded===item.id" @click="toggleExpanded(item.id)">{{ item.name }}</button>
          <span v-else class="namespace-cluster-name" :title="item.name">{{ item.name }}</span>
          <button v-if="namespaceEnabled&&expanded===item.id" type="button" class="namespace-refresh" :aria-label="t((namespaceCatalogs?.[item.id]?.loading||item.id===cluster&&loading)?'k8s.refreshingNamespaces':'k8s.refreshNamespaces',{name:item.name})" :title="t('k8s.refreshNamespaces',{name:item.name})" :aria-busy="Boolean(namespaceCatalogs?.[item.id]?.loading||item.id===cluster&&loading)" :disabled="disabled||!item.is_active||namespaceCatalogs?.[item.id]?.loading||item.id===cluster&&loading" @click="refreshNamespaces(item.id)"><RefreshCw aria-hidden="true" :size="14" :class="{'is-spinning':namespaceCatalogs?.[item.id]?.loading||item.id===cluster&&loading}" /></button>
        </div>
        <div v-if="namespaceEnabled&&expanded===item.id" class="namespace-children">
          <div v-for="name in visibleNamespaces" :key="name" class="namespace-tree-item" :class="{selected:allOf(item.id)||selectionOf(item.id).includes(name),'is-disabled':disabled}">
            <label class="namespace-child-checkbox"><input type="checkbox" :checked="allOf(item.id)||selectionOf(item.id).includes(name)" :disabled="disabled" :aria-label="name" @change="toggleNamespace(name,($event.target as HTMLInputElement).checked)" /></label><span class="namespace-child-name" :title="name">{{ name }}</span>
          </div>
          <div v-if="expandedError" class="namespace-error" role="alert">{{ expandedError }}</div>
          <p v-else-if="!visibleNamespaces.length&&expandedReady!==false" class="namespace-empty">{{ t('k8s.noNamespaceMatches') }}</p>
        </div>
      </section>
      <p v-if="!visibleClusters.length" class="namespace-empty">{{ t(!clusters.length?'k8s.noCluster':namespaceEnabled?'k8s.noNamespaceMatches':'k8s.noClusterMatches') }}</p>
    </div>
    <p v-if="namespaceEnabled" class="namespace-selection-hint">{{ chosen.length>1?t('k8s.clustersSelected',{count:chosen.length}):t(all?'k8s.allNamespaces':'k8s.namespaceSelected',{count:selectionOf(cluster).length}) }}</p>
    </div>
  </aside>
</template>

<style scoped>
.kube-namespace-tree{display:flex;flex-direction:column;gap:10px;min-width:0;align-self:start;padding:12px;border:1px solid var(--kube-border);border-radius:12px;background:var(--kube-surface);color:var(--kube-text);position:sticky;top:104px;max-height:calc(100dvh - 132px)}
h3{margin:0;font-size:13px;font-weight:600;color:var(--kube-text)}
.scope-heading{display:flex;align-items:center;gap:8px;min-height:28px;padding-bottom:8px;border-bottom:1px solid var(--kube-border)}
.scope-options{display:flex;flex-direction:column;gap:10px;min-height:0;flex:1}.scope-toggle{display:none}
.namespace-tree-scroll{overflow:auto;min-height:100px;scrollbar-width:thin;flex:1;scroll-padding:6px}
.namespace-cluster-row{display:flex;align-items:center;gap:6px;min-height:36px;border-radius:4px;padding:2px 3px}
.namespace-cluster-row.cluster-only{padding:4px 7px}
.namespace-tree-branch.active>.namespace-cluster-row:not(.is-disabled){background:var(--kube-selected)}
.namespace-cluster-row.cluster-only.selected:not(.is-disabled){background:var(--kube-selected)}
.namespace-cluster-checkbox{display:grid;place-items:center;width:24px;min-height:28px;flex-shrink:0;border-radius:3px;cursor:pointer}
.namespace-child-checkbox{display:grid;place-items:center;width:24px;min-height:28px;flex-shrink:0;border-radius:3px;cursor:pointer}
.namespace-cluster-checkbox:not(.is-disabled):hover{background:var(--kube-hover)}
.namespace-cluster-checkbox.is-disabled{cursor:not-allowed}
.namespace-cluster-checkbox input{width:16px;height:16px;margin:0;accent-color:var(--kube-link);cursor:inherit}
.namespace-child-checkbox input{width:16px;height:16px;margin:0;accent-color:var(--kube-link);cursor:inherit}
.namespace-child-checkbox input:focus-visible{outline:2px solid var(--kube-focus);outline-offset:3px}
.namespace-tree-item.is-disabled .namespace-child-checkbox{cursor:not-allowed}
.namespace-cluster-checkbox input:focus-visible{outline:2px solid var(--kube-focus);outline-offset:3px}
.namespace-expander,.namespace-cluster-name,.namespace-refresh{border:0;background:none;color:var(--kube-text);cursor:pointer;font:inherit;text-align:left;border-radius:3px}
.namespace-refresh{display:grid;place-items:center;width:24px;min-height:28px;padding:2px;flex-shrink:0;color:var(--kube-muted)}
.namespace-refresh .is-spinning{animation:namespace-refresh-spin 1s linear infinite}
@keyframes namespace-refresh-spin{to{transform:rotate(360deg)}}
.namespace-expander{display:grid;place-items:center;width:24px;min-height:28px;padding:2px;color:var(--kube-muted);flex-shrink:0}
.namespace-cluster-name{padding:4px 2px;min-height:28px;font-weight:600;font-size:13px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;flex:1}
.namespace-tree-branch.active>.namespace-cluster-row:not(.is-disabled) .namespace-cluster-name{color:var(--kube-link)}
.namespace-cluster-name:not(:disabled):hover,.namespace-expander:not(:disabled):hover,.namespace-refresh:not(:disabled):hover{background:var(--kube-hover);color:var(--kube-link)}
.namespace-cluster-name:disabled,.namespace-expander:disabled,.scope-toggle:disabled,.namespace-refresh:disabled{cursor:not-allowed;color:var(--kube-muted);opacity:.55}
.namespace-cluster-name:focus-visible,.namespace-expander:focus-visible,.scope-toggle:focus-visible,.namespace-refresh:focus-visible{outline:2px solid var(--kube-focus);outline-offset:1px}
.namespace-children{padding-left:30px;padding-top:4px}
.namespace-tree-item{display:flex;align-items:center;gap:6px;min-height:34px;padding:4px 3px;border-radius:8px;color:var(--kube-muted);font-size:13px}
.namespace-child-name{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;min-width:0}
.namespace-tree-item:not(.is-disabled):hover{background:var(--kube-hover)}
.namespace-tree-item:not(.is-disabled):focus-within{outline:2px solid var(--kube-focus);outline-offset:-2px}
.namespace-tree-item.is-disabled{color:var(--kube-muted);background:var(--kube-surface-soft);cursor:not-allowed}
.namespace-selection-hint{margin:0;border-top:1px solid var(--kube-border);padding-top:8px;font-size:12px;color:var(--kube-muted)}
.namespace-empty,.namespace-error{font-size:12px;color:var(--kube-muted);overflow-wrap:anywhere;padding:8px 0;margin:0;line-height:1.6}
.namespace-error{color:var(--kube-danger)}
@media(max-width:1000px){.kube-namespace-tree{position:static;max-height:280px}.namespace-tree-scroll{max-height:180px;min-height:0}.scope-heading{flex-wrap:wrap}.scope-toggle{display:flex;align-items:center;justify-content:space-between;flex:1;gap:8px;min-height:28px;min-width:160px;padding:3px 5px;border:1px solid var(--kube-border);border-radius:4px;background:var(--kube-surface-soft);color:var(--kube-text);font:inherit;font-size:12px;text-align:right;cursor:pointer}.scope-toggle>span{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.scope-toggle small{font:inherit;color:var(--kube-muted)}.scope-toggle svg{flex-shrink:0;transition:transform .12s}.scope-toggle svg.expanded{transform:rotate(180deg)}.scope-options:not(.is-open){display:none}}
@media(prefers-reduced-motion:reduce){.scope-toggle svg{transition:none}.namespace-refresh .is-spinning{animation:none}}
</style>
