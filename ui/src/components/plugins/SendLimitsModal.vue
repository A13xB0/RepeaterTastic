<script setup lang="ts">
// How much each plugin may transmit an hour. Applies at once to every plugin.
import { computed, ref, watch } from 'vue'
import { api } from '@/api/client'
import Modal from '@/components/ui/Modal.vue'
import Spinner from '@/components/ui/Spinner.vue'
import { toast } from '@/composables/toast'

const props = defineProps<{ open: boolean; messagesPerHour?: number; traceroutesPerHour?: number }>()
const emit = defineEmits<{ close: []; saved: [limits: { messages_per_hour: number; traceroutes_per_hour: number }] }>()

const MAX_MESSAGES = 600
const MAX_TRACEROUTES = 120
const messages = ref(30)
const traceroutes = ref(12)
const busy = ref(false)
const error = ref('')

watch(
  () => props.open,
  (o) => {
    if (!o) return
    messages.value = props.messagesPerHour ?? 30
    traceroutes.value = props.traceroutesPerHour ?? 12
    error.value = ''
  },
)

// Plugins may send a sixth of the hourly limit at once, then it refills evenly.
function describe(perHour: number, noun: string) {
  if (!perHour) return `Plugins can't send ${noun}.`
  const burst = Math.max(1, Math.floor(perHour / 6))
  const every = 3600 / perHour
  const pace = every >= 60 ? `${+(every / 60).toFixed(1)} min` : `${Math.round(every)} s`
  return `${burst} at once, then one every ${pace}.`
}
const valid = computed(
  () => Number.isInteger(messages.value) && messages.value >= 0 && messages.value <= MAX_MESSAGES &&
    Number.isInteger(traceroutes.value) && traceroutes.value >= 0 && traceroutes.value <= MAX_TRACEROUTES,
)

async function save() {
  busy.value = true
  error.value = ''
  try {
    const out = await api.put<{ messages_per_hour: number; traceroutes_per_hour: number }>('/plugins/limits', {
      messages_per_hour: messages.value,
      traceroutes_per_hour: traceroutes.value,
    })
    toast('Send limits saved')
    emit('saved', out)
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <Modal :open="open" title="Send limits" subtitle="How much each plugin may transmit. Applies to every plugin straight away." size="md" @close="emit('close')">
    <div class="grid gap-5">
      <div>
        <label class="label" for="sl-msg">Messages per hour, per plugin</label>
        <input id="sl-msg" v-model.number="messages" type="number" min="0" :max="MAX_MESSAGES" step="1" class="input tabular-nums" />
        <p class="hint">{{ describe(messages, 'messages') }} 0 to {{ MAX_MESSAGES }}.</p>
      </div>
      <div>
        <label class="label" for="sl-tr">Traceroutes per hour, per plugin</label>
        <input id="sl-tr" v-model.number="traceroutes" type="number" min="0" :max="MAX_TRACEROUTES" step="1" class="input tabular-nums" />
        <p class="hint">{{ describe(traceroutes, 'traceroutes') }} 0 to {{ MAX_TRACEROUTES }}.</p>
      </div>
      <p class="rounded-xl bg-raised px-3 py-2.5 text-xs leading-relaxed text-ink-3">
        Every traceroute is repeated by each node along the route and back, so it costs the whole mesh airtime. Whatever you set
        here, an identity still sends at most one traceroute every 30 seconds, and all transmissions stay within the radio's duty
        cycle.
      </p>
    </div>
    <p v-if="error" class="mt-3 text-[13px] text-bad">{{ error }}</p>
    <template #footer>
      <button class="btn" @click="emit('close')">Cancel</button>
      <button class="btn btn-primary" :disabled="busy || !valid" @click="save"><Spinner v-if="busy" />Save limits</button>
    </template>
  </Modal>
</template>
