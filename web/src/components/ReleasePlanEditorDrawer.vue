<script setup lang="ts">
import Sortable from 'sortablejs'
import { onBeforeUnmount, reactive, watch, type ComponentPublicInstance } from 'vue'
import { useI18n } from 'vue-i18n'
import { GripVertical, Plus, Trash2 } from 'lucide-vue-next'

interface ApplicationItem {
  id: string
  name: string
  is_active?: boolean
}

interface ReleaseGroupApplication {
  id: string
  application_id: string
  manual_deploy?: boolean
  source_type?: string
  source_value?: string
  sort_order?: number
}

interface ReleaseGroup {
  id: string
  name: string
  mode: string
  failure_policy: string
  sort_order?: number
  dependencies?: Array<{ depends_on_group_id: string }>
  applications?: ReleaseGroupApplication[]
}

interface ReleasePlan {
  id: string
  name?: string
  description?: string
  groups?: ReleaseGroup[]
}

interface EditableApplication {
  key: string
  id: string
  application_id: string
  manual_deploy: boolean
  source_type: string
  source_value: string
}

interface EditableGroup {
  key: string
  id: string
  name: string
  mode: 'parallel' | 'sequential'
  failure_policy: 'stop' | 'continue'
  depends_on_group_ids: string[]
  applications: EditableApplication[]
}

const props = defineProps<{
  open: boolean
  plan: ReleasePlan | null
  applications: ApplicationItem[]
  saving?: boolean
}>()

const emit = defineEmits<{
  'update:open': [open: boolean]
  save: [value: {
    id: string
    description: string
    groups: Array<{
      id: string
      name: string
      mode: 'parallel' | 'sequential'
      failure_policy: 'stop' | 'continue'
      depends_on_group_ids: string[]
      applications: Array<{
        application_id: string
        manual_deploy: boolean
        source_type: string
        source_value: string
      }>
    }>
  }]
}>()

const { t } = useI18n()
let localKey = 0
const draft = reactive({ id: '', description: '', groups: [] as EditableGroup[] })
const applicationSelection = reactive<Record<string, string[]>>({})
const listElements = new Map<string, HTMLElement>()
const sortables = new Map<string, Sortable>()

function nextKey(prefix: string) {
  localKey += 1
  return `${prefix}-${Date.now()}-${localKey}`
}

function initialize() {
  destroySortables()
  draft.id = props.plan?.id || ''
  const legacyName = props.plan?.name?.trim() || ''
  draft.description = props.plan?.description?.trim() || (legacyName && !/^发布计划-[0-9a-f]{8}$/i.test(legacyName) ? legacyName : '')
  const groups: ReleaseGroup[] = props.plan?.groups?.length
    ? [props.plan.groups[0]]
    : [{ id: '', name: '应用列表', mode: 'parallel', failure_policy: 'stop', sort_order: 0, applications: [], dependencies: [] }]
  draft.groups = groups
    .map((group) => ({
      key: group.id || nextKey('group'),
      id: group.id,
      name: '应用列表',
      mode: group.mode === 'sequential' ? 'sequential' : 'parallel',
      failure_policy: group.failure_policy === 'continue' ? 'continue' : 'stop',
      depends_on_group_ids: [],
      applications: [...(group.applications || [])]
        .sort((left, right) => (left.sort_order || 0) - (right.sort_order || 0))
        .map((item) => ({
          key: item.id,
          id: item.id,
          application_id: item.application_id,
          manual_deploy: Boolean(item.manual_deploy),
          source_type: item.source_type || '',
          source_value: item.source_value || '',
        })),
    }))
  Object.keys(applicationSelection).forEach((key) => delete applicationSelection[key])
}

function applicationName(applicationID: string) {
  return props.applications.find((item) => item.id === applicationID)?.name || t('releasePlan.unknownApplication')
}

function availableApplications(group: EditableGroup) {
  const ownIDs = new Set(group.applications.map((item) => item.application_id))
  return props.applications
    .filter((item) => item.is_active !== false)
    .map((item) => ({ value: item.id, label: item.name, disabled: ownIDs.has(item.id) }))
}

function addApplication(group: EditableGroup) {
  const applicationIDs = applicationSelection[group.key] || []
  const usedApplicationIDs = new Set(group.applications.map((item) => item.application_id))
  applicationIDs.forEach((applicationID) => {
    if (usedApplicationIDs.has(applicationID)) return
    group.applications.push({
      key: nextKey('application'),
      id: '',
      application_id: applicationID,
      manual_deploy: false,
      source_type: '',
      source_value: '',
    })
  })
  applicationSelection[group.key] = []
}

function removeApplication(group: EditableGroup, index: number) {
  group.applications.splice(index, 1)
}

