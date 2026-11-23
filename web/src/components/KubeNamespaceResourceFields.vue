<script setup lang="ts">
import { computed, ref, useId } from 'vue'
import { useI18n } from 'vue-i18n'
import { Trash2 } from 'lucide-vue-next'
import KubeEditorSelect from './KubeEditorSelect.vue'
import KubernetesResourceQuantityInput from './KubernetesResourceQuantityInput.vue'
import { limitFields, limitTypes, quotaAmountKind, quotaResourcePresets, type LimitRule, type NamespaceResourceDraft, type QuotaLimit } from '@/utils/kubernetesNamespaceResources'

const draft = defineModel<NamespaceResourceDraft>({ required: true })
defineProps<{ disabled: boolean }>()
const { t } = useI18n()
const fieldsId = useId()
const quotaSearch = ref('')
const quotaEntries = computed(() => {
  if (draft.value.kind !== 'ResourceQuota') return []
  const limits = draft.value.limits
  const typed = quotaSearch.value.trim()
  const manual = typed.length <= 317 && /^[a-zA-Z0-9][a-zA-Z0-9._-]*(?:\/[a-zA-Z0-9][a-zA-Z0-9._-]*)?$/.test(typed) ? typed : ''
  return limits.map(limit => {
    const used = new Set(limits.filter(row => row !== limit).map(row => row.name))
    const custom = new Set([limit.name, manual].filter(name => name && !Object.hasOwn(quotaResourcePresets, name)))
    const choices = [
      ...Object.entries(quotaResourcePresets).map(([value, key]) => ({ value, label: t(`k8s.${key}`), manual: false, disabled: used.has(value) })),
      ...[...custom].map(value => ({ value, label: value, manual: true, disabled: used.has(value) })),
    ]
    return { limit, choices, kind: quotaAmountKind(limit.name) }
  })
})

function searchQuota(search: string, option?: { value?: unknown; label?: unknown }) {
  const term = search.trim().toLowerCase()
  return [option?.value, option?.label].some(value => String(value ?? '').toLowerCase().includes(term))
}
function selectQuota(limit: QuotaLimit, name: string) {
  // 切换到不同种类的配额时，避免把原数值解释成新资源的单位。
  if (limit.name && quotaAmountKind(limit.name) !== quotaAmountKind(name)) limit.amount = ''
  limit.name = name
  quotaSearch.value = ''
}

function addLimitRule() {
  const form = draft.value
  if (form.kind !== 'LimitRange') return
  const type = limitTypes.find(type => form.rules.every(rule => rule.type !== type))
  if (type) form.rules.push({ type, resources: [{ name: '' }] })
}
function visibleFields(rule: LimitRule) {
  return limitFields.filter(field => rule.type === 'Container' || field === 'min' || field === 'max' ||
    rule.type === 'Pod' && field === 'maxLimitRequestRatio' || rule.resources.some(item => Boolean(item[field])))
}
</script>

