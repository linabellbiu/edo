<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ArrowUpRight, Boxes, CircleAlert, Clock3, Cpu, Layers3, MemoryStick, Network, RefreshCw, Search, Server, ShieldCheck } from 'lucide-vue-next'
import type { KubeObject } from '@/api/kubernetes'
import {
  topologyAddresses, topologyAmount, topologyFresh, topologyMeters, topologyNodeKey, topologyPercent,
  topologyReady, topologyRoles, topologySearchText, topologyTone,
  type NodeTopologyData, type NodeTopologyEntry, type TopologyMeter,
} from '@/utils/kubernetesTopology'
import type { NodeRole } from '@/utils/kubernetesRoles'
import { formatDateTime } from '@/utils/time'

const props = withDefaults(defineProps<{
  data: NodeTopologyData | null
  loading: boolean
  error: string
  clusterName?: string
  endpoint?: string
  disabled?: boolean
  selected?: string
}>(), { clusterName: '', endpoint: '', disabled: false, selected: '' })
const emit = defineEmits<{ select: [node: KubeObject]; refresh: [] }>()
const { t } = useI18n()
const query = ref('')
const role = ref<'all' | NodeRole>('all')
const now = ref(Date.now())
let clock: ReturnType<typeof setInterval> | undefined

watch(() => props.data?.cluster_id, () => { query.value = ''; role.value = 'all' })
onMounted(() => { clock = setInterval(() => { now.value = Date.now() }, 15_000) })
onBeforeUnmount(() => { if (clock) clearInterval(clock) })

const nodes = computed(() => {
  const seen = new Set<string>()
  return (props.data?.nodes ?? []).filter(entry => {
    const key = topologyNodeKey(entry)
    if (!key || seen.has(key)) return false
    seen.add(key)
    return true
  })
})
const filtersActive = computed(() => query.value.trim() !== '' || role.value !== 'all')
const visibleNodes = computed(() => {
  const needle = query.value.trim().toLocaleLowerCase()
  return nodes.value.filter(entry => (role.value === 'all' || topologyRoles(entry).includes(role.value)) && (!needle || topologySearchText(entry).includes(needle)))
    .slice().sort((left, right) => String(left.node.metadata?.name).localeCompare(String(right.node.metadata?.name)))
})
const groups = computed(() => (['control-plane', 'worker'] as const).map(key => ({
  key,
  title: t(key === 'control-plane' ? 'nodeRoles.controlPlane' : 'nodeRoles.worker'),
  entries: visibleNodes.value.filter(entry => topologyRoles(entry).includes('control-plane') ? key === 'control-plane' : key === 'worker'),
})).filter(group => group.entries.length))
const ready = computed(() => nodes.value.filter(entry => topologyReady(entry) === 'ready').length)
const metricsStale = computed(() => Boolean(props.data && !topologyFresh(props.data.collected_at, now.value)))
const hasMissingMetrics = computed(() => nodes.value.some(entry => topologyMeters(entry, props.data?.collected_at ?? '', Boolean(props.error), now.value).slice(0, 2).some(meter => meter.used === undefined)))
const unknownHosts = computed(() => props.data?.hosts?.filter(host => host.state !== 'matched').length ?? 0)
const roleOptions = computed(() => [
  { value: 'all', label: t('k8sTopology.allRoles') },
  { value: 'control-plane', label: t('k8sTopology.controlPlaneShort') },
  { value: 'worker', label: t('k8sTopology.workerShort') },
])

