<script setup lang="ts">
// Opt-in features that are still being proven. Each switch says what it changes.
import { onMounted, ref } from 'vue'
import { FlaskConical } from '@lucide/vue'
import { api } from '@/api/client'
import Toggle from '@/components/ui/Toggle.vue'
import { live, refreshAllIdentities, refreshIdentities } from '@/store/live'
import { confirmDialog } from '@/composables/confirm'
import { toast, toastError } from '@/composables/toast'

interface Experimental {
  multi_radio_identities: boolean
}

const state = ref<Experimental | null>(null)
const busy = ref(false)

onMounted(async () => {
  try {
    state.value = await api.get<Experimental>('/experimental')
  } catch (e) {
    toastError(e)
  }
})

async function setMultiRadio(on: boolean) {
  if (!state.value) return
  const ok = await confirmDialog(
    on
      ? {
          title: 'Turn on identities on several radios?',
          body: 'Identities can then join extra radios, and each channel and DM picks the radio it goes out on. Routing is set per identity in its editor and can’t be changed from the Meshtastic app. This is experimental: watch airtime on every radio after enabling it.',
          confirm: 'Turn on',
        }
      : {
          title: 'Turn off identities on several radios?',
          body: 'Every identity leaves its extra radios straight away and stays on its home radio only. Routing tables are kept for when you turn it back on.',
          confirm: 'Turn off',
          danger: true,
        },
  )
  if (!ok) return
  busy.value = true
  try {
    state.value = await api.put<Experimental>('/experimental', { ...state.value, multi_radio_identities: on })
    live.multiRadioIdentities = state.value.multi_radio_identities // every page follows straight away
    refreshIdentities().catch(() => {})
    refreshAllIdentities()
    toast(on ? 'Identities on several radios: on' : 'Identities on several radios: off')
  } catch (e) {
    toastError(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="max-w-3xl space-y-4">
    <p class="flex items-start gap-2 text-xs text-ink-3">
      <FlaskConical class="mt-0.5 size-4 shrink-0" />
      Features that may change or go away. Everything here is off unless you turn it on, and turning one off puts things back as they were.
    </p>
    <div v-if="!state" class="h-24 animate-pulse rounded-xl bg-sunken" />
    <div v-else class="flex items-start justify-between gap-4 rounded-xl border border-line-soft px-4 py-3.5">
      <div class="min-w-0">
        <div class="flex flex-wrap items-center gap-2 text-[13px] font-medium">
          Identities on several radios
          <span :class="['chip', state.multi_radio_identities ? 'bg-ok/14 text-ok' : 'bg-ink-3/12 text-ink-3']">{{ state.multi_radio_identities ? 'on' : 'off' }}</span>
        </div>
        <p class="mt-1 text-xs text-ink-3">
          One identity sends and receives on more than one radio. Channel traffic goes on the radio carrying that channel, DMs on the radio where the other node was last heard best, and replies go back the way they came. Set per identity in its editor; not available from the Meshtastic app.
        </p>
      </div>
      <Toggle :model-value="state.multi_radio_identities" :disabled="busy" label="Identities on several radios" @update:model-value="setMultiRadio" />
    </div>
  </div>
</template>
