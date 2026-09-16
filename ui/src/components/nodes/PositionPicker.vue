<script setup lang="ts">
// Drop a pin: click the map (or drag the pin) to set a latitude and longitude. Loaded lazily with
// Leaflet, like the nodes map.
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import L from 'leaflet'
import { baseMap } from '@/components/nodes/map'
import { live } from '@/store/live'

const lat = defineModel<number>('latitude', { required: true })
const lon = defineModel<number>('longitude', { required: true })

const el = ref<HTMLElement | null>(null)
let map: L.Map | null = null
let pin: L.Marker | null = null

const round = (v: number) => Math.round(v * 1e6) / 1e6
const isSet = () => !!lat.value || !!lon.value
const icon = L.divIcon({ className: 'rt-pin', html: '<span></span>', iconSize: [22, 22], iconAnchor: [11, 22] })

function place(at: L.LatLng) {
  const w = at.wrap()
  lat.value = round(w.lat)
  lon.value = round(w.lng)
}

function sync() {
  if (!map) return
  if (!isSet()) {
    pin?.remove()
    pin = null
    return
  }
  const at = L.latLng(lat.value, lon.value)
  if (!pin) {
    pin = L.marker(at, { icon, draggable: true, keyboard: true, title: 'Site position' }).addTo(map)
    pin.on('dragend', () => pin && place(pin.getLatLng()))
  } else {
    pin.setLatLng(at)
  }
  if (!map.getBounds().contains(at)) map.panTo(at)
}

// With no position yet, start where the nodes this site has heard are.
function startView(): [L.LatLngExpression, number] {
  if (isSet()) return [[lat.value, lon.value], 14]
  const heard = Object.values(live.nodes).filter((n) => n.position).map((n) => [n.position!.lat, n.position!.lon] as [number, number])
  if (heard.length) return [L.latLngBounds(heard).getCenter(), 9]
  return [[55.95, -3.19], 6]
}

onMounted(() => {
  if (!el.value) return
  const [center, zoom] = startView()
  map = baseMap(el.value, center, zoom)
  map.on('click', (e: L.LeafletMouseEvent) => place(e.latlng))
  sync()
  new ResizeObserver(() => map?.invalidateSize()).observe(el.value)
})
watch([lat, lon], sync)
onBeforeUnmount(() => {
  map?.remove()
  map = null
})
</script>

<template>
  <div ref="el" class="rt-map size-full cursor-crosshair" role="application" aria-label="Map: click to drop the site's pin, or drag the pin" />
</template>
