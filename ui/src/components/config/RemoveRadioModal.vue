<script setup lang="ts">
// Removing a running radio: its identities can move to another radio first (keys, chats and app
// ports go with them), or stay on disk off air until a radio with the same ID is added again.
import { computed, ref, watch } from 'vue'
import { api, enc } from '@/api/client'
import type { Identity } from '@/api/types'
import Modal from '@/components/ui/Modal.vue'
import Spinner from '@/components/ui/Spinner.vue'
import { live, refreshIdentities } from '@/store/live'

const props = defineProps<{ radio: { id: string; name: string } | null }>()
const emit = defineEmits<{ close: []; removed: [restartRequired: boolean] }>()

const choice = ref<'move' | 'leave'>('move')
const target = ref('')
const busy = ref(false)
const error = ref('')

const identities = computed(() => live.identities.filter((i) => i.radio_id === props.radio?.id && !i.is_relay))
const others = computed(() => live.radios.filter((r) => r.id !== props.radio?.id))

watch(
  () => props.radio?.id,
  () => {
    choice.value = identities.value.length ? 'move' : 'leave'
    target.value = others.value.find((r) => r.main)?.id ?? others.value[0]?.id ?? ''
    error.value = ''
  },
)

/** Moves every identity, stopping at the first that can't go; it says which and why. */
async function moveAll(): Promise<boolean> {
  for (const i of identities.value) {
    try {
      await api.post<Identity>(`/identities/${enc(i.node_id)}/move`, { radio_id: target.value })
    } catch (e) {
      error.value = `${i.long_name} couldn't move: ${(e as Error).message}. Nothing was removed.`
      return false
    }
  }
  return true
}

async function remove() {
  if (!props.radio) return
  busy.value = true
  error.value = ''
  try {
    if (choice.value === 'move' && identities.value.length && !(await moveAll())) return
    const r = await api.del<{ restart_required: boolean }>(`/radios/${enc(props.radio.id)}`)
    await refreshIdentities().catch(() => {})
    emit('removed', r.restart_required)
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <Modal :open="!!radio" :title="`Remove ${radio?.name ?? ''}?`" subtitle="The radio stops at the next restart." size="md" @close="emit('close')">
    <div v-if="radio" class="space-y-3 text-[13px]">
      <template v-if="identities.length">
        <p>
          {{ identities.length === 1 ? 'This identity lives' : `These ${identities.length} identities live` }} on {{ radio.name }}:
        </p>
        <ul class="flex flex-wrap gap-1.5">
          <li v-for="i in identities" :key="i.node_id" class="chip bg-ink-3/12 text-ink-2">{{ i.long_name }} <span class="mono ml-1 text-ink-3">{{ i.node_id }}</span></li>
        </ul>
        <fieldset class="space-y-2">
          <legend class="sr-only">What happens to them</legend>
          <label :class="['flex cursor-pointer items-start gap-2.5 rounded-xl border px-3 py-2.5', choice === 'move' ? 'border-brand/60 bg-brand/6' : 'border-line']">
            <input id="rm-move" v-model="choice" type="radio" value="move" class="mt-0.5 accent-[var(--brand)]" :disabled="!others.length" />
            <span class="min-w-0 flex-1">
              <span class="font-medium">Move them to</span>
              <select id="rm-target" v-model="target" class="input !h-7 !w-auto !py-0 ml-2 inline-block text-xs" aria-label="Radio to move them to" :disabled="choice !== 'move'">
                <option v-for="r in others" :key="r.id" :value="r.id">{{ r.name }} · {{ r.phy.preset_name }}</option>
              </select>
              <span class="mt-0.5 block text-xs text-ink-3">Keys, node numbers, chats and app ports go with them; their primary channel follows the new radio's preset.</span>
            </span>
          </label>
          <label :class="['flex cursor-pointer items-start gap-2.5 rounded-xl border px-3 py-2.5', choice === 'leave' ? 'border-brand/60 bg-brand/6' : 'border-line']">
            <input id="rm-leave" v-model="choice" type="radio" value="leave" class="mt-0.5 accent-[var(--brand)]" />
            <span>
              <span class="font-medium">Leave them off air</span>
              <span class="mt-0.5 block text-xs text-ink-3">They stay on disk; adding a radio with the ID <span class="mono">{{ radio.id }}</span> again brings them back.</span>
            </span>
          </label>
        </fieldset>
      </template>
      <p v-else class="text-ink-3">No identities live on it, only its relay persona, which stays on disk in case the radio comes back.</p>
      <p v-if="error" class="text-bad">{{ error }}</p>
    </div>
    <template #footer>
      <button type="button" class="btn" @click="emit('close')">Cancel</button>
      <button type="button" class="btn btn-danger" :disabled="busy || (choice === 'move' && identities.length > 0 && !target)" @click="remove">
        <Spinner v-if="busy" />{{ choice === 'move' && identities.length ? 'Move and remove' : 'Remove radio' }}
      </button>
    </template>
  </Modal>
</template>
