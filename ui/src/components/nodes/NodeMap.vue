<script setup lang="ts">
// Leaflet map of node positions. Loaded lazily (own chunk) so Leaflet never lands in the main bundle.
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import L from 'leaflet'
import type { MeshNode } from '@/api/types'
import { baseMap } from '@/components/nodes/map'

const props = defineProps<{ nodes: MeshNode[]; selected: string | null }>()
const emit = defineEmits<{ select: [id: string] }>()

const el = ref<HTMLElement | null>(null)
let map: L.Map | null = null
let layer: L.LayerGroup | null = null
let fitted = false
const markers = new Map<string, L.CircleMarker>()

// Colour by hop distance, once local/MQTT nodes (which have their own colour) are ruled out.
const HOP_COLOR_VARS: Record<number, string> = { 0: '--s1', 1: '--s3', 2: '--s4' }
function hopColorVar(hops: number): string {
  return HOP_COLOR_VARS[hops] ?? '--s5'
}

function color(n: MeshNode) {
  const css = getComputedStyle(document.documentElement)
  if (n.local) return css.getPropertyValue('--brand').trim()
  if (n.via_mqtt) return css.getPropertyValue('--ink-3').trim()
  const h = n.hops_away ?? 3
  return css.getPropertyValue(hopColorVar(h)).trim()
}

function markerRadius(n: MeshNode, sel: boolean): number {
  if (sel) return 9
  return n.local ? 7 : 6
}

function markerStyle(n: MeshNode, sel: boolean, surface: string): L.CircleMarkerOptions {
  const ink = getComputedStyle(document.documentElement).getPropertyValue('--ink').trim()
  return { radius: markerRadius(n, sel), color: sel ? ink : surface, weight: 2, fillColor: color(n), fillOpacity: 0.95 }
}

/** Moves an existing marker or creates one for this node's current position. */
function upsertMarker(n: MeshNode, style: L.CircleMarkerOptions): L.CircleMarker {
  const existing = markers.get(n.node_id)
  if (existing) return existing.setLatLng([n.position!.lat, n.position!.lon]).setStyle(style)
  const m = L.circleMarker([n.position!.lat, n.position!.lon], style)
  m.bindTooltip(`<b>${escapeHtml(n.short_name)}</b> ${escapeHtml(n.long_name)}`, { direction: 'top', offset: [0, -6] })
  m.on('click', () => emit('select', n.node_id))
  m.addTo(layer!)
  markers.set(n.node_id, m)
  return m
}

/** Removes markers for nodes no longer in the current set. */
function pruneMarkers(seen: Set<string>) {
  for (const [id, m] of markers) {
    if (seen.has(id)) continue
    m.remove()
    markers.delete(id)
  }
}

/** Fits the map to every marker once, the first time positions arrive. */
function fitOnce() {
  if (fitted || !markers.size || !map) return
  const b = L.latLngBounds([...markers.values()].map((m) => m.getLatLng()))
  map.fitBounds(b.pad(0.08), { maxZoom: 12 })
  fitted = true
}

function render() {
  if (!map || !layer) return
  const seen = new Set<string>()
  const surface = getComputedStyle(document.documentElement).getPropertyValue('--surface-solid').trim()
  for (const n of props.nodes) {
    if (!n.position) continue
    seen.add(n.node_id)
    const sel = n.node_id === props.selected
    const m = upsertMarker(n, markerStyle(n, sel, surface))
    if (sel) m.bringToFront()
  }
  pruneMarkers(seen)
  fitOnce()
}

function escapeHtml(s: string) {
  return s.replace(/[&<>"']/g, (c) => `&#${c.codePointAt(0)};`)
}

onMounted(() => {
  if (!el.value) return
  map = baseMap(el.value, [55.95, -3.19], 10)
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