function setApplicationListRef(groupKey: string, target: Element | ComponentPublicInstance | null) {
  const element = target instanceof HTMLElement
    ? target
    : target && '$el' in target && target.$el instanceof HTMLElement
      ? target.$el
      : null
  const previous = listElements.get(groupKey)
  if (previous === element) return
  sortables.get(groupKey)?.destroy()
  sortables.delete(groupKey)
  listElements.delete(groupKey)
  if (!element) return
  listElements.set(groupKey, element)
  sortables.set(groupKey, Sortable.create(element, {
    animation: 160,
    disabled: Boolean(props.saving),
    handle: '.plan-app-drag',
    draggable: '.plan-editor-application',
    ghostClass: 'plan-app-ghost',
    chosenClass: 'plan-app-chosen',
    onEnd(event) {
      if (event.oldIndex == null || event.newIndex == null || event.oldIndex === event.newIndex) return
      const group = draft.groups.find((item) => item.key === groupKey)
      if (!group) return
      const [moved] = group.applications.splice(event.oldIndex, 1)
      if (moved) group.applications.splice(event.newIndex, 0, moved)
    },
  }))
}

function destroySortables() {
  sortables.forEach((sortable) => sortable.destroy())
  sortables.clear()
  listElements.clear()
}

function submit() {
  const description = draft.description.trim()
  if (!description) return
  if (draft.groups.length !== 1 || !draft.groups[0].applications.length) return
  emit('save', {
    id: draft.id,
    description,
    groups: draft.groups.map((group) => ({
      id: group.id,
      name: '应用列表',
      mode: group.mode,
      failure_policy: group.failure_policy,
      depends_on_group_ids: [],
      applications: group.applications.map((item) => ({
        application_id: item.application_id,
        manual_deploy: item.manual_deploy,
        source_type: item.source_type,
        source_value: item.source_value,
      })),
    })),
  })
}

watch(() => props.open, (open) => {
  if (open) initialize()
  else destroySortables()
})
watch(() => props.plan?.id, () => {
  if (props.open) initialize()
})
watch(() => props.saving, (saving) => {
  sortables.forEach((sortable) => sortable.option('disabled', Boolean(saving)))
})
onBeforeUnmount(destroySortables)
</script>

<template>
  <a-drawer
    :open="open"
    :title="t(draft.id ? 'releasePlan.editor.title' : 'releasePlan.editor.createTitle')"
    width="min(820px, 100vw)"
    :mask-closable="!saving"
    :closable="!saving"
    class="release-plan-editor"
    @close="emit('update:open', false)"
  >
    <a-form layout="vertical" :disabled="saving" @submit.prevent="submit">
      <a-form-item :label="t('releasePlan.editor.description')" required>
        <a-textarea v-model:value="draft.description" :rows="3" :maxlength="500" show-count />
      </a-form-item>

      <section class="plan-editor-groups">
        <header>
          <div>
            <strong>{{ t('releasePlan.editor.applications') }}</strong>
            <small>{{ t('releasePlan.editor.applicationsHint') }}</small>
          </div>
        </header>

        <article
          v-for="group in draft.groups"
          :key="group.key"
          class="plan-editor-group"
        >
          <div class="plan-editor-rules">
            <label>
              <a-switch
                :checked="group.mode === 'sequential'"
                @change="group.mode = $event ? 'sequential' : 'parallel'"
              />
              <span>
                <strong>{{ t('releasePlan.editor.sequential') }}</strong>
                <small>{{ group.mode === 'sequential' ? t('releasePlan.editor.sequentialOn') : t('releasePlan.editor.sequentialOff') }}</small>
              </span>
            </label>
            <a-form-item :label="t('releasePlan.editor.failurePolicy')">
              <a-select
                v-model:value="group.failure_policy"
                :options="[
                  { value: 'stop', label: t('releasePlan.failurePolicy.stop') },
                  { value: 'continue', label: t('releasePlan.failurePolicy.continue') },
                ]"
              />
            </a-form-item>
          </div>

          <div class="plan-editor-add-application">
            <a-select
              v-model:value="applicationSelection[group.key]"
              mode="multiple"
              show-search
              allow-clear
              :placeholder="t('releasePlan.editor.applicationPlaceholder')"
              :options="availableApplications(group)"
              :filter-option="(input: string, option: { label?: string }) => String(option.label || '').toLowerCase().includes(input.toLowerCase())"
            />
            <a-button :disabled="!applicationSelection[group.key]?.length" @click="addApplication(group)">
              <Plus :size="14" />{{ t('releasePlan.editor.addApplication') }}
            </a-button>
          </div>

          <div
            :ref="(element) => setApplicationListRef(group.key, element)"
            class="plan-editor-applications"
          >
            <div
              v-for="(item, applicationIndex) in group.applications"
              :key="item.key"
              class="plan-editor-application"
            >
              <button type="button" class="plan-app-drag" :aria-label="t('releasePlan.editor.dragApplication')">
                <GripVertical :size="17" />
              </button>
              <span class="plan-app-order">{{ applicationIndex + 1 }}</span>
              <strong>{{ applicationName(item.application_id) }}</strong>
              <a-button danger type="text" :aria-label="t('releasePlan.editor.removeApplication')" @click="removeApplication(group, applicationIndex)">
                <Trash2 :size="15" />
              </a-button>
            </div>
          </div>
          <a-empty v-if="!group.applications.length" :description="t('releasePlan.editor.emptyApplications')" />
          <small class="plan-editor-order-hint">{{ t('releasePlan.editor.orderHint') }}</small>
        </article>
      </section>
    </a-form>

    <template #footer>
      <div class="plan-editor-footer">
        <a-button :disabled="saving" @click="emit('update:open', false)">{{ t('releasePlan.editor.cancel') }}</a-button>
        <a-button
          type="primary"
          :loading="saving"
          :disabled="!draft.description.trim() || draft.groups.length !== 1 || !draft.groups[0]?.applications.length"
          @click="submit"
        >
          {{ t(draft.id ? 'releasePlan.editor.save' : 'releasePlan.editor.create') }}
        </a-button>
      </div>
    </template>
  </a-drawer>
