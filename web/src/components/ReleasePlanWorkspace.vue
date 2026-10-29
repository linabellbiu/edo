<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Empty as AntEmpty } from 'ant-design-vue'
import {
  Boxes,
  ChevronRight,
  Clock3,
  GitBranch,
  GitCommit,
  ListChecks,
  Pencil,
  Play,
  Plus,
  Power,
  PowerOff,
  Route,
  TerminalSquare,
  Trash2,
} from 'lucide-vue-next'
import { formatDateTime, formatRelativeTime } from '@/utils/time'
import { useResourceList } from '@/utils/resourceList'
import ResourceDetailDrawer from '@/components/ResourceDetailDrawer.vue'
import ResourceRowActions from '@/components/ResourceRowActions.vue'
import '@/styles/plan-list.css'

type PlanTone = 'neutral' | 'info' | 'success' | 'danger'

interface ApplicationItem {
  id: string
  name: string
  is_active?: boolean
}

interface PipelineRunItem {
  id: string
  application_id: string
  status: string
  created_at: string
  updated_at?: string
}

interface ReleaseGroupApplication {
  id: string
  application_id: string
  application?: ApplicationItem
  manual_deploy?: boolean
  source_type?: string
  source_value?: string
  sort_order?: number
}

interface ReleaseGroupDependency {
  depends_on_group_id: string
}

interface ReleaseGroup {
  id: string
  name: string
  mode: string
  failure_policy: string
  sort_order?: number
  applications?: ReleaseGroupApplication[]
  dependencies?: ReleaseGroupDependency[]
}

interface ReleasePlanExecutionItem {
  id: string
  release_group_application_id: string
  application_id: string
  pipeline_run_id: string
  status: string
}

interface ReleasePlanItem {
  id: string
  name?: string
  description?: string
  status: string
  is_active?: boolean
  created_at: string
  updated_at?: string
  latest_execution?: {
    id: string
    status: string
    created_at: string
    finished_at?: string
    items?: ReleasePlanExecutionItem[]
  }
  groups?: ReleaseGroup[]
}

const props = defineProps<{
  plans: ReleasePlanItem[]
  applications: ApplicationItem[]
  pipelineRuns?: PipelineRunItem[]
  loading?: boolean
  canCreate?: boolean
  canUpdate?: boolean
  canDelete?: boolean
  canExecute?: boolean
  canReadDeployments?: boolean
  activePlanID?: string
  runnableCounts?: Record<string, number>
  runnableApplicationIds?: string[]
  executingPlanIds?: string[]
  relativeNow?: number
  mutatingPlanID?: string
}>()
const emit = defineEmits<{
  create: []
  execute: [planID: string]
  executeApplication: [planID: string, membershipID: string]
  edit: [planID: string]
  addApplication: [planID: string, groupID: string]
  toggle: [planID: string, enabled: boolean]
  remove: [planID: string]
  removeApplication: [planID: string, groupID: string, applicationID: string]
  openApplicationRun: [planID: string, applicationID: string, pipelineRunID: string]
  openApplicationResources: [planID: string, applicationID: string, pipelineRunID: string]
}>()
const { t, locale } = useI18n()
const { detailID: selectedPlanID, detailOpen, selected: detailPlan, pagination: planPagination, openDetails, changePage: changePlanPage } = useResourceList(
  () => props.plans,
  () => props.activePlanID || '',
)
const planColumns = computed(() => [
  { key: 'name', title: t('planList.releaseName'), width: 250 },
  { key: 'status', title: t('planList.status'), width: 120 },
  { key: 'applications', title: t('planList.applications'), width: 250 },
  { key: 'mode', title: t('planList.mode'), width: 160 },
  { key: 'updated', title: t('planList.updated'), width: 180 },
  { key: 'actions', title: t('planList.actions'), width: 235, fixed: 'right' as const, align: 'right' as const },
])

function primaryGroup(plan: ReleasePlanItem) {
  return plan.groups?.[0]
}

function planTitle(plan: ReleasePlanItem) {
  const legacyName = plan.name?.trim()
  return plan.description?.trim() || (legacyName && !/^发布计划-[0-9a-f]{8}$/i.test(legacyName) ? legacyName : '') || t('releasePlan.unnamed')
}