function clearFilters() { query.value = ''; role.value = 'all' }
function select(entry: NodeTopologyEntry) { if (!props.disabled) emit('select', entry.node) }
function refresh() { if (!props.loading && !props.disabled) emit('refresh') }
function meters(entry: NodeTopologyEntry) { return topologyMeters(entry, props.data?.collected_at ?? '', Boolean(props.error), now.value) }
function metricName(key: string) { return key === 'cpu' ? 'CPU' : key === 'memory' ? t('k8sTopology.memory') : 'Pod' }
function metricHint(meter: TopologyMeter) {
  if (meter.state === 'no_permission') return t('k8sTopology.podNoPermission')
  if (meter.state === 'missing_capacity') return t('k8sTopology.capacityMissing')
  if (meter.state !== 'ok') return t(meter.key === 'pods' ? 'k8sTopology.podUnavailable' : 'k8sTopology.unavailable')
  return `${t('k8sTopology.capacity')}: ${topologyAmount(meter.capacity, meter.key)}`
}
function roleName(value: NodeRole) { return t(value === 'control-plane' ? 'nodeRoles.controlPlane' : 'nodeRoles.worker') }
function version(entry: NodeTopologyEntry) { return String(entry.node.status?.nodeInfo?.kubeletVersion || '—') }
function nodeDescription(entry: NodeTopologyEntry) {
  return [t('k8sTopology.openNode', { name: entry.node.metadata?.name }), ...topologyRoles(entry).map(roleName), topologyReady(entry), ...topologyAddresses(entry), ...meters(entry).map(meter => `${metricName(meter.key)} ${topologyAmount(meter.used, meter.key)} / ${topologyAmount(meter.total, meter.key)} (${topologyPercent(meter)})`)].join(' · ')
}
</script>

