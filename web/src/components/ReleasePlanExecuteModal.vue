<script setup lang="ts">
import { computed, type Component } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  AlertTriangle,
  Boxes,
  CheckCircle2,
  CircleDashed,
  GitBranch,
  Layers3,
  LoaderCircle,
  RefreshCw,
  ShieldAlert,
} from 'lucide-vue-next'
import { formatGitReference } from '@/utils/gitReference'

type LoadState = 'idle' | 'loading' | 'ready' | 'blocked' | 'error'
type ReferenceKind = 'branch' | 'tag'

interface ReleasePath {
  id: string
  name: string
  environment?: string
}

interface ReferenceOption {
  kind: ReferenceKind
  ref: string
  name: string
  sha: string
}

interface ReleasePlanExecutionItem {
  membershipID: string
  applicationID: string
  applicationName: string
  workflowRevision: number
  loadState: LoadState
  reason?: string
  staticBlocked?: boolean
  sources: ReleasePath[]
  refs: ReferenceOption[]
  selectedSourceID: string
  selectedRef: string
}

interface ReleasePlanExecutionGroup {
  id: string
  name: string
  mode: string
  failurePolicy: string
  dependencies: string[]
  items: ReleasePlanExecutionItem[]
}

interface ItemStatus {
  key: LoadState | 'pending' | 'complete'
  label: string
  icon: Component
}

const props = defineProps<{
  open: boolean
  planTitle: string
  groups: ReleasePlanExecutionGroup[]
  submitting: boolean
}>()

const emit = defineEmits<{
  cancel: []
  submit: []
  retry: [applicationID: string]
  'update-source': [membershipID: string, value: string]
  'update-ref': [membershipID: string, value: string]
}>()

const { t } = useI18n()

const items = computed(() => props.groups.flatMap((group) => group.items))
const completeCount = computed(() => items.value.filter(isItemComplete).length)
const blockedCount = computed(() => items.value.filter((item) => item.loadState === 'blocked' || item.loadState === 'error').length)
const pendingCount = computed(() => items.value.length - completeCount.value - blockedCount.value)
const canSubmit = computed(() => items.value.length > 0 && items.value.every(isItemComplete))

const validationMeta = computed(() => {
  if (!items.value.length) {
    return {
      key: 'blocked',
      icon: ShieldAlert,
      title: t('releasePlanExecution.validation.emptyTitle'),
      description: t('releasePlanExecution.validation.emptyDescription'),
    }
  }
  if (blockedCount.value) {
    return {
      key: 'blocked',
      icon: ShieldAlert,
      title: t('releasePlanExecution.validation.blockedTitle', { count: blockedCount.value }),
      description: t('releasePlanExecution.validation.blockedDescription'),
    }
  }
  if (!canSubmit.value) {
    return {
      key: 'pending',
      icon: CircleDashed,
      title: t('releasePlanExecution.validation.pendingTitle', { count: pendingCount.value }),
      description: t('releasePlanExecution.validation.pendingDescription'),
    }
  }
  return {
    key: 'ready',
    icon: CheckCircle2,
    title: t('releasePlanExecution.validation.readyTitle', { count: completeCount.value }),
    description: t('releasePlanExecution.validation.readyDescription'),
  }
})

function isItemComplete(item: ReleasePlanExecutionItem) {
  return item.loadState === 'ready' && Boolean(item.selectedSourceID) && Boolean(item.selectedRef)
}

function itemStatus(item: ReleasePlanExecutionItem): ItemStatus {
  if (item.loadState === 'loading') return { key: 'loading', label: t('releasePlanExecution.status.loading'), icon: LoaderCircle }
  if (item.loadState === 'error') return { key: 'error', label: t('releasePlanExecution.status.error'), icon: AlertTriangle }
  if (item.loadState === 'blocked') return { key: 'blocked', label: t('releasePlanExecution.status.blocked'), icon: ShieldAlert }
  if (item.loadState === 'idle') return { key: 'idle', label: t('releasePlanExecution.status.idle'), icon: CircleDashed }
  if (!item.selectedSourceID || !item.selectedRef) return { key: 'pending', label: t('releasePlanExecution.status.pending'), icon: CircleDashed }
  return { key: 'complete', label: t('releasePlanExecution.status.ready'), icon: CheckCircle2 }
}

