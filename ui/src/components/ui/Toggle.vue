<script setup lang="ts">
const model = defineModel<boolean>({ required: true })
defineProps<{ disabled?: boolean; label?: string }>()
</script>

<template>
  <button
    type="button"
    role="switch"
    :aria-checked="model"
    :aria-label="label"
    :disabled="disabled"
    :class="[
      'relative inline-flex h-5 w-9 shrink-0 items-center rounded-full border transition-colors disabled:opacity-50',
      model ? 'border-brand bg-brand' : 'border-line bg-sunken',
    ]"
    @click="model = !model"
  >
    <span
      :class="['inline-block size-3.5 rounded-full bg-white shadow transition-transform', model ? 'translate-x-[18px]' : 'translate-x-[2px]']"
    />
  </button>
</template>
