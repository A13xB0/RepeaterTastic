<script setup lang="ts">
import { onBeforeUnmount, watch } from 'vue'
import { X } from '@lucide/vue'

const props = withDefaults(defineProps<{ open: boolean; title?: string; subtitle?: string; size?: 'sm' | 'md' | 'lg' | 'xl' }>(), { size: 'md' })
const emit = defineEmits<{ close: [] }>()

const widths = { sm: 'max-w-sm', md: 'max-w-lg', lg: 'max-w-2xl', xl: 'max-w-4xl' }

function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') emit('close')
}
watch(
  () => props.open,
  (o) => (o ? window.addEventListener('keydown', onKey) : window.removeEventListener('keydown', onKey)),
  { immediate: true },
)
onBeforeUnmount(() => window.removeEventListener('keydown', onKey))
</script>

<template>
  <Teleport to="body">
    <Transition name="fade">
      <div v-if="open" class="fixed inset-0 z-[300] bg-black/35 backdrop-blur-[2px] dark:bg-black/60" @click="emit('close')" />
    </Transition>
    <Transition name="pop">
      <div v-if="open" class="pointer-events-none fixed inset-0 z-[301] flex items-end justify-center p-0 sm:items-center sm:p-6">
        <div
          role="dialog"
          aria-modal="true"
          :class="['pointer-events-auto flex max-h-[92dvh] w-full flex-col overflow-hidden rounded-t-2xl border border-line bg-surface-solid shadow-2xl sm:rounded-2xl', widths[size]]"
        >
          <header v-if="title" class="flex items-start justify-between gap-3 border-b border-line-soft px-5 py-4">
            <div class="min-w-0">
              <h2 class="text-[15px] font-semibold tracking-tight">{{ title }}</h2>
              <p v-if="subtitle" class="mt-0.5 text-[13px] text-ink-3">{{ subtitle }}</p>
            </div>
            <button class="icon-btn -mr-1.5 -mt-1" aria-label="Close" @click="emit('close')"><X class="size-4" /></button>
          </header>
          <div class="min-h-0 flex-1 overflow-y-auto px-5 py-4">
            <slot />
          </div>
          <footer v-if="$slots.footer" class="flex flex-wrap items-center justify-end gap-2 border-t border-line-soft bg-raised/60 px-5 py-3">
            <slot name="footer" />
          </footer>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>