</template>

<style scoped>
.plan-editor-groups{display:grid;gap:12px}.plan-editor-groups>header{display:flex;align-items:center;justify-content:space-between;gap:16px}.plan-editor-groups>header strong,.plan-editor-groups>header small{display:block}.plan-editor-groups>header small{margin-top:3px;color:var(--edo-muted);font-size:12px}.plan-editor-groups :deep(.ant-btn){display:inline-flex;align-items:center;gap:5px}
.plan-editor-group{padding:14px;border:1px solid var(--edo-border);border-radius:12px;background:var(--edo-surface-soft)}
.plan-editor-rules{display:grid;align-items:end;grid-template-columns:minmax(0,1fr) minmax(180px,.55fr);gap:12px;margin-top:13px}.plan-editor-rules>label{display:flex;min-height:55px;align-items:center;gap:10px;padding:9px 11px;border:1px solid var(--edo-border);border-radius:9px;background:var(--edo-surface)}.plan-editor-rules>label strong,.plan-editor-rules>label small{display:block}.plan-editor-rules>label strong{font-size:13px}.plan-editor-rules>label small{margin-top:2px;color:var(--edo-muted);font-size:11px}.plan-editor-rules :deep(.ant-form-item){margin:0}.plan-editor-rules :deep(.ant-form-item-label){padding-bottom:4px}
.plan-editor-add-application{display:grid;grid-template-columns:minmax(0,1fr) auto;gap:8px;margin-top:13px}.plan-editor-applications{display:grid;gap:6px;margin-top:9px}.plan-editor-application{display:grid;min-height:42px;align-items:center;grid-template-columns:32px 26px minmax(0,1fr) 34px;gap:5px;padding:4px 5px;border:1px solid var(--edo-border);border-radius:9px;background:var(--edo-surface);transition:border-color 160ms ease,box-shadow 160ms ease}.plan-editor-application:hover{border-color:color-mix(in srgb,var(--edo-primary) 28%,var(--edo-border))}.plan-app-drag{display:grid;width:32px;height:32px;place-items:center;border:0;border-radius:7px;color:var(--edo-muted);background:transparent;cursor:grab}.plan-app-drag:active{cursor:grabbing}.plan-app-drag:focus-visible{outline:2px solid color-mix(in srgb,var(--edo-primary) 45%,transparent);outline-offset:1px}.plan-app-order{display:grid;width:23px;height:23px;place-items:center;border-radius:7px;color:var(--edo-primary);background:var(--edo-primary-soft);font-size:11px;font-weight:700}.plan-editor-application strong{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:13px}.plan-app-ghost{opacity:.4}.plan-app-chosen{border-color:var(--edo-primary);box-shadow:0 5px 18px color-mix(in srgb,var(--edo-primary) 15%,transparent)}.plan-editor-order-hint{display:block;margin-top:8px;color:var(--edo-muted);font-size:11px}.plan-editor-group :deep(.ant-empty){margin-block:10px}.plan-editor-footer{display:flex;justify-content:flex-end;gap:8px}
@media(max-width:640px){.plan-editor-groups>header{align-items:flex-start}.plan-editor-rules{grid-template-columns:1fr}.plan-editor-add-application{grid-template-columns:1fr}.plan-editor-add-application :deep(.ant-btn){justify-content:center}}
@media(prefers-reduced-motion:reduce){.plan-editor-application{transition:none}}
</style>
