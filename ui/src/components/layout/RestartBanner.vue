<script setup lang="ts">
// Shown on every page while saved changes wait for a daemon restart.
import { computed, ref } from 'vue'
import { RotateCcw, TriangleAlert } from '@lucide/vue'
import { api } from '@/api/client'
import { live } from '@/store/live'
import { confirmDialog } from '@/composables/confirm'
import { toast, toastError } from '@/composables/toast'

const reasons = computed(() => live.status?.restart_reasons ?? [])
const restarting = ref(false)

async function restart() {
  const ok = await confirmDialog({
    title: 'Restart RepeaterTastic?',
    body: `Applies: ${reasons.value.join(', ')}. Every identity drops off air and its app connections close for about 15 seconds.`,
    confirm: 'Restart',
  })
  if (!ok) return
  restarting.value = true
  try {
    await api.post('/restart')
    toast('Restarting… the page reconnects by itself')
  } catch (e) {
    toastError(e)
  } finally {
    setTimeout(() => (restarting.value = false), 20000)
  }
}
</script>

<template>
  <div v-if="reasons.length" class="mb-4 flex flex-wrap items-center gap-2.5 rounded-xl border border-warn/30 bg-warn/10 px-4 py-2.5 text-[13px]" role="status">
    <TriangleAlert class="size-4 shrink-0 text-warn" />
    <span class="min-w-0 flex-1">Restart to apply saved changes: <span class="font-medium">{{ reasons.join(', ') }}</span>.</span>
    <button type="button" class="btn btn-sm" :disabled="restarting" @click="restart"><RotateCcw :class="['size-3.5', restarting && 'animate-spin']" />{{ restarting ? 'Restarting…' : 'Restart now' }}</button>
  </div>
</template>