function groupMode(mode: string) {
  return t(mode === 'sequential' ? 'releasePlanExecution.group.sequential' : 'releasePlanExecution.group.parallel')
}

function failurePolicy(policy: string) {
  return t(policy === 'continue' ? 'releasePlanExecution.group.continueOnFailure' : 'releasePlanExecution.group.stopOnFailure')
}

function dependencyLabel(dependencies: string[]) {
  return dependencies.length
    ? t('releasePlanExecution.group.dependencies', { names: dependencies.join(' · ') })
    : ''
}

function references(item: ReleasePlanExecutionItem, kind: ReferenceKind) {
  return item.refs.filter((reference) => reference.kind === kind)
}

function referenceLabel(reference: ReferenceOption) {
  return formatGitReference(reference)
}

function sourceLabel(source: ReleasePath) {
  return source.environment ? `${source.name} · ${source.environment}` : source.name
}

function updateSource(membershipID: string, value: unknown) {
  emit('update-source', membershipID, typeof value === 'string' ? value : '')
}

function updateRef(membershipID: string, value: unknown) {
  emit('update-ref', membershipID, typeof value === 'string' ? value : '')
}
</script>

<template>
  <a-modal
    :open="open"
    :width="960"
    wrap-class-name="release-plan-execute-modal"
    :title="t('releasePlanExecution.title')"
    :closable="!submitting"
    :keyboard="!submitting"
    :mask-closable="!submitting"
    :confirm-loading="submitting"
    :ok-button-props="{ disabled: !canSubmit || submitting }"
    :cancel-button-props="{ disabled: submitting }"
    :ok-text="t('releasePlanExecution.submit')"
    :cancel-text="t('releasePlanExecution.cancel')"
    @ok="emit('submit')"
    @cancel="emit('cancel')"
  >
    <div class="execution-shell" :aria-busy="submitting">
      <section class="execution-overview">
        <span class="overview-mark"><Layers3 :size="22" /></span>
        <div>
          <small>{{ t('releasePlanExecution.eyebrow') }}</small>
          <strong :title="planTitle">{{ planTitle || t('releasePlanExecution.untitled') }}</strong>
          <p>{{ t('releasePlanExecution.description') }}</p>
        </div>
      </section>

      <dl class="execution-summary">
        <div>
          <dt>{{ t('releasePlanExecution.summary.total') }}</dt>
          <dd>{{ items.length }}</dd>
        </div>
        <div class="ready">
          <dt>{{ t('releasePlanExecution.summary.ready') }}</dt>
          <dd>{{ completeCount }}</dd>
        </div>
        <div class="pending">
          <dt>{{ t('releasePlanExecution.summary.pending') }}</dt>
          <dd>{{ pendingCount }}</dd>
        </div>
        <div class="blocked">
          <dt>{{ t('releasePlanExecution.summary.blocked') }}</dt>
          <dd>{{ blockedCount }}</dd>
        </div>
      </dl>

      <section class="validation-banner" :class="validationMeta.key" role="status">
        <span><component :is="validationMeta.icon" :size="17" /></span>
        <div>
          <strong>{{ validationMeta.title }}</strong>
          <small>{{ validationMeta.description }}</small>
        </div>
      </section>

      <div v-if="groups.length" class="execution-groups">
        <article v-for="(group, groupIndex) in groups" :key="group.id" class="execution-group">
          <header class="group-heading">
            <span class="group-step">{{ groupIndex + 1 }}</span>
            <div class="group-copy">
              <strong>{{ group.name }}</strong>
              <small v-if="dependencyLabel(group.dependencies)">{{ dependencyLabel(group.dependencies) }}</small>
            </div>
            <div class="group-rules">
              <span>{{ groupMode(group.mode) }}</span>
              <span>{{ failurePolicy(group.failurePolicy) }}</span>
              <span>{{ t('releasePlanExecution.group.itemCount', { count: group.items.length }) }}</span>
            </div>
          </header>

          <div v-if="group.items.length" class="execution-items">
            <section
              v-for="item in group.items"
              :key="`${group.id}:${item.membershipID}`"
              class="execution-item"
              :class="`state-${itemStatus(item).key}`"
            >
              <div class="item-grid">
                <div class="item-identity">
                  <span class="application-mark"><Boxes :size="17" /></span>
                  <div>
                    <strong :title="item.applicationName">{{ item.applicationName }}</strong>
                    <small>{{ t('releasePlanExecution.application.workflowRevision', { revision: item.workflowRevision }) }}</small>
                  </div>
                </div>

                <div class="item-field" :class="{ incomplete: item.loadState === 'ready' && !item.selectedSourceID }">
                  <label :for="`execution-source-${item.membershipID}`">{{ t('releasePlanExecution.field.source') }}</label>
                  <a-select
                    :id="`execution-source-${item.membershipID}`"
                    :value="item.selectedSourceID || undefined"
                    :disabled="item.loadState !== 'ready' || submitting"
                    allow-clear
                    show-search
                    option-filter-prop="label"
                    :placeholder="t('releasePlanExecution.field.sourcePlaceholder')"
                    :not-found-content="t('releasePlanExecution.field.noSources')"
                    @update:value="updateSource(item.membershipID, $event)"
                  >
                    <a-select-option
                      v-for="source in item.sources"
                      :key="source.id"
                      :value="source.id"
                      :label="sourceLabel(source)"
                    >
                      {{ sourceLabel(source) }}
                    </a-select-option>
                  </a-select>
                  <small v-if="item.loadState === 'ready' && !item.selectedSourceID" class="field-message">
                    {{ t('releasePlanExecution.validation.sourceRequired') }}
                  </small>
                </div>

                <div class="item-field" :class="{ incomplete: item.loadState === 'ready' && !item.selectedRef }">
                  <label :for="`execution-ref-${item.membershipID}`">{{ t('releasePlanExecution.field.reference') }}</label>
                  <a-select
                    :id="`execution-ref-${item.membershipID}`"
                    :value="item.selectedRef || undefined"
                    :disabled="item.loadState !== 'ready' || submitting"
                    allow-clear
                    show-search
                    option-filter-prop="label"
                    :placeholder="t('releasePlanExecution.field.referencePlaceholder')"
                    :not-found-content="t('releasePlanExecution.field.noReferences')"
                    @update:value="updateRef(item.membershipID, $event)"
                  >
                    <a-select-opt-group v-if="references(item, 'branch').length" :label="t('releasePlanExecution.reference.branches')">
                      <a-select-option
                        v-for="reference in references(item, 'branch')"
                        :key="reference.ref"
                        :value="reference.ref"
                        :label="referenceLabel(reference)"
                      >
                        <span class="reference-option"><GitBranch :size="13" />{{ referenceLabel(reference) }}</span>
                      </a-select-option>
                    </a-select-opt-group>
                    <a-select-opt-group v-if="references(item, 'tag').length" :label="t('releasePlanExecution.reference.tags')">
                      <a-select-option
                        v-for="reference in references(item, 'tag')"
                        :key="reference.ref"
                        :value="reference.ref"
                        :label="referenceLabel(reference)"
                      >
                        {{ referenceLabel(reference) }}
                      </a-select-option>
                    </a-select-opt-group>
                  </a-select>
                  <small v-if="item.loadState === 'ready' && !item.selectedRef" class="field-message">
                    {{ t('releasePlanExecution.validation.referenceRequired') }}
                  </small>
                </div>

                <div class="item-state">
                  <span :class="itemStatus(item).key">
                    <component :is="itemStatus(item).icon" :size="14" />
                    {{ itemStatus(item).label }}
                  </span>
                  <a-button
                    v-if="item.loadState === 'error' || (item.loadState === 'blocked' && !item.staticBlocked)"
                    type="text"
                    size="small"
                    :disabled="submitting"
                    @click="emit('retry', item.applicationID)"
                  >
                    <RefreshCw :size="13" />{{ t('releasePlanExecution.retry') }}
                  </a-button>
                </div>
              </div>

              <div v-if="item.reason" class="item-reason" role="alert">
                <AlertTriangle :size="14" />
                <span>{{ item.reason }}</span>
              </div>
            </section>
          </div>

          <div v-else class="group-empty">
            <Boxes :size="18" />
            <span>{{ t('releasePlanExecution.group.empty') }}</span>
          </div>
        </article>
      </div>

      <div v-else class="execution-empty">
        <ShieldAlert :size="24" />
        <strong>{{ t('releasePlanExecution.empty.title') }}</strong>
        <span>{{ t('releasePlanExecution.empty.description') }}</span>
      </div>
    </div>
  </a-modal>
