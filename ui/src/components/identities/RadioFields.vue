<script setup lang="ts">
// Home radio and default radio of an identity, always visible. Each is a choice only when it can
// actually be changed; otherwise it's shown as text with the reason.
import { computed } from 'vue'
import { live, radioName } from '@/store/live'
import { num } from '@/lib/format'

const props = defineProps<{ relay?: boolean; creating?: boolean; savedHome?: string; savedDefault?: string }>()
const home = defineModel<string>('home', { required: true })
const defaultRadio = defineModel<string>('defaultRadio', { required: true })

const several = computed(() => live.radios.length > 1)
const canMove = computed(() => several.value && !props.relay)
const canChooseDefault = computed(() => several.value && live.multiRadioIdentities && !props.relay)
const describe = (id: string) => {
  const r = live.radios.find((x) => x.id === id)
  return r ? `${r.name} · ${r.phy.preset_name} · ${num(r.phy.frequency_mhz, 3)} MHz` : radioName(id)
}
const defaultWhy = computed(() => {
  if (props.relay) return 'A relay persona always uses its own radio.'
  if (!several.value) return 'Add a second radio under Configuration → Radios to choose another.'
  if (!live.multiRadioIdentities) return 'Turn on Configuration → Experimental → Identities on several radios to choose another.'
  return ''
})
</script>

<template>
  <div class="grid gap-3 rounded-xl border border-line-soft bg-raised px-3.5 py-3 sm:grid-cols-2">
    <div>
      <label class="label" for="rf-home">Home radio</label>
      <select v-if="canMove" id="rf-home" v-model="home" class="input">
        <option v-for="r in live.radios" :key="r.id" :value="r.id">{{ describe(r.id) }}</option>
      </select>
      <div v-else id="rf-home" class="input !h-auto min-h-9 !cursor-default py-2 text-[13px]">{{ describe(home) }}</div>
      <p v-if="!creating && savedHome && home !== savedHome" class="hint !text-warn">Saving moves this identity to another radio. You'll be asked to confirm.</p>
      <p v-else class="hint">Where it lives: its key, app port and chats.{{ relay ? ' A relay persona stays with its radio.' : '' }}</p>
    </div>
    <div>
      <label class="label" for="rf-default">Default radio</label>
      <select v-if="canChooseDefault" id="rf-default" v-model="defaultRadio" class="input">
        <option v-for="r in live.radios" :key="r.id" :value="r.id">{{ describe(r.id) }}{{ r.id === home ? ' (home)' : '' }}</option>
      </select>
      <div v-else id="rf-default" class="input !h-auto min-h-9 !cursor-default py-2 text-[13px]">{{ describe(home) }} <span class="text-ink-3">(same as home)</span></div>
      <p v-if="defaultWhy" class="hint">{{ defaultWhy }}</p>
      <p v-else class="hint">Slot 0 is this radio's primary; new channels start here and DMs fall back to it.</p>
    </div>
  </div>
</template>
