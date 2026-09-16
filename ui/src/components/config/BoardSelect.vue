<script setup lang="ts">
// The boards the experimental spi driver knows (GET /boards), grouped by the computer they are
// for, with a board file path as the last choice. The model is the board id that goes in radio.device.
import { computed, ref, watch } from 'vue'
import type { Board } from '@/api/types'

const props = withDefaults(defineProps<{ id: string; boards: Board[]; inputClass?: string }>(), { inputClass: 'input' })
const device = defineModel<string>({ required: true })

const CUSTOM = '__custom__'
const known = (v: string) => props.boards.some((b) => b.id === v)
const choice = ref(!device.value ? 'auto' : known(device.value) ? device.value : CUSTOM)
const custom = ref(choice.value === CUSTOM ? device.value : '')

watch(device, (v) => {
  if (known(v)) choice.value = v
  else if (v) {
    choice.value = CUSTOM
    custom.value = v
  }
})
// The list arrives after the model: a config.d path typed as custom moves into its group.
watch(() => props.boards, () => known(device.value) && (choice.value = device.value))
watch(choice, (c) => (device.value = c === CUSTOM ? custom.value.trim() : c))
watch(custom, (v) => choice.value === CUSTOM && (device.value = v.trim()))

// One group per host computer (Meta.compatible): the same HAT has a file per host, and the host is
// what the user knows. Files found on this machine come first, with where they were found.
const groups = computed(() => {
  const order = ['Raspberry Pi', 'USB']
  const byHost = new Map<string, Board[]>()
  for (const b of props.boards) {
    if (b.source === 'auto') continue
    const host = b.source === 'built-in' ? b.host || 'Other' : 'On this machine (/etc/meshtasticd)'
    byHost.set(host, [...(byHost.get(host) ?? []), b])
  }
  const rank = (h: string) => (h.startsWith('On this') ? -1 : order.indexOf(h) === -1 ? order.length : order.indexOf(h))
  return [...byHost.entries()]
    .sort(([a], [b]) => rank(a) - rank(b) || a.localeCompare(b))
    .map(([label, boards]) => ({ label, boards: [...boards].sort((x, y) => x.name.localeCompare(y.name)) }))
})
const auto = computed(() => props.boards.filter((b) => b.source === 'auto'))
</script>

<template>
  <div>
    <select :id="id" v-model="choice" :class="inputClass" aria-label="Board">
      <option v-if="!auto.length" value="auto">Detect automatically</option>
      <option v-for="b in auto" :key="b.id" :value="b.id">{{ b.name }}</option>
      <optgroup v-for="g in groups" :key="g.label" :label="g.label">
        <option v-for="b in g.boards" :key="b.id" :value="b.id" :disabled="!b.supported" :title="b.error || b.id">
          {{ b.name }}{{ b.source !== 'built-in' ? ` · ${b.host} (${b.source}/${b.file})` : '' }}{{ b.bus ? ` · ${b.bus}` : '' }}{{ b.supported ? '' : ' (not supported yet)' }}
        </option>
      </optgroup>
      <option :value="CUSTOM">Board file path…</option>
    </select>
    <input
      v-if="choice === CUSTOM"
      :id="`${id}-path`"
      v-model="custom"
      :class="[inputClass, 'mono mt-2']"
      placeholder="/etc/meshtasticd/config.d/lora-….yaml"
      aria-label="Board file path"
      spellcheck="false"
    />
  </div>
</template>
