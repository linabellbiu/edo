<script setup lang="ts">
defineProps<{ title?: string; description?: string }>()
</script>

<template>
  <div class="page-toolbar">
    <div v-if="title || description" class="page-toolbar-copy">
      <strong v-if="title">{{ title }}</strong>
      <span v-if="description">{{ description }}</span>
    </div>
    <div class="page-toolbar-actions"><slot /></div>
  </div>
</template>
