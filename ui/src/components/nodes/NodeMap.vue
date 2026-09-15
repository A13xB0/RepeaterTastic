<script setup lang="ts">
// Leaflet map of node positions. Loaded lazily (own chunk) so Leaflet never lands in the main bundle.
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import L from 'leaflet'
import 'leaflet/dist/leaflet.css'
import type { MeshNode } from '@/api/types'

const props = defineProps<{ nodes: MeshNode[]; selected: string | null }>()
const emit = defineEmits<{ select: [id: string] }>()

const el = ref<HTMLElement | null>(null)
let map: L.Map | null = null
let layer: L.LayerGroup | null = null
let fitted = false
const markers = new Map<string, L.CircleMarker>()

function color(n: MeshNode) {
  const css = getComputedStyle(document.documentElement)
  if (n.local) return css.getPropertyValue('--brand').trim()
  if (n.via_mqtt) return css.getPropertyValue('--ink-3').trim()
  const h = n.hops_away ?? 3
  return css.getPropertyValue(h === 0 ? '--s1' : h === 1 ? '--s3' : h === 2 ? '--s4' : '--s5').trim()
}

function render() {
  if (!map || !layer) return
  const seen = new Set<string>()
  const surface = getComputedStyle(document.documentElement).getPropertyValue('--surface-solid').trim()
  for (const n of props.nodes) {
    if (!n.position) continue
    seen.add(n.node_id)
    const sel = n.node_id === props.selected
    const style: L.CircleMarkerOptions = { radius: sel ? 9 : n.local ? 7 : 6, color: sel ? getComputedStyle(document.documentElement).getPropertyValue('--ink').trim() : surface, weight: 2, fillColor: color(n), fillOpacity: 0.95 }
    let m = markers.get(n.node_id)
    if (m) {
      m.setLatLng([n.position.lat, n.position.lon]).setStyle(style)
    } else {
      m = L.circleMarker([n.position.lat, n.position.lon], style)
      m.bindTooltip(`<b>${escapeHtml(n.short_name)}</b> ${escapeHtml(n.long_name)}`, { direction: 'top', offset: [0, -6] })
      m.on('click', () => emit('select', n.node_id))
      m.addTo(layer)
      markers.set(n.node_id, m)
    }
    if (sel) m.bringToFront()
  }
  for (const [id, m] of markers) if (!seen.has(id)) { m.remove(); markers.delete(id) }
  if (!fitted && markers.size) {
    const b = L.latLngBounds([...markers.values()].map((m) => m.getLatLng()))
    map.fitBounds(b.pad(0.08), { maxZoom: 12 })
    fitted = true
  }
}

function escapeHtml(s: string) {
  return s.replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`)
}

onMounted(() => {
  if (!el.value) return
  map = L.map(el.value, { zoomControl: true, attributionControl: true, worldCopyJump: true }).setView([55.95, -3.19], 10)
  L.tileLayer('https://tile.openstreetmap.org/{z}/{x}/{y}.png', {
    maxZoom: 18,
    attribution: '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors',
  }).addTo(map)
  map.attributionControl.setPrefix('<a href="https://leafletjs.com">Leaflet</a>')
  layer = L.layerGroup().addTo(map)
  render()
  new ResizeObserver(() => map?.invalidateSize()).observe(el.value)
})

watch(() => [props.nodes, props.selected], render)
watch(
  () => props.selected,
  (id) => {
    const m = id ? markers.get(id) : undefined
    if (m && map && !map.getBounds().contains(m.getLatLng())) map.panTo(m.getLatLng())
  },
)
onBeforeUnmount(() => {
  map?.remove()
  map = null
})
</script>

<template>
  <div ref="el" class="rt-map size-full" />
</template>

<style>
.rt-map {
  background: var(--sunken);
  font: inherit;
}
.dark .rt-map .leaflet-tile-pane {
  filter: invert(1) hue-rotate(180deg) brightness(0.9) contrast(0.85) saturate(0.6);
}
.rt-map .leaflet-control-zoom a,
.rt-map .leaflet-control-attribution {
  background: var(--surface-solid);
  color: var(--ink-2);
  border-color: var(--line);
}
.rt-map .leaflet-control-attribution a {
  color: var(--brand);
}
.rt-map .leaflet-tooltip {
  background: var(--surface-solid);
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: 8px;
  box-shadow: var(--shadow);
  font-size: 12px;
}
.rt-map .leaflet-tooltip-top::before {
  border-top-color: var(--line);
}
</style>