function planStatus(status: string): { tone: PlanTone; label: string; hint: string; live: boolean } {
  const states: Record<string, { tone: PlanTone; label: string; hint: string; live: boolean }> = {
    draft: { tone: 'neutral', label: t('releasePlan.status.draft'), hint: t('releasePlan.statusHint.draft'), live: false },
    active: { tone: 'info', label: t('releasePlan.status.active'), hint: t('releasePlan.statusHint.active'), live: false },
    completed: { tone: 'success', label: t('releasePlan.status.completed'), hint: t('releasePlan.statusHint.completed'), live: false },
    pending: { tone: 'info', label: t('releasePlan.status.pending'), hint: t('releasePlan.statusHint.executing'), live: true },
    running: { tone: 'info', label: t('releasePlan.status.running'), hint: t('releasePlan.statusHint.executing'), live: true },
    succeeded: { tone: 'success', label: t('releasePlan.status.succeeded'), hint: t('releasePlan.statusHint.execution'), live: false },
    failed: { tone: 'danger', label: t('releasePlan.status.failed'), hint: t('releasePlan.statusHint.execution'), live: false },
    skipped: { tone: 'neutral', label: t('releasePlan.status.skipped'), hint: t('releasePlan.statusHint.execution'), live: false },
    disabled: { tone: 'neutral', label: t('releasePlan.status.disabled'), hint: t('releasePlan.statusHint.disabled'), live: false },
    canceled: { tone: 'neutral', label: t('releasePlan.status.canceled'), hint: t('releasePlan.statusHint.canceled'), live: false },
  }
  return states[status] || { tone: 'neutral', label: t('releasePlan.status.unknown'), hint: t('releasePlan.statusHint.unknown'), live: false }
}

function visiblePlanStatus(plan: ReleasePlanItem) {
  if (plan.is_active === false) return 'disabled'
  if (props.executingPlanIds?.includes(plan.id)) return 'running'
  if (plan.latest_execution?.status === 'pending' || plan.latest_execution?.status === 'running') return plan.latest_execution.status
  const latestRunStatus = latestPlanRun(plan)?.status
  if (latestRunStatus === 'detected' || latestRunStatus === 'ready' || latestRunStatus === 'awaiting_approval') return 'running'
  if (latestRunStatus === 'blocked') return 'failed'
  if (latestRunStatus) return latestRunStatus
  return plan.latest_execution?.status || plan.status
}

function planMutationBlocked(plan: ReleasePlanItem) {
  return Boolean(props.executingPlanIds?.includes(plan.id)) || plan.latest_execution?.status === 'pending' || plan.latest_execution?.status === 'running'
}

function canExecutePlan(plan: ReleasePlanItem) {
  const applications = planApplications(plan)
  return plan.is_active !== false && !planMutationBlocked(plan) && plan.status !== 'canceled' && applications.length > 0 && manualApplicationCount(plan) === applications.length
}

function canExecuteApplication(plan: ReleasePlanItem, item: ReleaseGroupApplication) {
  const runnable = props.runnableApplicationIds?.includes(item.application_id) ?? Boolean(item.manual_deploy)
  return plan.is_active !== false && !planMutationBlocked(plan) && plan.status !== 'canceled' && applicationEnabled(item) && runnable
}

function planApplications(plan: ReleasePlanItem) {
  return plan.groups?.[0]?.applications || []
}

function applicationCount(plan: ReleasePlanItem) {
  return new Set(planApplications(plan).map((item) => item.application_id)).size
}

function manualApplicationCount(plan: ReleasePlanItem) {
  return props.runnableCounts?.[plan.id] ?? planApplications(plan).filter((item) => item.manual_deploy).length
}

function applicationName(item: ReleaseGroupApplication) {
  return item.application?.name || props.applications.find((application) => application.id === item.application_id)?.name || t('releasePlan.unknownApplication')
}

function applicationEnabled(item: ReleaseGroupApplication) {
  return item.application?.is_active ?? props.applications.find((application) => application.id === item.application_id)?.is_active ?? true
}

function latestRun(runs: PipelineRunItem[]) {
  return runs.reduce<PipelineRunItem | undefined>((latest, run) => {
    if (!latest) return run
    return Date.parse(run.created_at) > Date.parse(latest.created_at) ? run : latest
  }, undefined)
}

function latestApplicationRun(item: ReleaseGroupApplication) {
  return latestRun((props.pipelineRuns || []).filter((run) => run.application_id === item.application_id))
}

function latestPlanRun(plan: ReleasePlanItem) {
  const applicationIDs = new Set(planApplications(plan).map((item) => item.application_id))
  return latestRun((props.pipelineRuns || []).filter((run) => applicationIDs.has(run.application_id)))
}

