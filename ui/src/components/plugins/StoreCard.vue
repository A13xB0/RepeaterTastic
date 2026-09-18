<script setup lang="ts">
// One plugin as the store offers it. What it will ask for is on the card, before Install, so the
// permissions aren't a surprise after the download.
import { computed, ref, watch } from 'vue'
import { ArrowUpCircle, Check, Container, Globe, Puzzle, Radio } from '@lucide/vue'
import type { StorePlugin } from '@/api/types'
import Spinner from '@/components/ui/Spinner.vue'
import { safeLink } from '@/lib/format'
import { transmits } from './PluginBits'

const props = defineProps<{ plugin: StorePlugin; permissions: Record<string, string>; busy?: boolean }>()
const emit = defineEmits<{ install: [plugin: StorePlugin] }>()

const p = computed(() => props.plugin)
const home = computed(() => safeLink(p.value.homepage))
// A permission list the store left out reads as null over the wire; without this the whole grid
// fails to render, not just this card.
const permissions = computed(() => p.value.permissions ?? [])
const logoFailed = ref(false)
watch(() => p.value.logo_url, () => (logoFailed.value = false))
const sends = computed(() => permissions.value.some(transmits))
// An attached plugin has a container rather than a bundle, so there is no version or size.
const release = computed(() => p.value.latest)
const size = computed(() => {
  const bytes = release.value?.size
  if (!bytes) return ''
  const mb = bytes / 1_000_000
  return mb >= 10 ? `${Math.round(mb)} MB` : `${mb.toFixed(1)} MB`
})
// An attached plugin runs in its own container, so there is nothing for this node to install.
const attachOnly = computed(() => !!p.value.image)
</script>

<template>
  <section class="card flex flex-col p-4 sm:p-5">
    <div class="flex items-start gap-3">
      <span class="flex size-10 shrink-0 items-center justify-center overflow-hidden rounded-xl border border-line-soft bg-raised text-ink-3">
        <img v-if="p.logo_url && !logoFailed" :src="p.logo_url" alt="" class="size-full object-contain p-1" @error="logoFailed = true" />
        <Puzzle v-else class="size-5" />
      </span>
      <div class="min-w-0 flex-1">
        <a v-if="home" :href="home" target="_blank" rel="noopener noreferrer" class="block truncate text-[14px] font-semibold hover:underline">{{ p.name }}</a>
        <span v-else class="block truncate text-[14px] font-semibold">{{ p.name }}</span>
        <div class="truncate text-xs text-ink-3">
          <template v-if="release">v{{ release.version }} · </template><template v-if="p.author">{{ p.author }}</template><template v-if="size"> · {{ size }}</template>
        </div>
      </div>
      <span v-if="p.update_available" class="chip bg-info/12 text-info"><ArrowUpCircle class="size-3" />update</span>
      <span v-else-if="p.installed" class="chip bg-ok/14 text-ok"><Check class="size-3" />installed</span>
    </div>

    <p class="mt-3 line-clamp-2 text-xs leading-relaxed text-ink-2">{{ p.summary }}</p>

    <div class="mt-3 flex flex-wrap gap-1.5">
      <span v-for="key in permissions" :key="key" class="chip bg-sunken text-ink-2" :title="props.permissions[key] ?? key">
        <Radio v-if="transmits(key)" class="size-3 text-warn" />{{ key }}
      </span>
      <span v-if="!permissions.length" class="chip bg-sunken text-ink-3">asks for nothing</span>
    </div>

    <p v-if="p.network?.length" class="mt-2 flex items-start gap-1.5 text-xs text-ink-3">
      <Globe class="mt-0.5 size-3.5 shrink-0" />Talks to {{ p.network.join(', ') }}
    </p>
    <p v-if="sends" class="mt-2 text-xs text-warn">Sends on the radio, within your send limits.</p>

    <p v-if="p.update_available" class="mt-3 text-xs text-ink-2">
      You have v{{ p.installed }}.<template v-if="release?.notes"> {{ release.notes }}</template>
    </p>

    <div class="mt-auto flex items-center justify-end gap-2 pt-4">
      <span v-if="attachOnly" class="mr-auto flex items-center gap-1.5 text-xs text-ink-3"><Container class="size-3.5" />runs in its own container</span>
      <span v-else-if="p.unusable" class="mr-auto text-xs text-ink-3">{{ p.unusable }}</span>
      <a v-if="attachOnly && home" class="btn btn-sm" :href="home" target="_blank" rel="noopener noreferrer">How to attach</a>
      <button v-else type="button" class="btn btn-sm" :class="p.update_available ? 'btn-primary' : ''" :disabled="busy || !!p.unusable || (!!p.installed && !p.update_available)" @click="emit('install', p)">
        <Spinner v-if="busy" />
        {{ p.update_available ? 'Update' : p.installed ? 'Installed' : 'Install' }}
      </button>
    </div>
  </section>
</template>
