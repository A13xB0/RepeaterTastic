<script setup lang="ts">
// Structure after openHop's Sidebar (MIT, © Lloyd Newton), rewritten for Meshtastic identities.
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import {
  Activity, Cable, ChartColumn, Layers, LayoutDashboard, LogOut, MapPinned, MessagesSquare, ScrollText, Settings2, UsersRound, X,
} from '@lucide/vue'
import Logo from '@/components/ui/Logo.vue'
import Sparkline from '@/components/charts/Sparkline.vue'
import CopyButton from '@/components/ui/CopyButton.vue'
import { live } from '@/store/live'
import { setToken } from '@/api/client'
import { num, uptime } from '@/lib/format'

defineProps<{ open: boolean }>()
const emit = defineEmits<{ close: [] }>()
const router = useRouter()

const unread = computed(() => live.identities.reduce((s, i) => s + (i.unread ?? 0), 0))
const nodeCount = computed(() => Object.values(live.nodes).filter((n) => !n.local).length)

const groups = computed(() => [
  {
    label: 'Mesh',
    items: [
      { to: '/', name: 'dashboard', label: 'Dashboard', icon: LayoutDashboard },
      { to: '/identities', name: 'identities', label: 'Identities', icon: UsersRound, count: live.identities.length || undefined },
      { to: '/chat', name: 'chat', label: 'Chat', icon: MessagesSquare, badge: unread.value || undefined },
      { to: '/channels', name: 'channels', label: 'Channels', icon: Layers },
      { to: '/nodes', name: 'nodes', label: 'Nodes & map', icon: MapPinned, count: nodeCount.value || undefined },
    ],
  },
  {
    label: 'Traffic',
    items: [
      { to: '/packets', name: 'packets', label: 'Packets', icon: Activity },
      { to: '/statistics', name: 'statistics', label: 'Statistics', icon: ChartColumn },
      { to: '/links', name: 'links', label: 'Links', icon: Cable },
    ],
  },
  {
    label: 'System',
    items: [
      { to: '/config', name: 'config', label: 'Configuration', icon: Settings2 },
      { to: '/logs', name: 'logs', label: 'Logs', icon: ScrollText },
    ],
  },
])

const noise = computed(() => [...live.noiseSeed, ...live.history.map((h) => h.noise)].slice(-60))

function logout() {
  setToken(null)
  router.push('/login')
}
</script>

<template>
  <Transition name="fade">
    <div v-if="open" class="fixed inset-0 z-[249] bg-black/30 backdrop-blur-[2px] lg:hidden" @click="emit('close')" />
  </Transition>
  <aside
    :class="[
      'fixed inset-y-0 left-0 z-[250] w-[272px] p-3 transition-transform duration-300 lg:static lg:z-auto lg:w-[264px] lg:shrink-0 lg:translate-x-0 lg:p-[14px]',
      open ? 'translate-x-0' : '-translate-x-full',
    ]"
  >
    <div class="card flex h-full flex-col overflow-hidden !bg-surface-solid lg:!bg-surface">
      <div class="flex items-center gap-3 px-4 pb-3 pt-4">
        <Logo :size="38" />
        <div class="min-w-0 flex-1 leading-tight">
          <div class="text-[15px] font-bold tracking-tight">Repeater<span class="text-brand">Tastic</span></div>
          <div class="text-2xs text-ink-3">Virtual Meshtastic nodes</div>
        </div>
        <button class="icon-btn lg:hidden" aria-label="Close menu" @click="emit('close')"><X class="size-4" /></button>
      </div>

      <div class="mx-3 rounded-xl border border-line-soft bg-raised/70 p-3">
        <div class="flex items-center justify-between gap-2">
          <span class="eyebrow">Relay persona</span>
          <span :class="['inline-flex items-center gap-1.5 text-2xs font-medium', live.connected ? 'text-ok' : 'text-ink-3']">
            <span :class="['dot', live.connected ? 'pulse-dot bg-ok text-ok' : 'bg-ink-3']" />{{ live.connected ? 'Live' : 'Offline' }}
          </span>
        </div>
        <template v-if="live.status">
          <div class="mt-1.5 truncate text-[13px] font-semibold">{{ live.status.relay.long_name }}</div>
          <div class="flex items-center gap-1 text-xs text-ink-3">
            <span class="mono">{{ live.status.relay.node_id }}</span>
            <CopyButton :text="live.status.relay.node_id" label="Node id" />
            <span class="ml-auto">up {{ uptime(live.status.uptime_s) }}</span>
          </div>
          <div class="mt-2 border-t border-line-soft pt-2">
            <div class="flex items-baseline justify-between text-2xs text-ink-3">
              <span>Noise floor</span>
              <span class="font-semibold tabular-nums text-ink">{{ num(live.status.radio.noise_floor_dbm, 0) }} dBm</span>
            </div>
            <Sparkline class="mt-1" :data="noise" :height="22" color="var(--info)" />
          </div>
        </template>
        <div v-else class="mt-2 h-16 animate-pulse rounded-lg bg-sunken" />
      </div>

      <nav class="mt-3 min-h-0 flex-1 overflow-y-auto px-3 pb-3">
        <div v-for="g in groups" :key="g.label" class="mb-3">
          <div class="eyebrow px-2.5 pb-1 pt-1">{{ g.label }}</div>
          <RouterLink
            v-for="item in g.items"
            :key="item.name"
            v-slot="{ href, navigate, isActive, isExactActive }"
            :to="item.to"
            custom
          >
            <a
              :href="href"
              :class="[
                'group relative flex h-9 items-center gap-2.5 rounded-xl px-2.5 text-[13px] font-medium transition-colors',
                (item.to === '/' ? isExactActive : isActive)
                  ? 'bg-brand/12 text-ink'
                  : 'text-ink-2 hover:bg-sunken hover:text-ink',
              ]"
              @click="navigate"
            >
              <span
                v-if="item.to === '/' ? isExactActive : isActive"
                class="absolute inset-y-2 left-0 w-[3px] rounded-full bg-brand"
              />
              <component
                :is="item.icon"
                :class="['size-[17px] shrink-0', (item.to === '/' ? isExactActive : isActive) ? 'text-brand' : 'text-ink-3 group-hover:text-ink-2']"
              />
              <span class="flex-1 truncate">{{ item.label }}</span>
              <span v-if="item.badge" class="badge">{{ item.badge }}</span>
              <span v-else-if="item.count" class="text-2xs tabular-nums text-ink-3">{{ item.count }}</span>
            </a>
          </RouterLink>
        </div>
      </nav>

      <div class="flex items-center gap-2 border-t border-line-soft px-4 py-3">
        <div class="min-w-0 flex-1 text-2xs leading-snug text-ink-3">
          <div>RepeaterTastic <span class="tabular-nums">v{{ (live.status?.version ?? '…').replace(/^v/, '') }}</span></div>
          <div>UI layout after openHop (MIT)</div>
        </div>
        <button class="btn btn-sm btn-ghost" title="Sign out" @click="logout"><LogOut class="size-4" />Sign out</button>
      </div>
    </div>
  </aside>
</template>
