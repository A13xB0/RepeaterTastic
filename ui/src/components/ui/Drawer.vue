<script setup lang="ts">
import { onBeforeUnmount, watch } from 'vue'
import { X } from '@lucide/vue'

const props = withDefaults(defineProps<{ open: boolean; title?: string; subtitle?: string; wide?: boolean }>(), { wide: false })
const emit = defineEmits<{ close: [] }>()

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
      <div v-if="open" class="fixed inset-0 z-[280] bg-black/25 dark:bg-black/50" @click="emit('close')" />
    </Transition>
    <Transition name="slide">
      <aside
        v-if="open"
        :class="['fixed inset-y-0 right-0 z-[281] flex w-full flex-col border-l border-line bg-surface-solid shadow-2xl', wide ? 'sm:max-w-2xl' : 'sm:max-w-md']"
      >
        <header class="flex items-start justify-between gap-3 border-b border-line-soft px-5 pb-3.5 pt-[max(1rem,env(safe-area-inset-top))]">
          <div class="min-w-0 flex-1">
            <slot name="header">
              <h2 class="truncate text-[15px] font-semibold tracking-tight">{{ title }}</h2>
              <p v-if="subtitle" class="mt-0.5 truncate text-[13px] text-ink-3">{{ subtitle }}</p>
            </slot>
          </div>
          <button class="icon-btn -mr-1.5" aria-label="Close" @click="emit('close')"><X class="size-4" /></button>
        </header>
        <div class="min-h-0 flex-1 overflow-y-auto px-5 py-4 pb-[max(1rem,env(safe-area-inset-bottom))]">
          <slot />
        </div>
      </aside>
    </Transition>
  </Teleport>
</template>