function planDisplayTime(plan: ReleasePlanItem) {
  const run = latestPlanRun(plan)
  return run?.updated_at || run?.created_at || plan.updated_at || plan.created_at
}

function executionItem(plan: ReleasePlanItem, item: ReleaseGroupApplication) {
  const run = latestApplicationRun(item)
  if (run) return { pipeline_run_id: run.id, status: run.status }
  return plan.latest_execution?.items?.find((candidate) => candidate.release_group_application_id === item.id)
    || plan.latest_execution?.items?.find((candidate) => candidate.application_id === item.application_id)
}

function executionItemStatus(status?: string) {
  const states: Record<string, { tone: PlanTone; label: string }> = {
    pending: { tone: 'info', label: '等待执行' },
    detected: { tone: 'info', label: '等待执行' },
    ready: { tone: 'info', label: '等待继续' },
    running: { tone: 'info', label: '正在执行' },
    awaiting_approval: { tone: 'info', label: '等待审核' },
    succeeded: { tone: 'success', label: '最近执行成功' },
    failed: { tone: 'danger', label: '最近执行失败' },
    blocked: { tone: 'danger', label: '最近执行阻塞' },
    skipped: { tone: 'neutral', label: '已跳过' },
    canceled: { tone: 'neutral', label: '已取消' },
  }
  return states[status || ''] || { tone: 'neutral' as const, label: status || '暂无执行记录' }
}

function formatTime(value?: string) {
  return formatDateTime(value, locale.value === 'en-US' ? 'en-US' : 'zh-CN', t('releasePlan.noTime'))
}

function relativeTime(value?: string) {
  return formatRelativeTime(value, props.relativeNow || Date.now(), locale.value === 'en-US' ? 'en-US' : 'zh-CN', t('releasePlan.noTime'))
}

function groupMode(mode: string) {
  return mode === 'sequential' ? t('releasePlan.groupMode.sequential') : t('releasePlan.groupMode.parallel')
}

function failurePolicy(policy: string) {
  return policy === 'continue' ? t('releasePlan.failurePolicy.continue') : t('releasePlan.failurePolicy.stop')
}

