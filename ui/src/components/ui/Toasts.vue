<script setup lang="ts">
import { CircleCheck, CircleAlert, Info, X } from '@lucide/vue'
import { dismiss, toasts } from '@/composables/toast'
</script>

<template>
  <Teleport to="body">
    <div class="pointer-events-none fixed inset-x-0 bottom-0 z-[400] flex flex-col items-center gap-2 p-4 sm:items-end">
      <TransitionGroup name="pop">
        <output
          v-for="t in toasts"
          :key="t.id"
          class="pointer-events-auto flex max-w-sm items-start gap-2.5 rounded-xl border border-line bg-surface-solid px-3.5 py-2.5 text-[13px] shadow-xl"
        >
          <CircleCheck v-if="t.kind === 'ok'" class="mt-px size-4 shrink-0 text-ok" />
          <CircleAlert v-else-if="t.kind === 'error'" class="mt-px size-4 shrink-0 text-bad" />
          <Info v-else class="mt-px size-4 shrink-0 text-info" />
          <span class="min-w-0 flex-1 break-words">{{ t.text }}</span>
          <button type="button" class="-mr-1 text-ink-3 hover:text-ink" aria-label="Dismiss" @click="dismiss(t.id)"><X class="size-3.5" /></button>
        </output>
      </TransitionGroup>
    </div>
  </Teleport>
</template>
