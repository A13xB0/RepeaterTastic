<script setup lang="ts">
// Narrows a site-wide view to one radio. Hidden on a single-radio site, where there's nothing to pick.
import { computed } from 'vue'
import { live } from '@/store/live'

defineProps<{ id?: string }>()
/** "all", or a radio id. */
const model = defineModel<string>({ default: 'all' })
const several = computed(() => live.radios.length > 1)
</script>

<template>
  <select v-if="several" :id="id ?? 'radio-filter'" v-model="model" class="input !h-8 !w-auto !py-0 text-[13px]" aria-label="Radio">
    <option value="all">All radios</option>
    <option v-for="r in live.radios" :key="r.id" :value="r.id">{{ r.name }} · {{ r.phy.preset_name }}</option>
  </select>
</template>
