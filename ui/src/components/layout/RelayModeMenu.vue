<script setup lang="ts">
// A radio's relay mode as a small menu: the current mode, and every mode with what it does.
import { computed, onBeforeUnmount, ref } from 'vue'
import { ChevronDown } from '@lucide/vue'
import type { RelayRole } from '@/api/types'
import { relayModes } from '@/lib/relay'

const props = defineProps<{ mode: string; label: string; disabled?: boolean }>()
const emit = defineEmits<{ pick: [mode: RelayRole] }>()

const open = ref(false)
const root = ref<HTMLElement | null>(null)
const current = computed(() => relayModes.find((m) => m.id === props.mode))

function onDocClick(e: MouseEvent) {
  if (open.value && root.value && !root.value.contains(e.target as Node)) open.value = false
}
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') open.value = false
}
document.addEventListener('click', onDocClick)
document.addEventListener('keydown', onKey)
onBeforeUnmount(() => {
  document.removeEventListener('click', onDocClick)
  document.removeEventListener('keydown', onKey)
})

function pick(mode: RelayRole) {
  open.value = false
  emit('pick', mode)
}
// "CLIENT: rebroadcasts after routers…" → "rebroadcasts after routers…"
const detail = (title: string) => title.replace(/^[A-Z_]+: /, '')
</script>

<template>
  <div ref="root" class="relative">
    <button
      type="button"
      class="flex h-6 items-center gap-1 rounded-md border border-line-soft bg-surface-solid px-2 text-xs font-medium transition-colors hover:border-line disabled:opacity-60"
      :class="current?.tone"
      :aria-label="`Relay mode on ${label}: ${current?.label ?? mode}`"
      :aria-expanded="open"
      aria-haspopup="menu"
      :disabled="disabled"
      @click.stop="open = !open"
    >
      {{ current?.label ?? mode }}<ChevronDown class="size-3 text-ink-3" />
    </button>
    <div v-if="open" role="menu" :aria-label="`Relay mode on ${label}`" class="absolute right-0 top-full z-50 mt-1.5 w-72 rounded-xl border border-line bg-surface-solid p-1 shadow-xl">
      <template v-for="(m, i) in relayModes" :key="m.id">
        <hr v-if="i > 0 && relayModes[i - 1]!.group !== m.group" class="mx-2 my-1 h-px border-0 bg-line-soft" />
        <button
          type="button"
          role="menuitemradio"
          :aria-checked="m.id === mode"
          :class="['block w-full rounded-lg px-2.5 py-1.5 text-left hover:bg-raised', m.id === mode && 'bg-raised']"
          @click="pick(m.id)"
        >
          <span :class="['block text-[13px] font-medium', m.id === mode ? m.tone : '']">{{ m.label }}</span>
          <span class="block text-2xs leading-snug text-ink-3">{{ detail(m.title) }}</span>
        </button>
      </template>
    </div>
  </div>
</template>