<template>
  <section class="node-topology" :aria-busy="loading" :aria-label="t('k8sTopology.title')">
    <header class="topology-toolbar">
      <div class="topology-title"><Network :size="17" /><strong>{{ t('k8sTopology.title') }}</strong><span>{{ t('k8sTopology.nodeCount', { count: nodes.length }) }}</span></div>
      <div class="topology-filters"><a-input v-model:value="query" allow-clear :placeholder="t('k8sTopology.search')" :aria-label="t('k8sTopology.search')"><template #prefix><Search :size="14" /></template></a-input><a-select v-model:value="role" :options="roleOptions" :aria-label="t('k8sTopology.roles')" /></div>
      <a-button :loading="loading" :disabled="disabled" @click="refresh"><RefreshCw :size="14" />{{ t('k8sTopology.refresh') }}</a-button>
    </header>
    <a-alert v-if="error" class="topology-alert" type="error" show-icon :message="t('k8sTopology.loadFailed')" :description="error"><template #action><a-button size="small" :disabled="disabled" @click="refresh">{{ t('k8sTopology.refresh') }}</a-button></template></a-alert>
    <div class="topology-meta"><span>{{ t('k8sTopology.visibleCount', { visible: visibleNodes.length, total: nodes.length }) }}</span><span v-if="data"><Clock3 :size="12" />{{ t('k8sTopology.updatedAt', { time: formatDateTime(data.collected_at) }) }}</span></div>
    <div v-if="!data && loading" class="topology-loading" role="status"><a-spin /><span>{{ t('k8sTopology.loading') }}</span></div>
    <a-empty v-else-if="!data" class="topology-empty" :description="t(error ? 'k8sTopology.loadFailed' : 'k8sTopology.noCluster')" />
    <template v-else>
      <div v-if="hasMissingMetrics || unknownHosts" class="topology-notices"><span v-if="hasMissingMetrics"><CircleAlert :size="13" />{{ t(metricsStale ? 'k8sTopology.staleMetrics' : 'k8sTopology.missingMetrics') }}</span><span v-if="unknownHosts"><Server :size="13" />{{ t('k8sTopology.hostsUnverified', { count: unknownHosts }) }}</span></div>
      <div v-if="visibleNodes.length" class="topology-canvas" tabindex="0" :aria-label="t('k8sTopology.membershipLegend')">
        <div class="topology-root"><span class="cluster-symbol"><Boxes :size="23" /></span><div><small>{{ t('k8sTopology.cluster') }}</small><strong>{{ clusterName || data.cluster_id }}</strong><code v-if="endpoint">{{ endpoint }}</code></div><span class="cluster-ready" :class="{ degraded: ready !== nodes.length }"><i />{{ t('k8sTopology.readyCount', { ready, total: nodes.length }) }}</span></div>
        <div class="topology-lanes" :class="{ 'single-lane': groups.length === 1 }">
          <section v-for="group in groups" :key="group.key" class="topology-lane" :class="group.key">
            <header class="lane-heading"><span class="lane-icon"><ShieldCheck v-if="group.key === 'control-plane'" :size="16" /><Layers3 v-else :size="16" /></span><strong>{{ group.title }}</strong><span>{{ group.entries.length }}</span></header>
            <ol class="topology-nodes">
              <li v-for="entry in group.entries" :key="topologyNodeKey(entry)" class="topology-node-branch">
                <button type="button" class="topology-node-card" :class="{ selected: selected === topologyNodeKey(entry) }" :disabled="disabled" :aria-label="nodeDescription(entry)" @click="select(entry)">
                  <span class="node-card-heading"><span class="node-symbol"><Server :size="18" /></span><span class="node-identity"><strong :title="entry.node.metadata?.name">{{ entry.node.metadata?.name }}</strong><span class="node-roles"><span v-for="nodeRole in topologyRoles(entry)" :key="nodeRole" :class="nodeRole">{{ roleName(nodeRole) }}</span></span></span><ArrowUpRight :size="15" class="node-open-icon" /></span>
                  <span class="node-status-row"><span class="node-ready" :class="topologyReady(entry)"><i />{{ t(topologyReady(entry) === 'ready' ? 'k8sTopology.ready' : topologyReady(entry) === 'not_ready' ? 'k8sTopology.notReady' : 'k8sTopology.unknown') }}</span><span v-if="entry.node.spec?.unschedulable" class="node-cordoned">{{ t('k8sTopology.schedulingPaused') }}</span><code :title="topologyAddresses(entry).join(', ')">{{ topologyAddresses(entry).join(', ') || '—' }}</code></span>
                  <span class="node-resource-meters">
                    <span v-for="meter in meters(entry)" :key="meter.key" class="node-resource" :class="`tone-${topologyTone(meter)}`" :title="metricHint(meter)">
                      <span class="node-resource-line"><span class="resource-label"><Cpu v-if="meter.key === 'cpu'" :size="12" /><MemoryStick v-else-if="meter.key === 'memory'" :size="12" /><Boxes v-else :size="12" />{{ metricName(meter.key) }}</span><span class="resource-values">{{ topologyAmount(meter.used, meter.key) }}<span> / {{ topologyAmount(meter.total, meter.key) }}</span></span><strong>{{ topologyPercent(meter) }}</strong></span>
                      <span class="resource-track" :class="{ unknown: meter.percent === undefined }" :role="meter.percent === undefined ? undefined : 'meter'" :aria-label="`${entry.node.metadata?.name} ${metricName(meter.key)}`" :aria-valuemin="meter.percent === undefined ? undefined : 0" :aria-valuemax="meter.percent === undefined ? undefined : Math.max(100, meter.percent)" :aria-valuenow="meter.percent" :aria-valuetext="`${topologyAmount(meter.used, meter.key)} / ${topologyAmount(meter.total, meter.key)} (${topologyPercent(meter)})`"><span v-if="meter.percent !== undefined" :style="{ width: `${meter.width}%` }" /></span>
                    </span>
                  </span>
                  <span class="node-card-footer"><span v-if="entry.hosts?.length" class="node-hosts"><Server :size="11" /><span :title="entry.hosts.map(host => host.name).join(', ')">{{ entry.hosts.map(host => host.name).join(', ') }}</span></span><span v-else class="node-version-spacer" /><code>{{ version(entry) }}</code></span>
                </button>
              </li>
            </ol>
          </section>
        </div>
      </div>
      <a-empty v-else class="topology-empty" :description="t(filtersActive ? 'k8sTopology.noMatches' : 'k8sTopology.noNodes')"><a-button v-if="filtersActive" @click="clearFilters">{{ t('k8sTopology.clearFilters') }}</a-button></a-empty>
      <footer class="topology-legend"><span><i class="membership-line" />{{ t('k8sTopology.membershipLegend') }}</span><span>{{ t('k8sTopology.resourceLegend') }}</span></footer>
    </template>
  </section>
