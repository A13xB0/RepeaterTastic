<script setup lang="ts">
import Toasts from '@/components/ui/Toasts.vue'
import ConfirmHost from '@/components/ui/ConfirmHost.vue'
</script>

<template>
  <RouterView />
  <Toasts />
  <ConfirmHost />
</template>
