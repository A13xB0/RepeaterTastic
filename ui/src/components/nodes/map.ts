// A Leaflet map with the server's tile layer, styled for the GUI's themes.
import L from 'leaflet'
import 'leaflet/dist/leaflet.css'
import '@/components/nodes/map.css'
import { live } from '@/store/live'

export function baseMap(el: HTMLElement, center: L.LatLngExpression, zoom: number): L.Map {
  const map = L.map(el, { zoomControl: true, attributionControl: true, worldCopyJump: true }).setView(center, zoom)
  // web.map_tile_url on the server; the public OSM tiles when it isn't set
  const tiles = live.status?.map?.tile_url || 'https://{s}.basemaps.cartocdn.com/light_all/{z}/{x}/{y}{r}.png'
  L.tileLayer(tiles, {
    subdomains: 'abcd',
    maxZoom: 18,
    attribution:
      '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors' +
      (tiles.includes('cartocdn') ? ' &copy; <a href="https://carto.com/attributions">CARTO</a>' : ''),
  }).addTo(map)
  map.attributionControl.setPrefix('<a href="https://leafletjs.com">Leaflet</a>')
  return map
}
