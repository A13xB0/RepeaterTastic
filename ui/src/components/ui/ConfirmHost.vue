<script setup lang="ts">
import { TriangleAlert } from '@lucide/vue'
import Modal from './Modal.vue'
import { confirmState, settleConfirm } from '@/composables/confirm'
</script>

<template>
  <Modal :open="confirmState.open" size="sm" @close="settleConfirm(false)">
    <div class="flex gap-3 pt-1">
      <div
        :class="['flex size-9 shrink-0 items-center justify-center rounded-full', confirmState.opts.danger ? 'bg-bad/12 text-bad' : 'bg-brand/12 text-brand']"
      >
        <TriangleAlert class="size-[18px]" />
      </div>
      <div class="min-w-0">
        <h2 class="text-[15px] font-semibold tracking-tight">{{ confirmState.opts.title }}</h2>
        <p class="mt-1.5 text-[13px] leading-relaxed text-ink-2">{{ confirmState.opts.body }}</p>
      </div>
    </div>
    <template #footer>
      <button class="btn" @click="settleConfirm(false)">Cancel</button>
      <button :class="['btn', confirmState.opts.danger ? 'btn-danger' : 'btn-primary']" @click="settleConfirm(true)">
        {{ confirmState.opts.confirm ?? 'Confirm' }}
      </button>
    </template>
  </Modal>
</template>
