<script setup lang="ts">
import { computed, ref } from 'vue'
import type { K3sPreflightReport, K3sInstallMode } from '@/api/k3s'

const props = defineProps<{report:K3sPreflightReport;mode?:K3sInstallMode;standalone?:boolean}>()
const root=ref<HTMLElement>()
defineExpose({focus:()=>root.value?.focus()})
const groups = computed(() => [
  ...(props.report.checks.length?[{id:'cluster',name:'集群检查',checks:props.report.checks}]:[]),
  ...props.report.hosts.map(host=>({id:host.host_id,name:host.host_name,checks:host.checks})),
])
const checks = computed(() => groups.value.flatMap(group=>group.checks))
const failed = computed(() => checks.value.filter(check=>check.status==='failed').length)
const failedGroups=computed(()=>groups.value.map(group=>({...group,checks:group.checks.filter(check=>check.status==='failed')})).filter(group=>group.checks.length))
const safetyMessage=computed(()=>{
  if(!props.report.hosts.length||props.report.hosts.some(host=>!host.checks.length))return '检测结果不完整，请重新检测。'
  if(props.report.passed&&(failed.value>0||props.report.hosts.some(host=>!host.passed))||!props.report.passed&&failed.value===0||props.report.hosts.some(host=>host.passed&&host.checks.some(check=>check.status==='failed')))return '检测结果未确认通过，请重新检测。'
  return ''
})
</script>

<template>
  <section ref="root" class="preflight-report" aria-label="安装前置检查报告" tabindex="-1">
    <a-alert v-if="safetyMessage" type="error" show-icon :message="safetyMessage" role="status" />
    <p v-else-if="!failed" class="preflight-clear" role="status">未发现不满足项</p>
    <section v-for="group in failedGroups" :key="group.id" class="preflight-group">
      <h4>{{ group.name }}</h4>
      <ul class="preflight-checks"><li v-for="check in group.checks" :key="check.id"><a-tag color="red">不满足</a-tag><div><strong>{{ check.name }}</strong><p>{{ check.message }}</p></div></li></ul>
    </section>
    <a class="preflight-docs" href="https://docs.k3s.io/zh/installation/configuration" target="_blank" rel="noopener noreferrer">官方安装说明</a>
  </section>
</template>

<style scoped>
.preflight-report{display:grid;grid-template-columns:minmax(0,1fr);min-width:0;gap:12px}.preflight-report>*{min-width:0}.preflight-report :deep(.ant-alert-content){min-width:0;overflow-wrap:anywhere}.preflight-clear{margin:0;font-size:13px}.preflight-group{min-width:0}.preflight-group h4{margin:0 0 4px;font-size:12px;font-weight:600;overflow-wrap:anywhere}.preflight-checks{list-style:none;margin:0;padding:0}.preflight-checks li{display:flex;align-items:flex-start;gap:10px;padding:10px 0;border-bottom:1px solid var(--edo-border);font-size:12px}.preflight-checks .ant-tag{margin:0;flex:none}.preflight-checks strong{font-weight:500}.preflight-checks p{margin:4px 0 0;color:var(--edo-muted);line-height:1.65;white-space:pre-wrap;overflow-wrap:anywhere}.preflight-checks li>div{min-width:0}.preflight-docs{justify-self:start;font-size:12px}
</style>
