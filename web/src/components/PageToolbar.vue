<script setup lang="ts">
defineProps<{ summary?: string }>()
</script>

<template>
  <div class="page-toolbar">
    <span v-if="summary" class="page-toolbar-summary">{{ summary }}</span>
    <div class="page-toolbar-actions"><slot /></div>
  </div>
</template>
