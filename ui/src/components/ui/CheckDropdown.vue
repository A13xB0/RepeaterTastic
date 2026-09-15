<script setup lang="ts">
// A dropdown with a tick box per option, for choosing several things (radios, ports, ...).
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { Check, ChevronDown } from '@lucide/vue'

const props = withDefaults(
  defineProps<{ options: { value: string; label: string; hint?: string }[]; id?: string; disabled?: boolean; emptyLabel?: string }>(),
  { emptyLabel: 'None' },
)
const model = defineModel<string[]>({ required: true })
const open = ref(false)
const root = ref<HTMLElement | null>(null)

const summary = computed(() => {
  const chosen = props.options.filter((o) => model.value.includes(o.value))
  if (!chosen.length) return props.emptyLabel
  if (chosen.length === props.options.length && props.options.length > 1) return `All ${chosen.length}: ${chosen.map((o) => o.label).join(', ')}`
  return chosen.map((o) => o.label).join(', ')
})

function toggle(v: string) {
  model.value = model.value.includes(v) ? model.value.filter((x) => x !== v) : [...model.value, v]
}

function onPointer(e: PointerEvent) {
  if (root.value && !root.value.contains(e.target as Node)) open.value = false
}
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') open.value = false
}
watch(open, (o) => {
  if (o) {
    document.addEventListener('pointerdown', onPointer)
    document.addEventListener('keydown', onKey)
  } else {
    document.removeEventListener('pointerdown', onPointer)
    document.removeEventListener('keydown', onKey)
  }
})
onBeforeUnmount(() => (open.value = false))
</script>

<template>
  <div ref="root" class="relative">
    <button
      :id="id"
      type="button"
      class="input flex items-center gap-2 text-left"
      :disabled="disabled"
      :aria-expanded="open"
      aria-haspopup="listbox"
      @click="open = !open"
    >
      <span :class="['min-w-0 flex-1 truncate', model.length ? '' : 'text-ink-3']">{{ summary }}</span>
      <ChevronDown :class="['size-4 shrink-0 text-ink-3 transition-transform', open && 'rotate-180']" />
    </button>
    <div
      v-if="open"
      role="listbox"
      aria-multiselectable="true"
      class="absolute inset-x-0 z-50 mt-1 max-h-72 overflow-y-auto rounded-xl border border-line bg-surface-solid p-1 shadow-xl"
    >
      <button
        v-for="o in options"
        :key="o.value"
        type="button"
        role="option"
        :aria-selected="model.includes(o.value)"
        class="flex w-full items-center gap-3 rounded-lg px-2.5 py-2 text-left text-[13px] hover:bg-sunken"
        @click="toggle(o.value)"
      >
        <span
          :class="[
            'flex size-4 shrink-0 items-center justify-center rounded border',
            model.includes(o.value) ? 'border-brand bg-brand text-brand-ink' : 'border-line bg-surface-solid',
          ]"
        >
          <Check v-if="model.includes(o.value)" class="size-3" :stroke-width="3" />
        </span>
        <span class="min-w-0 flex-1">
          <span class="block truncate">{{ o.label }}</span>
          <span v-if="o.hint" class="mono block truncate text-2xs text-ink-3">{{ o.hint }}</span>
        </span>
      </button>
      <div v-if="!options.length" class="px-2.5 py-2 text-[13px] text-ink-3">Nothing to choose from.</div>
      <div v-if="model.length" class="border-t border-line-soft px-1 pt-1">
        <button type="button" class="w-full rounded-lg px-2.5 py-1.5 text-left text-xs text-ink-3 hover:bg-sunken hover:text-ink" @click="model = []">
          Clear ({{ emptyLabel.toLowerCase() }})
        </button>
      </div>
    </div>
  </div>
</template>