</template>

<style scoped>
.execution-shell{color:var(--zrt-text)}
.execution-overview{display:flex;align-items:flex-start;gap:13px;padding:2px 0 16px}.overview-mark{display:grid;width:44px;height:44px;flex:0 0 44px;place-items:center;border-radius:13px;color:var(--zrt-primary);background:var(--zrt-primary-soft)}.execution-overview>div{min-width:0}.execution-overview small,.execution-overview strong,.execution-overview p{display:block}.execution-overview small{color:var(--zrt-primary);font-size:10px;font-weight:650;letter-spacing:.08em}.execution-overview strong{overflow:hidden;margin-top:2px;text-overflow:ellipsis;white-space:nowrap;font-size:18px}.execution-overview p{margin:4px 0 0;color:var(--zrt-muted);font-size:12px;line-height:1.5}
.execution-summary{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:8px;margin:0 0 12px}.execution-summary>div{--summary-color:var(--zrt-primary);min-width:0;padding:10px 12px;border:1px solid var(--zrt-border);border-radius:10px;background:var(--zrt-surface-soft)}.execution-summary>div.ready{--summary-color:#24a86c}.execution-summary>div.pending{--summary-color:#d28b20}.execution-summary>div.blocked{--summary-color:#df5260}.execution-summary dt{color:var(--zrt-muted);font-size:10px}.execution-summary dd{margin:2px 0 0;color:var(--summary-color);font-size:20px;font-weight:700;line-height:1.2}
.validation-banner{--validation-color:var(--zrt-primary);display:flex;align-items:flex-start;gap:10px;margin-bottom:13px;padding:10px 12px;border:1px solid color-mix(in srgb,var(--validation-color) 24%,var(--zrt-border));border-radius:10px;background:color-mix(in srgb,var(--validation-color) 7%,var(--zrt-surface))}.validation-banner.pending{--validation-color:#d28b20}.validation-banner.blocked{--validation-color:#df5260}.validation-banner.ready{--validation-color:#24a86c}.validation-banner>span{display:grid;width:28px;height:28px;flex:0 0 28px;place-items:center;border-radius:8px;color:var(--validation-color);background:color-mix(in srgb,var(--validation-color) 11%,var(--zrt-surface))}.validation-banner strong,.validation-banner small{display:block}.validation-banner strong{font-size:12px}.validation-banner small{margin-top:2px;color:var(--zrt-muted);font-size:10px;line-height:1.45}
.execution-groups{display:grid;gap:11px}.execution-group{overflow:hidden;border:1px solid var(--zrt-border);border-radius:12px;background:var(--zrt-surface)}.group-heading{display:grid;min-height:58px;align-items:center;grid-template-columns:30px minmax(0,1fr) auto;gap:10px;padding:10px 12px;border-bottom:1px solid var(--zrt-border);background:var(--zrt-surface-soft)}.group-step{display:grid;width:28px;height:28px;place-items:center;border-radius:9px;color:var(--zrt-primary);background:var(--zrt-primary-soft);font-size:11px;font-weight:700}.group-copy{min-width:0}.group-copy strong,.group-copy small{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.group-copy strong{font-size:13px}.group-copy small{margin-top:2px;color:var(--zrt-muted);font-size:10px}.group-rules{display:flex;flex-wrap:wrap;justify-content:flex-end;gap:5px}.group-rules span{padding:3px 7px;border-radius:999px;color:var(--zrt-muted);background:var(--zrt-surface);font-size:10px;white-space:nowrap}
.execution-items{display:grid;gap:7px;padding:8px}.execution-item{--item-state:var(--zrt-primary);overflow:hidden;border:1px solid var(--zrt-border);border-radius:10px;background:var(--zrt-surface);transition:border-color 160ms ease,background-color 160ms ease}.execution-item.state-complete{--item-state:#24a86c}.execution-item.state-pending,.execution-item.state-loading{--item-state:#d28b20}.execution-item.state-error,.execution-item.state-blocked{--item-state:#df5260}.execution-item:hover{border-color:color-mix(in srgb,var(--item-state) 34%,var(--zrt-border));background:color-mix(in srgb,var(--item-state) 2%,var(--zrt-surface))}.item-grid{display:grid;align-items:start;grid-template-columns:minmax(180px,.82fr) minmax(150px,.9fr) minmax(220px,1.12fr) auto;gap:11px;padding:11px}.item-identity{display:flex;min-width:0;align-items:center;gap:9px;padding-top:18px}.application-mark{display:grid;width:34px;height:34px;flex:0 0 34px;place-items:center;border-radius:9px;color:var(--item-state);background:color-mix(in srgb,var(--item-state) 9%,var(--zrt-surface-soft))}.item-identity>div{min-width:0}.item-identity strong,.item-identity small{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.item-identity strong{font-size:12px}.item-identity small{margin-top:3px;color:var(--zrt-muted);font-size:10px}
.item-field{min-width:0}.item-field label{display:block;margin-bottom:5px;color:var(--zrt-muted);font-size:10px;font-weight:600}.item-field :deep(.ant-select){width:100%}.item-field.incomplete :deep(.ant-select-selector){border-color:color-mix(in srgb,#d28b20 58%,var(--zrt-border))!important}.field-message{display:block;margin-top:4px;color:#b87715;font-size:9px}.reference-option{display:flex;align-items:center;gap:6px}.reference-option svg{flex:0 0 auto;color:var(--zrt-muted)}
.item-state{display:flex;min-width:96px;align-items:flex-end;flex-direction:column;gap:3px;padding-top:18px}.item-state>span{display:flex;align-items:center;gap:5px;padding:4px 7px;border-radius:999px;color:var(--item-state);background:color-mix(in srgb,var(--item-state) 9%,var(--zrt-surface-soft));font-size:10px;font-weight:600;white-space:nowrap}.item-state>span.loading svg{animation:execution-spin 1.2s linear infinite}.item-state :deep(.ant-btn){display:inline-flex;align-items:center;gap:4px;padding-inline:5px;color:var(--zrt-primary);font-size:10px}.item-reason{display:flex;align-items:flex-start;gap:6px;padding:7px 11px;border-top:1px solid color-mix(in srgb,#df5260 20%,var(--zrt-border));color:#c94552;background:color-mix(in srgb,#df5260 5%,var(--zrt-surface));font-size:10px;line-height:1.45}.item-reason svg{flex:0 0 auto;margin-top:1px}
.group-empty,.execution-empty{display:flex;align-items:center;justify-content:center;gap:7px;color:var(--zrt-muted);font-size:11px}.group-empty{min-height:76px}.execution-empty{min-height:210px;flex-direction:column;padding:28px;text-align:center}.execution-empty svg{color:var(--zrt-primary)}.execution-empty strong{color:var(--zrt-text);font-size:14px}.execution-empty span{max-width:420px;font-size:11px}
@keyframes execution-spin{to{transform:rotate(360deg)}}
@media(max-width:820px){.item-grid{grid-template-columns:minmax(0,1fr) minmax(0,1fr)}.item-identity{padding-top:0}.item-state{align-items:flex-start;padding-top:0}.group-heading{grid-template-columns:30px minmax(0,1fr)}.group-rules{grid-column:2;justify-content:flex-start}}
@media(max-width:600px){.execution-summary{grid-template-columns:repeat(2,minmax(0,1fr))}.execution-overview p{font-size:11px}.item-grid{grid-template-columns:1fr}.item-state{flex-direction:row;align-items:center}.group-heading{align-items:start}.group-copy small{white-space:normal}.group-rules{grid-column:1/-1;justify-content:flex-start}.execution-items{padding:6px}.execution-item{border-radius:9px}}
@media(prefers-reduced-motion:reduce){.execution-item{transition:none}.item-state>span.loading svg{animation:none}}
:global(.release-plan-execute-modal .ant-modal){max-width:calc(100vw - 24px);padding-bottom:18px}:global(.release-plan-execute-modal .ant-modal-body){max-height:min(72vh,760px);overflow-y:auto;padding-right:3px;scrollbar-width:thin}
</style>
