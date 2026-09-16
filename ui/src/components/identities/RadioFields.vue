<script setup lang="ts">
// An identity's home radio: a choice when the site has more than one radio and it isn't a relay,
// otherwise shown as text.
import { computed } from 'vue'
import { live, radioName } from '@/store/live'
import { num } from '@/lib/format'

const props = defineProps<{ relay?: boolean; creating?: boolean; savedHome?: string }>()
const home = defineModel<string>('home', { required: true })

const canMove = computed(() => live.radios.length > 1 && !props.relay)
const describe = (id: string) => {
  const r = live.radios.find((x) => x.id === id)
  return r ? `${r.name} · ${r.phy.preset_name} · ${num(r.phy.frequency_mhz, 3)} MHz` : radioName(id)
}
</script>

<template>
  <div>
    <label class="label" for="rf-home">Home radio</label>
    <select v-if="canMove" id="rf-home" v-model="home" class="input">
      <option v-for="r in live.radios" :key="r.id" :value="r.id">{{ describe(r.id) }}</option>
    </select>
    <div v-else id="rf-home" class="input !h-auto min-h-9 !cursor-default py-2 text-[13px]">{{ describe(home) }}</div>
    <p v-if="!creating && savedHome && home !== savedHome" class="hint !text-warn">Saving moves this identity to another radio. You'll be asked to confirm.</p>
    <p v-else class="hint">Where it lives: its key, app port and chats.{{ relay ? ' A relay persona stays with its radio.' : '' }}</p>
  </div>
</template>
