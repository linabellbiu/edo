<script setup lang="ts">
defineProps<{ title?: string; description: string }>()
</script>

<template>
  <div class="page-toolbar">
    <div class="page-toolbar-copy">
      <strong v-if="title">{{ title }}</strong>
      <span>{{ description }}</span>
    </div>
    <div class="page-toolbar-actions"><slot /></div>
  </div>
</template>