function sourceMeta(item: ReleaseGroupApplication) {
  if (!item.manual_deploy) return { icon: Route, label: t('releasePlan.followPipeline'), value: t('releasePlan.autoSource') }
  const isCommit = item.source_type === 'commit'
  const value = isCommit ? item.source_value?.slice(0, 12) : item.source_value?.replace(/^refs\/heads\//, '')
  return {
    icon: isCommit ? GitCommit : GitBranch,
    label: isCommit ? t('releasePlan.commit') : t('releasePlan.branch'),
    value: value || t('releasePlan.sourcePending'),
  }
}
</script>

<template>
  <div v-if="loading && !plans.length" class="plan-loading vben-card">
    <a-skeleton active :paragraph="{ rows: 8 }" />
  </div>

  <div v-else-if="plans.length" class="expandable-plan-list vben-card">
    <a-table
      class="plan-table resource-list-table" row-key="id" size="small" table-layout="fixed" :columns="planColumns" :data-source="plans"
      :pagination="planPagination" :scroll="{ x: 1300 }"
      :row-class-name="(plan: ReleasePlanItem) => detailOpen && selectedPlanID === plan.id ? 'resource-selected' : ''"
      @change="changePlanPage"
    >
      <template #bodyCell="{ column, record: plan }">
        <div v-if="column.key === 'name'" class="plan-cell">
          <button class="resource-name" :title="planTitle(plan)" @click="openDetails(plan)">{{ planTitle(plan) }}</button>
          <small>{{ t('releasePlan.createdAt', { time: formatTime(plan.created_at) }) }}</small>
        </div>
        <span v-else-if="column.key === 'status'" class="plan-status" :class="[`tone-${planStatus(visiblePlanStatus(plan)).tone}`, { live: planStatus(visiblePlanStatus(plan)).live }]" :title="planStatus(visiblePlanStatus(plan)).hint">
          <i />{{ planStatus(visiblePlanStatus(plan)).label }}
        </span>
        <div v-else-if="column.key === 'applications'" class="plan-cell">
          <div class="plan-app-preview" :title="planApplications(plan).map(applicationName).join('、')">
            <span v-for="item in planApplications(plan).slice(0, 2)" :key="item.id">{{ applicationName(item) }}</span>
            <small v-if="applicationCount(plan) > 2">{{ t('planList.applicationOverflow', { count: applicationCount(plan) - 2 }) }}</small>
          </div>
          <small>{{ t('releasePlan.applicationCount', { count: applicationCount(plan) }) }} · {{ t('planList.manualCount', { count: manualApplicationCount(plan) }) }}</small>
        </div>
        <div v-else-if="column.key === 'mode'" class="plan-cell">
          <strong>{{ groupMode(primaryGroup(plan)?.mode || 'parallel') }}</strong>
          <small>{{ failurePolicy(primaryGroup(plan)?.failure_policy || 'stop') }}</small>
        </div>
        <div v-else-if="column.key === 'updated'" class="plan-cell">
          <time :datetime="planDisplayTime(plan)">{{ formatTime(planDisplayTime(plan)) }}</time><small>{{ relativeTime(planDisplayTime(plan)) }}</small>
        </div>
        <ResourceRowActions v-else-if="column.key === 'actions'" :more="canUpdate || canDelete" :loading="mutatingPlanID === plan.id">
          <a-button size="small" @click="openDetails(plan)">{{ t('resourceList.details') }}</a-button>
          <a-button v-if="canExecute && canExecutePlan(plan)" size="small" @click="emit('execute', plan.id)"><Play :size="14" />{{ t('releasePlan.execute') }}</a-button>
          <template #more>
            <a-menu-item v-if="canUpdate && !planMutationBlocked(plan)" @click="emit('edit', plan.id)">{{ t('releasePlan.actions.edit') }}</a-menu-item>
            <a-menu-item v-if="canUpdate" :disabled="mutatingPlanID === plan.id" @click="emit('toggle', plan.id, plan.is_active === false)">{{ plan.is_active === false ? t('releasePlan.actions.enable') : t('releasePlan.actions.disable') }}</a-menu-item>
            <a-menu-divider v-if="canDelete" />
            <a-menu-item v-if="canDelete && !planMutationBlocked(plan)" danger :disabled="mutatingPlanID === plan.id" @click="emit('remove', plan.id)">{{ t('releasePlan.actions.remove') }}</a-menu-item>
          </template>
        </ResourceRowActions>
      </template>
    </a-table>
  </div>

  <div v-else class="plan-empty vben-card">
    <span><ListChecks /></span>
    <h3>{{ t('releasePlan.emptyTitle') }}</h3>
    <p>{{ t('releasePlan.emptyDescription') }}</p>
    <a-button v-if="canCreate" type="primary" @click="emit('create')">{{ t('releasePlan.create') }}</a-button>
  </div>
  <ResourceDetailDrawer v-model:open="detailOpen" :title="detailPlan ? planTitle(detailPlan) : ''">
    <template #actions>
      <a-button v-if="detailPlan && canExecute && canExecutePlan(detailPlan)" size="small" type="primary" @click="emit('execute', detailPlan.id)"><Play :size="14" />{{ t('releasePlan.execute') }}</a-button>
      <a-button v-if="detailPlan && canUpdate && !planMutationBlocked(detailPlan)" size="small" @click="emit('edit', detailPlan.id)">{{ t('releasePlan.actions.edit') }}</a-button>
      <span v-if="detailPlan" class="resource-secondary">{{ groupMode(primaryGroup(detailPlan)?.mode || 'parallel') }} · {{ failurePolicy(primaryGroup(detailPlan)?.failure_policy || 'stop') }}</span>
    </template>
    <section v-for="plan in detailPlan ? [detailPlan] : []" :key="plan.id" :id="`release-plan-${plan.id}`" :aria-label="planTitle(plan)" class="resource-detail-section" :class="{ 'is-current': selectedPlanID === plan.id }">
          <section class="plan-groups">
            <header>
              <h3>{{ t('releasePlan.applicationsTitle') }}</h3>
              <div class="plan-group-heading-actions">
                <span>{{ t('releasePlan.applicationCount', { count: applicationCount(plan) }) }}</span>
                <a-button v-if="primaryGroup(plan) && canUpdate && !planMutationBlocked(plan)" size="small" type="text" :disabled="mutatingPlanID === plan.id" @click="emit('addApplication', plan.id, primaryGroup(plan)!.id)">
                  <Plus :size="13" />{{ t('releasePlan.editor.addApplication') }}
                </a-button>
              </div>
            </header>

            <div v-if="primaryGroup(plan)" class="plan-group-list">
              <article class="plan-group-row">
                <div v-if="primaryGroup(plan)!.applications?.length" class="plan-application-grid">
                  <div v-for="item in primaryGroup(plan)!.applications" :key="item.application_id" class="plan-application">
                    <span class="plan-app-mark"><Boxes /></span>
                    <div class="plan-app-copy">
                      <strong :title="applicationName(item)">{{ applicationName(item) }}</strong>
                      <span :title="item.source_value">
                        <component :is="sourceMeta(item).icon" />
                        {{ sourceMeta(item).label }}：{{ sourceMeta(item).value }}
                      </span>
                      <span v-if="executionItem(plan, item)" class="plan-app-execution" :class="`tone-${executionItemStatus(executionItem(plan, item)?.status).tone}`">
                        <Route />{{ executionItemStatus(executionItem(plan, item)?.status).label }}
                      </span>
                      <time v-if="latestApplicationRun(item)" class="plan-app-time" :datetime="latestApplicationRun(item)!.created_at" :title="formatTime(latestApplicationRun(item)!.created_at)">
                        <Clock3 /><span>{{ formatTime(latestApplicationRun(item)!.created_at) }}</span><em>{{ relativeTime(latestApplicationRun(item)!.created_at) }}</em>
                      </time>
                    </div>
                    <div class="plan-app-actions">
                      <em :class="{ disabled: !applicationEnabled(item) }">{{ applicationEnabled(item) ? t('releasePlan.enabled') : t('releasePlan.disabled') }}</em>
                      <a-button
                        v-if="canExecute && canExecuteApplication(plan, item)"
                        type="text"
                        size="small"
                        @click="emit('executeApplication', plan.id, item.id)"
                      >
                        <Play :size="13" />{{ t('releasePlan.executeApplication') }}
                      </a-button>
                      <a-button
                        v-if="executionItem(plan, item)?.pipeline_run_id"
                        type="text"
                        size="small"
                        @click="emit('openApplicationRun', plan.id, item.application_id, executionItem(plan, item)!.pipeline_run_id)"
                      >
                        <ListChecks :size="13" />流水线记录
                      </a-button>
                      <a-button
                        v-if="executionItem(plan, item)?.pipeline_run_id && canReadDeployments"
                        type="text"
                        size="small"
                        @click="emit('openApplicationResources', plan.id, item.application_id, executionItem(plan, item)!.pipeline_run_id)"
                      >
                        <TerminalSquare :size="13" />部署资源
                      </a-button>
                      <em v-else-if="executionItem(plan, item)?.pipeline_run_id && !canReadDeployments" class="disabled">无部署权限</em>
                      <a-popconfirm
                        v-if="canUpdate && !planMutationBlocked(plan)"
                        :title="t('releasePlan.editor.removeApplicationConfirm', { name: applicationName(item) })"
                        :description="t('releasePlan.editor.removeApplicationHint')"
                        :ok-text="t('releasePlan.editor.remove')"
                        :cancel-text="t('releasePlan.editor.cancel')"
                        ok-type="danger"
                        @confirm="emit('removeApplication', plan.id, primaryGroup(plan)!.id, item.application_id)"
                      >
                        <a-button danger type="text" size="small" :disabled="mutatingPlanID === plan.id">
                          <Trash2 :size="13" />{{ t('releasePlan.editor.removeApplication') }}
                        </a-button>
                      </a-popconfirm>
                    </div>
                  </div>
                </div>
                <a-empty v-else :image="AntEmpty.PRESENTED_IMAGE_SIMPLE" :description="t('releasePlan.emptyApplications')" />
              </article>
            </div>
            <a-empty v-else :image="AntEmpty.PRESENTED_IMAGE_SIMPLE" :description="t('releasePlan.emptyApplications')" />
          </section>
        </section>
  </ResourceDetailDrawer>
</template>

<style scoped>
.plan-loading{padding:20px}
.plan-status{display:inline-flex;align-items:center;gap:7px;color:var(--edo-muted);font-size:12px}.plan-status i{width:7px;height:7px;border-radius:50%;background:currentColor}
.plan-status.tone-info{color:var(--edo-primary)}.plan-status.tone-success{color:var(--edo-success,#2ab573)}.plan-status.tone-danger{color:var(--edo-danger,#ed5965)}
.plan-status.live i{animation:plan-pulse 2s ease-out infinite}@keyframes plan-pulse{0%{box-shadow:0 0 0 0 color-mix(in srgb,currentColor 30%,transparent)}70%{box-shadow:0 0 0 6px transparent}100%{box-shadow:0 0 0 0 transparent}}@media(prefers-reduced-motion:reduce){.plan-status.live i{animation:none}}
.plan-app-preview{display:flex;min-width:0;align-items:center;gap:5px}.plan-app-preview>span{max-width:100px;overflow:hidden;padding:2px 6px;border-radius:4px;background:var(--edo-surface-soft);font-size:12px;text-overflow:ellipsis;white-space:nowrap}.plan-app-preview>small{flex-shrink:0;margin:0}
.plan-groups{overflow:hidden;border:1px solid var(--edo-border);border-radius:8px}.plan-groups>header{display:flex;align-items:center;justify-content:space-between;gap:12px;padding:10px 12px;background:var(--edo-surface-soft)}.plan-groups>header small{display:none}.plan-groups>header h3{margin:0;font-size:13px}.plan-group-heading-actions{display:flex;align-items:center;gap:7px}.plan-group-heading-actions>span{color:var(--edo-muted);font-size:12px}.plan-group-heading-actions :deep(.ant-btn){display:inline-flex;align-items:center;gap:4px}
.plan-application-grid{display:grid;grid-template-columns:1fr}.plan-application{display:grid;min-width:0;align-items:center;grid-template-columns:30px minmax(0,1fr) auto;gap:10px;padding:10px 12px;background:var(--edo-surface)}.plan-application+.plan-application{border-top:1px solid var(--edo-border)}.plan-app-mark{display:grid;width:30px;height:30px;place-items:center;border-radius:7px;color:var(--edo-primary);background:var(--edo-primary-soft)}.plan-app-mark svg{width:16px}
.plan-app-copy{display:grid;min-width:0;grid-template-columns:minmax(140px,1fr) minmax(220px,1fr);gap:4px 20px}.plan-app-copy strong,.plan-app-copy>span{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.plan-app-copy strong{grid-column:1;grid-row:1;font-size:13px}.plan-app-copy>span{display:flex;align-items:center;gap:4px;grid-column:1;grid-row:2;color:var(--edo-muted);font-size:12px}.plan-app-copy>span svg{width:13px;height:13px;flex:0 0 13px}.plan-app-copy>.plan-app-execution{grid-column:2;grid-row:1}.plan-app-copy .tone-info{color:var(--edo-primary)}.plan-app-copy .tone-success{color:var(--edo-success,#2ab573)}.plan-app-copy .tone-danger{color:var(--edo-danger,#ed5965)}
.plan-app-time{display:flex;min-width:0;align-items:center;gap:4px;grid-column:2;grid-row:2;color:var(--edo-muted);font-size:12px}.plan-app-time svg{width:12px;flex:0 0 12px}.plan-app-time span{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.plan-app-time em{flex:0 0 auto;font-style:normal;white-space:nowrap}
.plan-app-actions{display:flex;align-items:center;justify-content:flex-end;gap:3px}.plan-app-actions em{padding:3px 6px;border-radius:4px;color:var(--edo-success,#2ab573);background:var(--edo-surface-soft);font-size:12px;font-style:normal;white-space:nowrap}.plan-app-actions em.disabled{color:var(--edo-muted)}.plan-app-actions :deep(.ant-btn){display:inline-flex;align-items:center;gap:4px;padding-inline:5px}
.plan-empty{display:grid;min-height:460px;place-items:center;align-content:center;padding:32px;text-align:center}.plan-empty>span{display:grid;width:58px;height:58px;place-items:center;border-radius:18px;color:var(--edo-primary);background:var(--edo-primary-soft)}.plan-empty>span svg{width:25px}.plan-empty h3{margin:15px 0 0;font-size:16px}.plan-empty p{max-width:420px;margin:6px 0 16px;color:var(--edo-muted);font-size:12px}
@media(max-width:1100px){.plan-app-copy{grid-template-columns:minmax(0,1fr)}.plan-app-copy>.plan-app-execution{grid-column:1;grid-row:3}.plan-app-time{grid-column:1;grid-row:4}}
@media(max-width:760px){.plan-application{grid-template-columns:30px minmax(0,1fr)}.plan-app-actions{grid-column:2;justify-content:flex-start;flex-wrap:wrap}.plan-groups>header{flex-wrap:wrap}}
.plan-groups>header h3{font-size:14px}.plan-application{min-height:64px}.plan-app-actions{flex-wrap:wrap}.plan-app-actions :deep(.ant-btn){font-size:12px}.plan-app-copy strong{font-size:13px}
</style>
