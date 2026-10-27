<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { K3sPreflightReport } from '@/api/k3s'
import { formatDateTime } from '@/utils/time'

const props = defineProps<{report:K3sPreflightReport}>()
const { t } = useI18n()
const groups = computed(() => [
  ...(props.report.checks.length?[{id:'cluster',name:'集群检查',role:'',hostname:'',checks:props.report.checks}]:[]),
  ...props.report.hosts.map(host=>({id:host.host_id,name:host.host_name,role:host.role==='host'?t('k8s.installationHost'):host.role==='server'?'控制平面（Master）':'工作节点（Worker）',hostname:host.hostname,checks:host.checks})),
])
const checks = computed(() => groups.value.flatMap(group=>group.checks))
const failed = computed(() => checks.value.filter(check=>check.status==='failed').length)
const warnings = computed(() => checks.value.filter(check=>check.status==='warning').length)
const labels = {passed:'通过',warning:'提示',failed:'阻断'}
const colors = {passed:'green',warning:'orange',failed:'red'}
</script>

<template>
  <section class="preflight-report" aria-label="安装前置检查报告">
    <a-alert :type="report.passed?(warnings?'warning':'success'):'error'" show-icon :message="report.passed?(warnings?`前置检查通过，含 ${warnings} 项提示`:'前置检查通过'):`前置检查未通过${failed?`，${failed} 项阻断`:''}`" description="本次检查为只读操作。请处理阻断项后重新检查；安装前会再次实时复查。" />
    <div class="preflight-summary"><span>检查时间：{{ formatDateTime(report.checked_at,'zh-CN') }}</span><span>{{ report.hosts.length }} 台主机 · {{ checks.length }} 项检查</span></div>
    <details v-for="group in groups" :key="group.id" :open="group.checks.some(check=>check.status!=='passed')" class="preflight-group">
      <summary><strong>{{ group.name }}</strong><span v-if="group.role">{{ group.role }}</span><code v-if="group.hostname">{{ group.hostname }}</code><a-tag :color="group.checks.some(check=>check.status==='failed')?'red':group.checks.some(check=>check.status==='warning')?'orange':'green'">{{ group.checks.some(check=>check.status==='failed')?'需处理':group.checks.some(check=>check.status==='warning')?'含提示':'通过' }}</a-tag></summary>
      <ul class="preflight-checks"><li v-for="check in group.checks" :key="check.id"><a-tag :color="colors[check.status]">{{ labels[check.status] }}</a-tag><div><strong>{{ check.name }}</strong><p>{{ check.message }}</p></div></li></ul>
    </details>
  </section>
</template>

<style scoped>
.preflight-report{display:grid;gap:12px}.preflight-summary{display:flex;justify-content:space-between;gap:8px;flex-wrap:wrap;color:var(--edo-muted);font-size:11px}.preflight-group{border:1px solid var(--edo-border);border-radius:6px;min-width:0}.preflight-group summary{display:flex;align-items:center;gap:9px;flex-wrap:wrap;padding:12px;cursor:pointer;font-size:12px}.preflight-group summary strong{font-weight:600;overflow-wrap:anywhere}.preflight-group summary>span,.preflight-group code{color:var(--edo-muted)}.preflight-group summary .ant-tag{margin-left:auto;flex:none}.preflight-checks{list-style:none;margin:0;padding:0 12px 4px}.preflight-checks li{display:flex;align-items:flex-start;gap:10px;padding:10px 0;border-top:1px solid var(--edo-border);font-size:12px}.preflight-checks .ant-tag{margin:0;flex:none}.preflight-checks strong{font-weight:500}.preflight-checks p{margin:4px 0 0;color:var(--edo-muted);line-height:1.65;white-space:pre-wrap;overflow-wrap:anywhere}.preflight-checks li>div{min-width:0}
</style>
