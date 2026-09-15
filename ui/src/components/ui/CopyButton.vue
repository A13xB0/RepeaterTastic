<script setup lang="ts">
import { Copy } from '@lucide/vue'
import { copyText } from '@/composables/clipboard'

defineProps<{ text: string; label?: string }>()
</script>

<template>
  <button type="button" class="icon-btn size-6 rounded-md" :title="label ?? 'Copy'" @click.stop="copyText(text, label ? `${label} copied` : 'Copied')">
    <Copy class="size-3.5" />
  </button>
</template>
