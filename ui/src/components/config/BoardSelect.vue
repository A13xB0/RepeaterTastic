<script setup lang="ts">
// The boards the experimental spi driver knows (GET /boards), grouped by where they come from,
// with a board file path as the last choice. The model is the board id that goes in radio.device.
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

const groups = computed(() =>
  (
    [
      ['config.d', 'meshtasticd config.d'],
      ['available.d', 'meshtasticd available.d'],
      ['built-in', 'Built-in'],
    ] as const
  )
    .map(([source, label]) => ({ source, label, boards: props.boards.filter((b) => b.source === source) }))
    .filter((g) => g.boards.length),
)
const auto = computed(() => props.boards.filter((b) => b.source === 'auto'))
</script>

<template>
  <div>
    <select :id="id" v-model="choice" :class="inputClass" aria-label="Board">
      <option v-if="!auto.length" value="auto">Detect automatically</option>
      <option v-for="b in auto" :key="b.id" :value="b.id">{{ b.name }}</option>
      <optgroup v-for="g in groups" :key="g.source" :label="g.label">
        <option v-for="b in g.boards" :key="b.id" :value="b.id" :disabled="!b.supported" :title="b.error || b.id">
          {{ b.name }} · {{ b.module }}{{ b.bus ? ` · ${b.bus}` : '' }}{{ b.supported ? '' : ' (not supported yet)' }}
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