<template>
  <section v-if="draft.kind === 'ResourceQuota'" class="namespace-policy-fields" :aria-label="t('k8s.quotaHard')">
    <h3>{{ t('k8s.quotaHard') }}</h3>
    <div v-for="(entry, index) in quotaEntries" :key="index" class="quota-editor-row">
      <div class="quota-field">
        <label :for="`${fieldsId}-quota-${index}`">{{ t('k8s.resourceKey') }}</label>
        <KubeEditorSelect :id="`${fieldsId}-quota-${index}`" :active="!disabled" :value="entry.limit.name || undefined"
          :placeholder="t('k8s.quotaSelectOrEnter')" :options="entry.choices" :filter-option="searchQuota" show-search
          @search="quotaSearch = $event" @dropdown-visible-change="open => { if (!open) quotaSearch = '' }" @update:value="selectQuota(entry.limit, $event)">
          <template #option="{ value, label, manual }">
            <span class="quota-option" :title="value">
              <span>{{ label }}</span>
              <small v-if="manual || label !== value" class="quota-option-detail">{{ manual ? t('k8s.quotaCustomKey') : value }}</small>
            </span>
          </template>
        </KubeEditorSelect>
      </div>
      <div class="quota-field">
        <span>{{ t('k8s.quotaLimitValue') }}</span>
        <KubernetesResourceQuantityInput v-if="entry.kind !== 'raw' && entry.kind !== 'count'" :key="entry.limit.name" v-model:value="entry.limit.amount"
          :label="t('k8s.quotaLimitValue')" :resource="entry.kind" :disabled="disabled" />
        <a-input v-else v-model:value="entry.limit.amount" :aria-label="t('k8s.quotaLimitValue')" :disabled="disabled || !entry.limit.name"
          :inputmode="entry.kind === 'count' ? 'numeric' : undefined" :addon-after="entry.kind === 'count' ? t('k8s.quotaObjectUnit') : undefined" :maxlength="128" />
      </div>
      <a-button type="text" danger class="quota-remove" :disabled="disabled" :aria-label="t('k8s.remove')" @click="draft.limits.splice(index, 1)"><Trash2 :size="14" aria-hidden="true" /></a-button>
    </div>
    <a-button class="namespace-add-field" size="small" :disabled="disabled" @click="draft.limits.push({ name: '', amount: '' })">+ {{ t('k8s.addQuotaLimit') }}</a-button>
  </section>
  <section v-else-if="draft.kind === 'LimitRange'" class="namespace-policy-fields" :aria-label="t('k8s.limitRules')">
    <h3>{{ t('k8s.limitRules') }}</h3>
    <fieldset v-for="(rule, index) in draft.rules" :key="index" class="limit-rule" :disabled="disabled">
      <legend class="namespace-sr-only">{{ t('k8s.limitRuleNumber', { count: index + 1 }) }}</legend>
      <div class="limit-rule-heading">
        <div class="limit-type-field">
          <label :for="`${fieldsId}-limit-${index}`">{{ t('k8s.limitType') }}</label>
          <KubeEditorSelect :id="`${fieldsId}-limit-${index}`" v-model:value="rule.type" :active="!disabled"
            :options="limitTypes.map(value => ({ value, label: t(`k8s.limitType_${value}`), disabled: draft.kind === 'LimitRange' && draft.rules.some((item, position) => position !== index && item.type === value) }))" />
        </div>
        <a-button type="text" danger :disabled="disabled" :aria-label="t('k8s.removeLimitRule')" @click="draft.rules.splice(index, 1)"><Trash2 :size="14" aria-hidden="true" /></a-button>
      </div>
      <div v-for="(resource, position) in rule.resources" :key="position" class="limit-resource-row">
        <label><span>{{ t('k8s.resourceKey') }}</span><a-input v-model:value="resource.name" :maxlength="253" :placeholder="rule.type === 'PersistentVolumeClaim' ? 'storage' : 'cpu'" /></label>
        <label v-for="field in visibleFields(rule)" :key="field"><span>{{ t(`k8s.limit_${field}`) }}</span><a-input v-model:value="resource[field]" :maxlength="128" /></label>
        <a-button type="text" danger :disabled="disabled" :aria-label="t('k8s.remove')" @click="rule.resources.splice(position, 1)"><Trash2 :size="14" aria-hidden="true" /></a-button>
      </div>
      <a-button class="namespace-add-field" size="small" :disabled="disabled" @click="rule.resources.push({ name: '' })">+ {{ t('k8s.addLimitResource') }}</a-button>
    </fieldset>
    <a-button v-if="draft.rules.length < limitTypes.length" class="namespace-add-field" size="small" :disabled="disabled" @click="addLimitRule">+ {{ t('k8s.addLimitRule') }}</a-button>
  </section>
  <section v-else class="namespace-policy-fields" :aria-label="t('k8s.serviceAccountSettings')">
    <h3>{{ t('k8s.serviceAccountSettings') }}</h3>
    <div class="account-fields">
      <a-form-item :label="t('k8s.serviceAccountAutomount')" :html-for="`${fieldsId}-automount`">
        <KubeEditorSelect :id="`${fieldsId}-automount`" v-model:value="draft.automount" :active="!disabled" :options="['default','enabled','disabled'].map(value => ({ value, label: t(`k8s.automount_${value}`) }))" />
      </a-form-item>
      <a-form-item :label="t('k8s.imagePullSecretReferences')">
        <a-input v-model:value="draft.imagePullSecrets" :aria-label="t('k8s.imagePullSecretReferences')" placeholder="registry-auth" />
      </a-form-item>
    </div>
  </section>
</template>

<style scoped>
.namespace-policy-fields { display: grid; gap: 10px; margin-block: 14px; min-width: 0; }
h3 { border-bottom: 1px solid var(--edo-border); padding-bottom: 8px; margin: 0; color: var(--edo-text); font-size: 13px; font-weight: 600; }
.quota-editor-row { display: grid; grid-template-columns: minmax(0, 268px) minmax(0, 184px) 32px; align-items: end; gap: 8px; }
.quota-field, .limit-type-field, .limit-resource-row label { display: grid; gap: 4px; min-width: 0; color: var(--edo-muted); font-size: 12px; }
.quota-field { grid-template-columns: minmax(0, 1fr); }
.quota-field :deep(.ant-select), .limit-rule-heading :deep(.ant-select) { width: 100%; }
.quota-option { display: flex; align-items: baseline; gap: 8px; }
.quota-option span, .quota-option-detail { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
.quota-option-detail { flex: 1 1 auto; color: var(--edo-muted); font-size: 12px; }
.quota-remove { display: inline-grid; place-items: center; padding: 0; }
.limit-rule-heading, .limit-resource-row { display: flex; align-items: end; flex-wrap: wrap; gap: 6px; }
.limit-rule { display: grid; gap: 8px; min-width: 0; border: 1px solid var(--edo-border); border-radius: 6px; padding: 10px; margin: 0; }
.limit-rule-heading { justify-content: space-between; }
.limit-type-field { width: min(100%, 200px); }
.limit-resource-row label { flex: 1 0 78px; }
.limit-resource-row label:first-child { flex: 2 0 120px; }
.namespace-add-field { justify-self: start; }
.account-fields { display: grid; gap: 10px; grid-template-columns: minmax(150px, 210px) minmax(0, 1fr); }
.account-fields :deep(.ant-form-item) { margin-bottom: 0; }
.namespace-sr-only { position: absolute; width: 1px; height: 1px; padding: 0; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
@media (max-width: 560px) {
  .quota-editor-row { grid-template-columns: minmax(0, 1.2fr) minmax(0, 1fr) 26px; gap: 6px; }
  .account-fields { grid-template-columns: 1fr; }
}
@media (max-width: 360px) {
  .quota-editor-row { grid-template-columns: minmax(0, 1fr) 26px; }
  .quota-editor-row .quota-field:first-child { grid-column: 1 / -1; }
}
</style>