</template>

<style scoped>
.node-topology{min-width:0;background:var(--edo-surface);border:1px solid var(--edo-border);border-radius:7px;overflow:hidden}.topology-toolbar{display:flex;align-items:center;gap:16px;padding:14px 16px;border-bottom:1px solid var(--edo-border)}.topology-title{display:flex;align-items:center;gap:8px;white-space:nowrap}.topology-title>svg{color:var(--edo-primary)}.topology-title strong{font-size:14px;font-weight:600}.topology-title>span{padding-left:3px;color:var(--edo-muted);font-size:12px}.topology-filters{display:flex;align-items:center;gap:8px;margin-left:auto;min-width:0}.topology-filters>.ant-input-affix-wrapper{width:260px}.topology-filters>.ant-select{width:132px}.topology-toolbar>.ant-btn{display:inline-flex;align-items:center;gap:6px}.topology-alert{margin:14px 16px 0}.topology-meta{display:flex;align-items:center;justify-content:space-between;gap:12px;padding:10px 16px;color:var(--edo-muted);font-size:11px}.topology-meta>span{display:inline-flex;align-items:center;gap:5px}.topology-loading{display:flex;min-height:360px;align-items:center;justify-content:center;gap:10px;color:var(--edo-muted);font-size:13px}.topology-empty{padding:50px 16px}.topology-notices{display:flex;flex-wrap:wrap;gap:10px 20px;padding:0 16px 10px;font-size:12px;color:var(--edo-muted)}.topology-notices>span{display:inline-flex;align-items:center;gap:6px}.topology-notices svg{color:#aa741b}.topology-canvas{--topology-line:color-mix(in srgb,var(--edo-primary) 26%,var(--edo-border));overflow:auto;max-height:850px;min-height:380px;padding:20px 24px 28px;background-color:var(--edo-surface-soft);background-image:radial-gradient(var(--edo-border) .65px,transparent .65px);background-size:14px 14px;outline-offset:-3px}.topology-canvas:focus-visible{outline:2px solid var(--edo-primary)}.topology-root{position:relative;display:flex;align-items:center;gap:12px;width:max-content;max-width:min(100%,580px);min-width:290px;margin:0 auto 34px;padding:13px 16px;border:1px solid color-mix(in srgb,var(--edo-primary) 35%,var(--edo-border));border-radius:7px;background:var(--edo-surface);box-shadow:0 2px 7px rgb(16 24 40 / 3%)}.topology-root::after{position:absolute;top:100%;left:50%;height:34px;border-left:1px solid var(--topology-line);content:''}.cluster-symbol{display:grid;width:42px;height:42px;flex:0 0 42px;place-items:center;border-radius:6px;color:var(--edo-primary);background:var(--edo-primary-soft)}.topology-root>div{min-width:0}.topology-root small,.topology-root strong,.topology-root code{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.topology-root small{color:var(--edo-muted);font-size:10px}.topology-root strong{max-width:320px;margin-top:1px;font-size:14px;font-weight:600}.topology-root code{max-width:320px;margin-top:3px;color:var(--edo-muted);font-size:10px}.cluster-ready{display:inline-flex;align-items:center;gap:5px;margin-left:12px;white-space:nowrap;color:var(--edo-muted);font-size:11px}.cluster-ready>i{width:6px;height:6px;border-radius:50%;background:#1e9971}.topology-lanes{position:relative;display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:28px;max-width:1040px;margin:0 auto}.topology-lanes::before{position:absolute;top:0;left:25%;right:25%;border-top:1px solid var(--topology-line);content:''}.topology-lanes.single-lane{grid-template-columns:minmax(0,520px);justify-content:center}.topology-lanes.single-lane::before{display:none}.topology-lane{--lane-color:var(--edo-primary);position:relative;min-width:0;padding-top:21px}.topology-lane.worker{--lane-color:#168675}.topology-lane::before{position:absolute;top:0;left:50%;height:21px;border-left:1px solid var(--topology-line);content:''}.lane-heading{position:relative;display:flex;width:max-content;max-width:100%;align-items:center;gap:7px;min-height:34px;margin:0 auto 15px;padding:5px 10px;border:1px solid var(--edo-border);border-radius:5px;background:var(--edo-surface)}.lane-heading::after{position:absolute;top:100%;left:50%;height:15px;border-left:1px solid var(--topology-line);content:''}.lane-icon{display:grid;place-items:center;color:var(--lane-color)}.lane-heading strong{font-size:12px;font-weight:600}.lane-heading>span:last-child{display:inline-flex;min-width:19px;justify-content:center;padding:0 4px;border-radius:3px;color:var(--edo-muted);background:var(--edo-surface-soft);font-size:11px}.topology-nodes{position:relative;display:grid;gap:14px;margin:0;padding:0 0 0 18px;list-style:none}.topology-nodes::before{position:absolute;top:0;bottom:50px;left:0;border-left:1px solid var(--topology-line);content:''}.topology-node-branch{position:relative;min-width:0}.topology-node-branch::before{position:absolute;top:31px;left:-18px;width:18px;border-top:1px solid var(--topology-line);content:''}.topology-node-branch:first-child::after{position:absolute;left:-18px;top:0;width:calc(50% + 9px);height:1px;border-top:1px solid var(--topology-line);content:''}.topology-node-card{display:block;width:100%;padding:13px 14px 0;border:1px solid var(--edo-border);border-radius:6px;color:var(--edo-text);background:var(--edo-surface);font:inherit;text-align:left;cursor:pointer;box-shadow:0 1px 4px rgb(16 24 40 / 2%);transition:border-color .12s,box-shadow .12s}.topology-node-card:hover:not(:disabled){border-color:color-mix(in srgb,var(--lane-color) 65%,var(--edo-border));box-shadow:0 2px 8px rgb(16 24 40 / 6%)}.topology-node-card:focus-visible{outline:2px solid var(--edo-primary);outline-offset:3px}.topology-node-card.selected{border-color:var(--edo-primary);box-shadow:0 0 0 1px var(--edo-primary)}.topology-node-card:disabled{cursor:default}.node-card-heading{display:flex;align-items:flex-start;gap:10px;min-width:0}.node-symbol{display:grid;width:32px;height:32px;flex:0 0 32px;place-items:center;border-radius:5px;color:var(--lane-color);background:color-mix(in srgb,var(--lane-color) 8%,var(--edo-surface))}.node-identity{min-width:0;flex:1}.node-identity>strong{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:13px;font-weight:600;line-height:18px}.node-roles{display:flex;flex-wrap:wrap;gap:3px 5px;margin-top:5px}.node-roles>span{display:inline-block;padding:1px 5px;border-radius:3px;color:var(--edo-primary);background:var(--edo-primary-soft);font-size:10px;line-height:15px}.node-roles>span.worker{color:#168675;background:color-mix(in srgb,#168675 10%,var(--edo-surface))}.node-open-icon{flex-shrink:0;color:var(--edo-muted)}.node-status-row{display:flex;align-items:center;gap:7px;min-width:0;margin-top:10px;font-size:10px}.node-ready{display:inline-flex;align-items:center;gap:4px;flex-shrink:0;color:var(--edo-muted)}.node-ready>i{width:5px;height:5px;border-radius:50%;background:currentColor}.node-ready.ready{color:#168675}.node-ready.not_ready{color:#c44857}.node-status-row>code{overflow:hidden;margin-left:auto;color:var(--edo-muted);text-overflow:ellipsis;white-space:nowrap;font-size:10px}.node-cordoned{padding:0 4px;border-radius:3px;color:#99640b;background:color-mix(in srgb,#c28a20 11%,var(--edo-surface));white-space:nowrap;line-height:17px}.node-resource-meters{display:grid;gap:9px;margin-top:13px}.node-resource{--resource-color:#5376a7;display:block}.node-resource:first-child{--resource-color:var(--edo-primary)}.node-resource:last-child{--resource-color:#168675}.node-resource.tone-warning{--resource-color:#ac7615}.node-resource.tone-danger{--resource-color:#c44857}.node-resource.tone-unknown{--resource-color:var(--edo-muted)}.node-resource-line{display:flex;align-items:center;gap:9px;min-width:0;font-size:11px;font-variant-numeric:tabular-nums}.resource-label{display:inline-flex;align-items:center;gap:5px;min-width:45px;color:var(--edo-muted)}.resource-values{overflow:hidden;margin-left:auto;text-align:right;text-overflow:ellipsis;white-space:nowrap;color:var(--edo-text)}.resource-values>span{color:var(--edo-muted)}.node-resource-line>strong{width:45px;flex:0 0 45px;text-align:right;font-weight:600;color:var(--resource-color)}.resource-track{display:block;height:4px;overflow:hidden;margin-top:5px;border-radius:2px;background:color-mix(in srgb,var(--edo-muted) 12%,var(--edo-surface))}.resource-track>span{display:block;height:100%;border-radius:inherit;background:var(--resource-color)}.resource-track.unknown{background:repeating-linear-gradient(110deg,var(--edo-border),var(--edo-border) 3px,var(--edo-surface-soft) 3px,var(--edo-surface-soft) 7px)}.node-card-footer{display:flex;align-items:center;justify-content:space-between;gap:10px;margin-top:13px;padding:8px 0;border-top:1px solid var(--edo-border);font-size:10px;color:var(--edo-muted)}.node-card-footer>code{flex-shrink:0;color:var(--edo-muted);font-size:10px}.node-hosts{display:inline-flex;align-items:center;gap:4px;min-width:0}.node-hosts>span{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.node-version-spacer{flex:1}.topology-legend{display:flex;align-items:center;justify-content:space-between;flex-wrap:wrap;gap:8px 18px;padding:11px 16px;border-top:1px solid var(--edo-border);font-size:11px;color:var(--edo-muted)}.topology-legend>span{display:inline-flex;align-items:center;gap:7px}.membership-line{width:22px;border-top:1px solid color-mix(in srgb,var(--edo-primary) 40%,var(--edo-border))}@media(max-width:1100px){.topology-toolbar{flex-wrap:wrap;gap:10px}.topology-title{flex:1}.topology-filters{order:3;flex-basis:100%;margin-left:0}.topology-filters>.ant-input-affix-wrapper{width:auto;flex:1}.topology-canvas{padding-inline:15px}.topology-lanes{gap:18px}.node-resource-line{gap:5px}.resource-label{min-width:40px}.node-resource-line>strong{width:42px;flex-basis:42px}}@media(max-width:720px){.topology-lanes{grid-template-columns:1fr;gap:23px;max-width:500px}.topology-lanes::before{display:none}.topology-lane::before{display:none}.topology-root{margin-bottom:20px;min-width:0;max-width:100%;width:100%;padding:11px}.topology-root::after{height:20px}.cluster-ready{margin-left:auto;font-size:10px}.topology-root strong,.topology-root code{max-width:230px}.topology-meta{align-items:flex-start;flex-direction:column;gap:4px}.topology-canvas{max-height:900px;padding:16px 12px 23px}.node-resource-line{gap:9px}.topology-lane:not(:first-child){padding-top:0}.topology-legend{align-items:flex-start;flex-direction:column}.topology-filters>.ant-select{width:120px}}@media(prefers-reduced-motion:reduce){.topology-node-card{transition:none}}
</style>

<style scoped>
.cluster-ready.degraded>i{background:#b77e16}
</style>
