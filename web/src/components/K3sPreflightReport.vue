<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { K3sPreflightReport, K3sInstallMode } from '@/api/k3s'
import { formatDateTime } from '@/utils/time'

const props = defineProps<{report:K3sPreflightReport;mode?:K3sInstallMode;standalone?:boolean}>()
const root=ref<HTMLElement>()
defineExpose({focus:()=>root.value?.focus()})
const { t } = useI18n()
const groups = computed(() => [
  ...(props.report.checks.length?[{id:'cluster',name:'集群检查',role:'',hostname:'',passed:props.report.checks.every(check=>check.status!=='failed'),checks:props.report.checks}]:[]),
  ...props.report.hosts.map(host=>({id:host.host_id,name:host.host_name,role:host.role==='host'?t('k8s.installationHost'):host.role==='server'?'控制平面（Master）':'工作节点（Worker）',hostname:host.hostname,passed:host.passed,checks:host.checks})),
])
const checks = computed(() => groups.value.flatMap(group=>group.checks))
const failed = computed(() => checks.value.filter(check=>check.status==='failed').length)
const warnings = computed(() => checks.value.filter(check=>check.status==='warning').length)
const passed = computed(() => props.report.passed&&props.report.hosts.length>0&&props.report.hosts.every(host=>host.passed&&host.checks.length>0)&&failed.value===0)
const labels = {passed:'通过',warning:'警告',failed:'不满足'}
const colors = {passed:'green',warning:'orange',failed:'red'}
</script>

<template>
  <section ref="root" class="preflight-report" aria-label="安装前置检查报告" tabindex="-1">
    <a-alert :type="passed?(warnings?'warning':'success'):'error'" show-icon :message="passed?(warnings?`安装前检测通过，含 ${warnings} 项警告`:'安装前检测通过'):`安装前检测未通过${failed?`，${failed} 项不满足`:''}`" :description="standalone?'只读检测，不创建安装计划、不安装或修改主机；开始安装前仍会再次复查。':'请处理不满足项后重新检测；开始安装前会再次实时复查。'" role="status" />
    <div class="preflight-summary"><span v-if="mode">检测模式：{{ mode==='k3d'?'k3d':'原生 K3s' }}</span><span>检测时间：{{ formatDateTime(report.checked_at,'zh-CN') }}</span><span>{{ report.hosts.length }} 台主机 · {{ checks.length }} 项检测</span></div>
    <details v-for="group in groups" :key="group.id" :open="group.checks.some(check=>check.status!=='passed')" class="preflight-group">
      <summary><strong>{{ group.name }}</strong><span v-if="group.role">{{ group.role }}</span><code v-if="group.hostname">{{ group.hostname }}</code><a-tag :color="!group.checks.length?undefined:!group.passed||group.checks.some(check=>check.status==='failed')?'red':group.checks.some(check=>check.status==='warning')?'orange':'green'">{{ !group.checks.length?'未取得检测结果':!group.passed||group.checks.some(check=>check.status==='failed')?'不满足':group.checks.some(check=>check.status==='warning')?'含警告':'通过' }}</a-tag></summary>
      <ul class="preflight-checks"><li v-for="check in group.checks" :key="check.id"><a-tag :color="colors[check.status]">{{ labels[check.status] }}</a-tag><div><strong>{{ check.name }}</strong><p>{{ check.message }}</p></div></li></ul>
    </details>
  </section>
</template>

<style scoped>
.preflight-report{display:grid;gap:12px}.preflight-summary{display:flex;justify-content:space-between;gap:8px;flex-wrap:wrap;color:var(--edo-muted);font-size:11px}.preflight-group{border:1px solid var(--edo-border);border-radius:6px;min-width:0}.preflight-group summary{display:flex;align-items:center;gap:9px;flex-wrap:wrap;padding:12px;cursor:pointer;font-size:12px}.preflight-group summary strong{font-weight:600;overflow-wrap:anywhere}.preflight-group summary>span,.preflight-group code{color:var(--edo-muted)}.preflight-group summary .ant-tag{margin-left:auto;flex:none}.preflight-checks{list-style:none;margin:0;padding:0 12px 4px}.preflight-checks li{display:flex;align-items:flex-start;gap:10px;padding:10px 0;border-top:1px solid var(--edo-border);font-size:12px}.preflight-checks .ant-tag{margin:0;flex:none}.preflight-checks strong{font-weight:500}.preflight-checks p{margin:4px 0 0;color:var(--edo-muted);line-height:1.65;white-space:pre-wrap;overflow-wrap:anywhere}.preflight-checks li>div{min-width:0}
</style>
