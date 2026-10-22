<script setup lang="ts">
import { Ellipsis } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
withDefaults(defineProps<{ more?: boolean; loading?: boolean }>(), { more: true, loading: false })
const { t } = useI18n()
</script>

<template>
  <div class="resource-row-actions">
    <slot />
    <a-dropdown v-if="more && $slots.more" :trigger="['click']" placement="bottomRight">
      <a-button size="small" class="resource-more-button" :aria-label="t('resourceList.more')" :loading="loading"><Ellipsis :size="16" /></a-button>
      <template #overlay><a-menu><slot name="more" /></a-menu></template>
    </a-dropdown>
  </div>
</template>
