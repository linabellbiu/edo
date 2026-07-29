<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Empty as AntEmpty } from 'ant-design-vue'
import {
  Boxes,
  CalendarDays,
  ChevronRight,
  GitBranch,
  GitCommit,
  Layers3,
  ListChecks,
  Pencil,
  Play,
  Plus,
  Power,
  PowerOff,
  Route,
  Trash2,
  UsersRound,
} from 'lucide-vue-next'

type PlanTone = 'neutral' | 'info' | 'success' | 'danger'

interface ApplicationItem {
  id: string
  name: string
  is_active?: boolean
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
  }
  groups?: ReleaseGroup[]
}

const props = defineProps<{
  plans: ReleasePlanItem[]
  applications: ApplicationItem[]
  loading?: boolean
  canManage?: boolean
  canRun?: boolean
  runnableCounts?: Record<string, number>
  mutatingPlanID?: string
}>()
const emit = defineEmits<{
  create: []
  execute: [planID: string]
  edit: [planID: string, groupID?: string, addGroup?: boolean]
  toggle: [planID: string, enabled: boolean]
  remove: [planID: string]
  removeGroup: [planID: string, groupID: string]
}>()
const { t, locale } = useI18n()
const selectedPlanID = ref('')

const selectedPlan = computed(() => props.plans.find((plan) => plan.id === selectedPlanID.value) || props.plans[0] || null)

watch(
  () => props.plans.map((plan) => plan.id),
  (ids) => {
    if (!ids.includes(selectedPlanID.value)) selectedPlanID.value = ids[0] || ''
  },
  { immediate: true },
)

function planTitle(plan: ReleasePlanItem) {
  const legacyName = plan.name?.trim()
  return plan.description?.trim() || (legacyName && !/^发布计划-[0-9a-f]{8}$/i.test(legacyName) ? legacyName : '') || t('releasePlan.unnamed')
}

function planStatus(status: string): { tone: PlanTone; label: string; hint: string; live: boolean } {
  const states: Record<string, { tone: PlanTone; label: string; hint: string; live: boolean }> = {
    draft: { tone: 'neutral', label: t('releasePlan.status.draft'), hint: t('releasePlan.statusHint.draft'), live: false },
    active: { tone: 'info', label: t('releasePlan.status.active'), hint: t('releasePlan.statusHint.active'), live: true },
    completed: { tone: 'success', label: t('releasePlan.status.completed'), hint: t('releasePlan.statusHint.completed'), live: false },
    pending: { tone: 'info', label: t('releasePlan.status.pending'), hint: t('releasePlan.statusHint.executing'), live: true },
    running: { tone: 'info', label: t('releasePlan.status.running'), hint: t('releasePlan.statusHint.executing'), live: true },
    succeeded: { tone: 'success', label: t('releasePlan.status.succeeded'), hint: t('releasePlan.statusHint.execution'), live: false },
    failed: { tone: 'danger', label: t('releasePlan.status.failed'), hint: t('releasePlan.statusHint.execution'), live: false },
    disabled: { tone: 'neutral', label: t('releasePlan.status.disabled'), hint: t('releasePlan.statusHint.disabled'), live: false },
    canceled: { tone: 'neutral', label: t('releasePlan.status.canceled'), hint: t('releasePlan.statusHint.canceled'), live: false },
  }
  return states[status] || { tone: 'neutral', label: t('releasePlan.status.unknown'), hint: t('releasePlan.statusHint.unknown'), live: false }
}

function visiblePlanStatus(plan: ReleasePlanItem) {
  if (plan.is_active === false) return 'disabled'
  return plan.latest_execution?.status || plan.status
}

function planMutationBlocked(plan: ReleasePlanItem) {
  return plan.latest_execution?.status === 'pending' || plan.latest_execution?.status === 'running'
}

