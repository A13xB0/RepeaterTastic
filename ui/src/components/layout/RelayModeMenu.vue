<script setup lang="ts">
// A radio's relay mode as a small menu: the current mode, and every mode with what it does.
import { computed, onBeforeUnmount, ref } from 'vue'
import { ChevronDown } from '@lucide/vue'
import type { RelayRole } from '@/api/types'
import { relayModes } from '@/lib/relay'

const props = defineProps<{ role: string; label: string; disabled?: boolean }>()
const emit = defineEmits<{ pick: [role: RelayRole] }>()

const open = ref(false)
const root = ref<HTMLElement | null>(null)
const current = computed(() => relayModes.find((m) => m.id === props.role))

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

function pick(role: RelayRole) {
  open.value = false
  emit('pick', role)
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
      :aria-label="`Relay mode on ${label}: ${current?.label ?? role}`"
      :aria-expanded="open"
      aria-haspopup="menu"
      :disabled="disabled"
      @click.stop="open = !open"
    >
      {{ current?.label ?? role }}<ChevronDown class="size-3 text-ink-3" />
    </button>
    <div v-if="open" role="menu" :aria-label="`Relay mode on ${label}`" class="absolute right-0 top-full z-50 mt-1.5 w-72 rounded-xl border border-line bg-surface-solid p-1 shadow-xl">
      <template v-for="(m, i) in relayModes" :key="m.id">
        <div v-if="i > 0 && relayModes[i - 1]!.group !== m.group" class="mx-2 my-1 h-px bg-line-soft" role="separator" />
        <button
          type="button"
          role="menuitemradio"
          :aria-checked="m.id === role"
          :class="['block w-full rounded-lg px-2.5 py-1.5 text-left hover:bg-raised', m.id === role && 'bg-raised']"
          @click="pick(m.id)"
        >
          <span :class="['block text-[13px] font-medium', m.id === role ? m.tone : '']">{{ m.label }}</span>
          <span class="block text-2xs leading-snug text-ink-3">{{ detail(m.title) }}</span>
        </button>
      </template>
    </div>
  </div>
</template>
