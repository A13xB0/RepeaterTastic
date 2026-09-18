<script setup lang="ts">
// A dropdown with a tick box per option, for choosing several things (radios, ports, ...).
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { Check, ChevronDown } from '@lucide/vue'

const props = withDefaults(
  defineProps<{ options: { value: string; label: string; hint?: string }[]; id?: string; disabled?: boolean; emptyLabel?: string }>(),
  { emptyLabel: 'None' },
)
const model = defineModel<string[]>({ required: true })
const open = ref(false)
// A long list (the site's nodes, say) needs a way in other than scrolling.
const filter = ref('')
const searchable = computed(() => props.options.length > 12)
const shown = computed(() => {
  const q = filter.value.trim().toLowerCase()
  if (!q) return props.options
  return props.options.filter((o) => o.label.toLowerCase().includes(q) || o.hint?.toLowerCase().includes(q))
})
const search = ref<HTMLInputElement | null>(null)
const root = ref<HTMLElement | null>(null)
const menu = ref<HTMLElement | null>(null)
const trigger = ref<HTMLButtonElement | null>(null)
// The list is teleported to <body> and placed next to the button, so cards with overflow hidden
// or the bottom of the screen don't cut it off; it opens upwards when there's more room there.
const place = ref({ left: 0, width: 0, top: 0 as number | undefined, bottom: undefined as number | undefined, maxHeight: 288 })

function position() {
  const el = root.value
  if (!el) return
  const r = el.getBoundingClientRect()
  const gap = 4
  const below = window.innerHeight - r.bottom - gap - 8
  const above = r.top - gap - 8
  const want = Math.min(288, (shown.value.length + (model.value.length ? 1 : 0)) * 40 + 16 + (searchable.value ? 44 : 0))
  if (below >= want || below >= above) {
    place.value = { left: r.left, width: r.width, top: r.bottom + gap, bottom: undefined, maxHeight: Math.max(120, Math.min(288, below)) }
  } else {
    place.value = { left: r.left, width: r.width, top: undefined, bottom: window.innerHeight - r.top + gap, maxHeight: Math.max(120, Math.min(288, above)) }
  }
}

const summary = computed(() => {
  const chosen = props.options.filter((o) => model.value.includes(o.value))
  if (!chosen.length) return props.emptyLabel
  if (chosen.length === props.options.length && props.options.length > 1) return `All ${chosen.length}`
  // Past a few, the names stop being readable in one line and the count is what matters.
  if (chosen.length > 3) return `${chosen.length} chosen`
  return chosen.map((o) => o.label).join(', ')
})

function toggle(v: string) {
  model.value = model.value.includes(v) ? model.value.filter((x) => x !== v) : [...model.value, v]
  void nextTick(position)
}

function onPointer(e: PointerEvent) {
  const t = e.target as Node
  if (root.value?.contains(t) || menu.value?.contains(t)) return
  open.value = false
}
const optionButtons = () => Array.from(menu.value?.querySelectorAll<HTMLButtonElement>('li > button') ?? [])

// The list lives at the end of <body>, so keyboard focus is moved into it and back by hand.
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') {
    open.value = false
    trigger.value?.focus()
    return
  }
  const buttons = optionButtons()
  const at = buttons.indexOf(document.activeElement as HTMLButtonElement)
  if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
    e.preventDefault()
    if (at < 0 && document.activeElement === search.value) {
      if (e.key === 'ArrowDown') buttons[0]?.focus()
      return
    }
    const next = e.key === 'ArrowDown' ? Math.min(buttons.length - 1, at + 1) : Math.max(0, at - 1)
    if (e.key === 'ArrowUp' && at === 0 && search.value) search.value.focus()
    else buttons[next]?.focus()
  } else if (e.key === 'Tab' && at >= 0) {
    e.preventDefault()
    open.value = false
    trigger.value?.focus()
  }
}

function listen(on: boolean) {
  const doc = on ? document.addEventListener.bind(document) : document.removeEventListener.bind(document)
  const win = on ? window.addEventListener.bind(window) : window.removeEventListener.bind(window)
  doc('pointerdown', onPointer as EventListener)
  doc('keydown', onKey as EventListener)
  win('scroll', position, true)
  win('resize', position)
}

watch(open, async (o) => {
  listen(o)
  if (!o) return
  filter.value = ''
  position()
  await nextTick()
  position()
  if (searchable.value) search.value?.focus()
  else optionButtons()[0]?.focus()
})
watch(filter, () => void nextTick(position))
onBeforeUnmount(() => listen(false))
</script>

<template>
  <div ref="root" class="relative">
    <button
      :id="id"
      ref="trigger"
      type="button"
      class="input flex items-center gap-2 text-left"
      :disabled="disabled"
      :aria-expanded="open"
      aria-haspopup="listbox"
      @click="open = !open"
      @keydown.down.prevent="open = true"
    >
      <span :class="['min-w-0 flex-1 truncate', model.length ? '' : 'text-ink-3']">{{ summary }}</span>
      <ChevronDown :class="['size-4 shrink-0 text-ink-3 transition-transform', open && 'rotate-180']" />
    </button>
    <Teleport to="body">
    <div
      v-if="open"
      ref="menu"
      class="fixed z-[400] overflow-y-auto rounded-xl border border-line bg-surface-solid p-1 shadow-xl"
      :style="{ left: `${place.left}px`, width: `${place.width}px`, top: place.top !== undefined ? `${place.top}px` : undefined, bottom: place.bottom !== undefined ? `${place.bottom}px` : undefined, maxHeight: `${place.maxHeight}px` }"
    >
      <div v-if="searchable" class="sticky top-0 z-10 bg-surface-solid p-1 pb-1.5">
        <input
          ref="search"
          v-model="filter"
          type="search"
          class="input h-8 w-full text-[13px]"
          placeholder="Search"
          :aria-label="`Search ${options.length} options`"
          @keydown.stop.escape="open = false"
        />
      </div>
      <ul>
        <li v-for="o in shown" :key="o.value">
          <button
            type="button"
            :aria-pressed="model.includes(o.value)"
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
        </li>
      </ul>
      <div v-if="!options.length" class="px-2.5 py-2 text-[13px] text-ink-3">Nothing to choose from.</div>
      <div v-else-if="!shown.length" class="px-2.5 py-2 text-[13px] text-ink-3">Nothing matches “{{ filter }}”.</div>
      <div v-if="model.length" class="border-t border-line-soft px-1 pt-1">
        <button type="button" class="w-full rounded-lg px-2.5 py-1.5 text-left text-xs text-ink-3 hover:bg-sunken hover:text-ink" @click="model = []">
          Clear ({{ emptyLabel.toLowerCase() }})
        </button>
      </div>
    </div>
    </Teleport>
  </div>
</template>