function canExecutePlan(plan: ReleasePlanItem) {
  const applications = planApplications(plan)
  return plan.is_active !== false && !plan.latest_execution && !['completed', 'canceled'].includes(plan.status) && applications.length > 0 && manualApplicationCount(plan) === applications.length
}

function planApplications(plan: ReleasePlanItem) {
  return (plan.groups || []).flatMap((group) => group.applications || [])
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

function formatTime(value?: string) {
  if (!value) return t('releasePlan.noTime')
  return new Date(value).toLocaleString(locale.value === 'en-US' ? 'en-US' : 'zh-CN', { hour12: false })
}

function groupMode(mode: string) {
  return mode === 'sequential' ? t('releasePlan.groupMode.sequential') : t('releasePlan.groupMode.parallel')
}

function failurePolicy(policy: string) {
  return policy === 'continue' ? t('releasePlan.failurePolicy.continue') : t('releasePlan.failurePolicy.stop')
}

function dependencyLabel(plan: ReleasePlanItem, group: ReleaseGroup) {
  const names = (group.dependencies || [])
    .map((dependency) => plan.groups?.find((candidate) => candidate.id === dependency.depends_on_group_id)?.name)
    .filter(Boolean)
  return names.length ? t('releasePlan.dependsOn', { names: names.join('、') }) : t('releasePlan.noDependency')
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

  <div v-else-if="plans.length" class="plan-workspace vben-card">
    <aside class="plan-index">
      <header>
        <div>
          <strong>{{ t('releasePlan.listTitle') }}</strong>
          <small>{{ t('releasePlan.planCount', { count: plans.length }) }}</small>
        </div>
        <span><ListChecks :size="16" /></span>
      </header>

      <div class="plan-index-list">
        <button
          v-for="plan in plans"
          :key="plan.id"
          type="button"
          :class="[{ active: selectedPlan?.id === plan.id }, `tone-${planStatus(visiblePlanStatus(plan)).tone}`]"
          @click="selectedPlanID = plan.id"
        >
          <i :class="{ live: planStatus(visiblePlanStatus(plan)).live }" />
          <span class="plan-index-copy">
            <strong :title="planTitle(plan)">{{ planTitle(plan) }}</strong>
            <span>{{ planStatus(visiblePlanStatus(plan)).label }} · {{ t('releasePlan.applicationCount', { count: applicationCount(plan) }) }}</span>
            <small>{{ formatTime(plan.updated_at || plan.created_at) }}</small>
          </span>
          <ChevronRight :size="16" />
        </button>
      </div>
    </aside>

    <main v-if="selectedPlan" class="plan-detail" :class="`tone-${planStatus(visiblePlanStatus(selectedPlan)).tone}`">
      <header class="plan-detail-heading">
        <div class="plan-identity">
          <span class="plan-mark"><ListChecks /></span>
          <div>
            <small>{{ t('releasePlan.eyebrow') }}</small>
            <h2>{{ planTitle(selectedPlan) }}</h2>
            <p><CalendarDays :size="14" />{{ t('releasePlan.createdAt', { time: formatTime(selectedPlan.created_at) }) }}</p>
          </div>
        </div>
        <div class="plan-detail-actions">
          <div class="plan-state" :class="{ live: planStatus(visiblePlanStatus(selectedPlan)).live }">
            <i />
            <span><small>{{ planStatus(visiblePlanStatus(selectedPlan)).hint }}</small><strong>{{ planStatus(visiblePlanStatus(selectedPlan)).label }}</strong></span>
          </div>
          <a-button v-if="canRun && canExecutePlan(selectedPlan)" type="primary" @click="emit('execute', selectedPlan.id)">
            <Play :size="14" />{{ t('releasePlan.execute') }}
          </a-button>
          <a-button v-if="canManage && !planMutationBlocked(selectedPlan)" :disabled="mutatingPlanID === selectedPlan.id" @click="emit('edit', selectedPlan.id)">
            <Pencil :size="14" />{{ t('releasePlan.actions.edit') }}
          </a-button>
          <a-button
            v-if="canManage"
            :loading="mutatingPlanID === selectedPlan.id"
            @click="emit('toggle', selectedPlan.id, selectedPlan.is_active === false)"
          >
            <Power v-if="selectedPlan.is_active === false" :size="14" />
            <PowerOff v-else :size="14" />
            {{ selectedPlan.is_active === false ? t('releasePlan.actions.enable') : t('releasePlan.actions.disable') }}
          </a-button>
          <a-button v-if="canManage && !planMutationBlocked(selectedPlan)" danger :disabled="mutatingPlanID === selectedPlan.id" @click="emit('remove', selectedPlan.id)">
            <Trash2 :size="14" />{{ t('releasePlan.actions.remove') }}
          </a-button>
        </div>
      </header>

      <dl class="plan-summary">
        <div>
          <dt><UsersRound />{{ t('releasePlan.summaryApplications') }}</dt>
          <dd>{{ applicationCount(selectedPlan) }}</dd>
          <small>{{ t('releasePlan.summaryApplicationsHint') }}</small>
        </div>
        <div>
          <dt><Layers3 />{{ t('releasePlan.summaryGroups') }}</dt>
          <dd>{{ selectedPlan.groups?.length || 0 }}</dd>
          <small>{{ t('releasePlan.summaryGroupsHint') }}</small>
        </div>
        <div>
          <dt><GitBranch />{{ t('releasePlan.summaryManual') }}</dt>
          <dd>{{ manualApplicationCount(selectedPlan) }}</dd>
          <small>{{ t('releasePlan.summaryManualHint') }}</small>
        </div>
      </dl>

      <section class="plan-groups">
        <header>
          <div><small>{{ t('releasePlan.orchestration') }}</small><h3>{{ t('releasePlan.groupsTitle') }}</h3></div>
          <div class="plan-group-heading-actions">
            <span>{{ t('releasePlan.groupCount', { count: selectedPlan.groups?.length || 0 }) }}</span>
            <a-button
              v-if="canManage && !planMutationBlocked(selectedPlan)"
              size="small"
              type="dashed"
              :disabled="mutatingPlanID === selectedPlan.id"
              @click="emit('edit', selectedPlan.id, undefined, true)"
            >
              <Plus :size="13" />{{ t('releasePlan.editor.addGroup') }}
            </a-button>
          </div>
        </header>

        <div v-if="selectedPlan.groups?.length" class="plan-group-list">
          <article v-for="(group, index) in selectedPlan.groups" :key="group.id" class="plan-group-row">
            <span class="plan-group-step">{{ index + 1 }}</span>
            <header>
              <div><strong>{{ group.name }}</strong><small>{{ dependencyLabel(selectedPlan, group) }}</small></div>
              <div class="plan-group-actions">
                <div class="plan-group-rules"><span>{{ groupMode(group.mode) }}</span><span>{{ failurePolicy(group.failure_policy) }}</span></div>
                <a-button v-if="canManage && !planMutationBlocked(selectedPlan)" size="small" type="text" :disabled="mutatingPlanID === selectedPlan.id" @click="emit('edit', selectedPlan.id, group.id)">
                  <Pencil :size="13" />{{ t('releasePlan.actions.edit') }}
                </a-button>
                <a-button v-if="canManage && !planMutationBlocked(selectedPlan)" size="small" type="text" danger :disabled="mutatingPlanID === selectedPlan.id" @click="emit('removeGroup', selectedPlan.id, group.id)">
                  <Trash2 :size="13" />{{ t('releasePlan.actions.remove') }}
                </a-button>
              </div>
            </header>

            <div v-if="group.applications?.length" class="plan-application-grid">
              <div v-for="item in group.applications" :key="item.application_id" class="plan-application">
                <span class="plan-app-mark"><Boxes /></span>
                <div class="plan-app-copy">
                  <strong :title="applicationName(item)">{{ applicationName(item) }}</strong>
                  <span :title="item.source_value">
                    <component :is="sourceMeta(item).icon" />
                    {{ sourceMeta(item).label }} · {{ sourceMeta(item).value }}
                  </span>
                </div>
                <em :class="{ disabled: !applicationEnabled(item) }">{{ applicationEnabled(item) ? t('releasePlan.enabled') : t('releasePlan.disabled') }}</em>
              </div>
            </div>
            <a-empty v-else :image="AntEmpty.PRESENTED_IMAGE_SIMPLE" :description="t('releasePlan.emptyGroup')" />
          </article>
        </div>
        <a-empty v-else :image="AntEmpty.PRESENTED_IMAGE_SIMPLE" :description="t('releasePlan.emptyGroups')" />
      </section>
    </main>
  </div>

  <div v-else class="plan-empty vben-card">
    <span><ListChecks /></span>
    <h3>{{ t('releasePlan.emptyTitle') }}</h3>
    <p>{{ t('releasePlan.emptyDescription') }}</p>
    <a-button v-if="canManage" type="primary" @click="emit('create')">{{ t('releasePlan.create') }}</a-button>
  </div>
</template>

<style scoped>
.plan-loading{min-height:460px;padding:24px}.plan-workspace{display:grid;min-height:clamp(460px,calc(100vh - 224px),620px);grid-template-columns:290px minmax(0,1fr);gap:8px;overflow:hidden;padding:8px;background:var(--zrt-surface-soft)}
.plan-index,.plan-detail{min-width:0;border-radius:11px;background:var(--zrt-surface)}.plan-index{overflow:hidden}.plan-index>header{display:flex;min-height:66px;align-items:center;justify-content:space-between;padding:11px 14px;border-bottom:1px solid var(--zrt-border)}.plan-index>header strong,.plan-index>header small{display:block}.plan-index>header small{margin-top:2px;color:var(--zrt-muted);font-size:11px}.plan-index>header>span{display:grid;width:32px;height:32px;place-items:center;border-radius:9px;color:var(--zrt-primary);background:var(--zrt-primary-soft)}
.plan-index-list{max-height:calc(100vh - 250px);overflow-y:auto;padding:7px;scrollbar-width:thin}.plan-index-list>button{--plan-tone:#9ba1ad;display:grid;width:100%;min-height:88px;align-items:center;grid-template-columns:10px minmax(0,1fr) 17px;gap:10px;margin:3px 0;padding:10px 9px;border:0;border-radius:11px;outline:0;color:var(--zrt-text);background:transparent;cursor:pointer;text-align:left;transition:background-color 160ms ease,box-shadow 160ms ease}.plan-index-list>button.tone-info{--plan-tone:#4f7df3}.plan-index-list>button.tone-success{--plan-tone:#2ab573}.plan-index-list>button.tone-danger{--plan-tone:#ed5965}.plan-index-list>button:hover{background:var(--zrt-surface-soft)}.plan-index-list>button:focus-visible{box-shadow:inset 0 0 0 2px color-mix(in srgb,var(--zrt-primary) 45%,transparent)}.plan-index-list>button.active{background:var(--zrt-primary-soft);box-shadow:inset 3px 0 var(--zrt-primary)}.plan-index-list>button>i{width:8px;height:8px;border-radius:50%;background:var(--plan-tone);box-shadow:0 0 0 4px color-mix(in srgb,var(--plan-tone) 10%,transparent)}.plan-index-list>button>i.live{animation:plan-pulse 2s ease-out infinite}.plan-index-list>button>svg{color:var(--zrt-muted)}
.plan-index-copy{min-width:0}.plan-index-copy strong,.plan-index-copy span,.plan-index-copy small{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.plan-index-copy strong{font-size:13px}.plan-index-copy span{margin-top:4px;color:var(--zrt-text);font-size:11px}.plan-index-copy small{margin-top:3px;color:var(--zrt-muted);font-size:10px}
.plan-detail{--plan-tone:#9ba1ad;padding:20px}.plan-detail.tone-info{--plan-tone:#4f7df3}.plan-detail.tone-success{--plan-tone:#2ab573}.plan-detail.tone-danger{--plan-tone:#ed5965}.plan-detail-heading{display:flex;align-items:flex-start;justify-content:space-between;gap:20px;padding-bottom:18px;border-bottom:1px solid var(--zrt-border)}.plan-identity{display:flex;min-width:0;align-items:flex-start;gap:13px}.plan-mark{display:grid;width:44px;height:44px;flex:0 0 44px;place-items:center;border-radius:13px;color:var(--plan-tone);background:color-mix(in srgb,var(--plan-tone) 10%,var(--zrt-surface))}.plan-mark svg{width:21px}.plan-identity>div{min-width:0}.plan-identity small{color:var(--zrt-primary);font-size:11px;font-weight:600;letter-spacing:.08em}.plan-identity h2{overflow:hidden;margin:2px 0 0;text-overflow:ellipsis;font-size:20px;line-height:1.35}.plan-identity p{display:flex;align-items:center;gap:5px;margin:6px 0 0;color:var(--zrt-muted);font-size:11px}
.plan-detail-actions{display:flex;flex:0 0 auto;align-items:center;gap:8px}.plan-detail-actions :deep(.ant-btn){display:inline-flex;align-items:center;gap:5px}.plan-state{display:flex;flex:0 0 auto;align-items:center;gap:9px;padding:8px 11px;border-radius:10px;color:var(--plan-tone);background:color-mix(in srgb,var(--plan-tone) 9%,var(--zrt-surface))}.plan-state>i{width:8px;height:8px;flex:0 0 8px;border-radius:50%;background:currentColor;box-shadow:0 0 0 4px color-mix(in srgb,currentColor 10%,transparent)}.plan-state.live>i{animation:plan-pulse 2s ease-out infinite}.plan-state small,.plan-state strong{display:block}.plan-state small{color:var(--zrt-muted);font-size:10px;font-weight:400}.plan-state strong{font-size:13px}
.plan-summary{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:9px;margin:16px 0}.plan-summary>div{min-width:0;padding:12px 13px;border:1px solid var(--zrt-border);border-radius:11px;background:var(--zrt-surface-soft)}.plan-summary dt{display:flex;align-items:center;gap:6px;color:var(--zrt-muted);font-size:11px}.plan-summary dt svg{width:14px;color:var(--zrt-primary)}.plan-summary dd{margin:4px 0 0;font-size:21px;font-weight:650;line-height:1.2}.plan-summary small{display:block;margin-top:3px;color:var(--zrt-muted);font-size:10px}
.plan-groups{overflow:hidden;border:1px solid var(--zrt-border);border-radius:12px}.plan-groups>header{display:flex;align-items:center;justify-content:space-between;padding:13px 15px;background:var(--zrt-surface-soft)}.plan-groups>header small,.plan-groups>header h3{display:block;margin:0}.plan-groups>header small{color:var(--zrt-muted);font-size:10px}.plan-groups>header h3{margin-top:1px;font-size:14px}.plan-group-heading-actions{display:flex;align-items:center;gap:7px}.plan-group-heading-actions>span{padding:4px 8px;border-radius:999px;color:var(--zrt-muted);background:var(--zrt-surface);font-size:10px}.plan-group-heading-actions :deep(.ant-btn),.plan-group-actions :deep(.ant-btn){display:inline-flex;align-items:center;gap:4px}
.plan-group-list{padding:0 15px 4px}.plan-group-row{position:relative;padding:15px 0 15px 39px}.plan-group-row+.plan-group-row{border-top:1px solid var(--zrt-border)}.plan-group-step{position:absolute;top:16px;left:0;display:grid;width:26px;height:26px;place-items:center;border-radius:9px;color:var(--zrt-primary);background:var(--zrt-primary-soft);font-size:11px;font-weight:700}.plan-group-row>header{display:flex;align-items:flex-start;justify-content:space-between;gap:12px}.plan-group-row>header strong,.plan-group-row>header small{display:block}.plan-group-row>header strong{font-size:13px}.plan-group-row>header small{margin-top:3px;color:var(--zrt-muted);font-size:10px}.plan-group-actions{display:flex;flex-wrap:wrap;align-items:center;justify-content:flex-end;gap:3px}.plan-group-rules{display:flex;flex-wrap:wrap;justify-content:flex-end;gap:5px}.plan-group-rules span{padding:3px 7px;border-radius:999px;color:var(--zrt-muted);background:var(--zrt-surface-soft);font-size:10px;white-space:nowrap}
.plan-application-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(280px,100%),1fr));gap:7px;margin-top:11px}.plan-application{display:grid;min-width:0;align-items:center;grid-template-columns:34px minmax(0,1fr) auto;gap:9px;padding:9px 10px;border:1px solid var(--zrt-border);border-radius:10px;background:var(--zrt-surface-soft)}.plan-app-mark{display:grid;width:34px;height:34px;place-items:center;border-radius:9px;color:var(--zrt-primary);background:var(--zrt-surface)}.plan-app-mark svg{width:16px}.plan-app-copy{min-width:0}.plan-app-copy strong,.plan-app-copy span{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.plan-app-copy strong{font-size:12px}.plan-app-copy span{display:flex;align-items:center;gap:4px;margin-top:3px;color:var(--zrt-muted);font-size:10px}.plan-app-copy span svg{width:12px;flex:0 0 12px}.plan-application em{padding:3px 6px;border-radius:999px;color:#168b57;background:color-mix(in srgb,#2ab573 10%,var(--zrt-surface));font-size:9px;font-style:normal;white-space:nowrap}.plan-application em.disabled{color:var(--zrt-muted);background:var(--zrt-surface)}
.plan-empty{display:grid;min-height:460px;place-items:center;align-content:center;padding:32px;text-align:center}.plan-empty>span{display:grid;width:58px;height:58px;place-items:center;border-radius:18px;color:var(--zrt-primary);background:var(--zrt-primary-soft)}.plan-empty>span svg{width:25px}.plan-empty h3{margin:15px 0 0;font-size:16px}.plan-empty p{max-width:420px;margin:6px 0 16px;color:var(--zrt-muted);font-size:12px}
@keyframes plan-pulse{0%{box-shadow:0 0 0 0 color-mix(in srgb,currentColor 30%,transparent)}70%{box-shadow:0 0 0 7px transparent}100%{box-shadow:0 0 0 0 transparent}}
@media(max-width:980px){.plan-workspace{grid-template-columns:250px minmax(0,1fr)}.plan-application-grid{grid-template-columns:1fr}}
@media(max-width:760px){.plan-workspace{grid-template-columns:1fr;min-height:0}.plan-index-list{display:flex;max-height:none;overflow-x:auto;overflow-y:hidden;padding:7px}.plan-index-list>button{width:260px;flex:0 0 260px}.plan-detail{padding:16px}.plan-detail-heading{align-items:flex-start}.plan-summary{grid-template-columns:repeat(3,minmax(100px,1fr));overflow-x:auto}.plan-group-row>header{flex-direction:column}.plan-group-rules{justify-content:flex-start}}
@media(max-width:520px){.plan-detail-heading{flex-direction:column}.plan-detail-actions{width:100%;align-items:stretch;flex-direction:column}.plan-state{align-self:stretch}.plan-detail-actions :deep(.ant-btn){justify-content:center}.plan-summary{grid-template-columns:1fr}.plan-application{grid-template-columns:34px minmax(0,1fr)}.plan-application em{grid-column:2;justify-self:start}.plan-group-row{padding-left:34px}}
@media(prefers-reduced-motion:reduce){.plan-index-list>button>i.live,.plan-state.live>i{animation:none}}
</style>
