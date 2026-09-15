// Port of internal/phy (Resolve + AirtimeMs) so the mock's /phy/preview matches the daemon.
import type { Phy, Region } from '../src/api/types.ts'

interface Preset { bw: number; sf: number; cr: number; display: string }

export const PRESETS: Record<string, Preset> = {
  LONG_FAST: { bw: 250, sf: 11, cr: 5, display: 'LongFast' },
  LONG_MODERATE: { bw: 125, sf: 11, cr: 8, display: 'LongMod' },
  LONG_SLOW: { bw: 125, sf: 12, cr: 8, display: 'LongSlow' },
  LONG_TURBO: { bw: 500, sf: 11, cr: 8, display: 'LongTurbo' },
  MEDIUM_FAST: { bw: 250, sf: 9, cr: 5, display: 'MediumFast' },
  MEDIUM_SLOW: { bw: 250, sf: 10, cr: 5, display: 'MediumSlow' },
  MEDIUM_TURBO: { bw: 500, sf: 9, cr: 5, display: 'MediumTurbo' },
  SHORT_FAST: { bw: 250, sf: 7, cr: 5, display: 'ShortFast' },
  SHORT_SLOW: { bw: 250, sf: 8, cr: 5, display: 'ShortSlow' },
  SHORT_TURBO: { bw: 500, sf: 7, cr: 5, display: 'ShortTurbo' },
  LITE_FAST: { bw: 125, sf: 9, cr: 5, display: 'LiteFast' },
  LITE_SLOW: { bw: 125, sf: 10, cr: 5, display: 'LiteSlow' },
  NARROW_FAST: { bw: 62.5, sf: 7, cr: 6, display: 'NarrowFast' },
  NARROW_SLOW: { bw: 62.5, sf: 8, cr: 6, display: 'NarrowSlow' },
}

interface RegionInfo { start: number; end: number; duty: number; power: number; spacing?: number; padding?: number; override?: number }

const REGIONS: Record<string, RegionInfo> = {
  EU_868: { start: 869.4, end: 869.65, duty: 10, power: 27 },
  EU_866: { start: 865.6, end: 867.6, duty: 2.5, power: 27, spacing: 0.4, padding: 0.0375 },
  EU_N_868: { start: 869.4, end: 869.65, duty: 10, power: 27, padding: 0.0104, override: 1 },
  EU_433: { start: 433.0, end: 434.0, duty: 10, power: 10 },
  UA_433: { start: 433.0, end: 434.7, duty: 10, power: 10 },
  US: { start: 902.0, end: 928.0, duty: 100, power: 30 },
  ANZ: { start: 915.0, end: 928.0, duty: 100, power: 30 },
  ANZ_433: { start: 433.05, end: 434.79, duty: 100, power: 14 },
  NZ_865: { start: 864.0, end: 868.0, duty: 100, power: 36 },
  JP: { start: 920.5, end: 923.5, duty: 100, power: 13 },
  KR: { start: 920.0, end: 923.0, duty: 100, power: 23 },
  TW: { start: 920.0, end: 925.0, duty: 100, power: 27 },
  IN: { start: 865.0, end: 867.0, duty: 100, power: 30 },
  TH: { start: 920.0, end: 925.0, duty: 10, power: 27 },
  RU: { start: 868.7, end: 869.2, duty: 100, power: 20 },
  CN: { start: 470.0, end: 510.0, duty: 100, power: 19 },
  BR_902: { start: 902.0, end: 907.5, duty: 100, power: 30 },
  SG_923: { start: 917.0, end: 925.0, duty: 100, power: 20 },
  MY_919: { start: 919.0, end: 924.0, duty: 100, power: 27 },
}

export function regionList(): Region[] {
  return Object.entries(REGIONS).map(([name, r]) => ({
    name,
    presets: Object.keys(PRESETS),
    duty_cycle_pct: r.duty,
    power_limit_dbm: r.power,
  }))
}

function djb2(s: string): number {
  let h = 5381
  for (let i = 0; i < s.length; i++) h = (Math.imul(h, 33) + s.charCodeAt(i)) >>> 0
  return h
}

const f32 = Math.fround

export function resolvePhy(region: string, preset: string, primary = '', txPower = 0): Phy {
  const reg = REGIONS[region]
  const pp = PRESETS[preset]
  if (!reg) throw new Error(`unknown region "${region}"`)
  if (!pp) throw new Error(`unknown preset "${preset}"`)
  const spacing = reg.spacing ?? 0
  const padding = reg.padding ?? 0
  const slotWidth = f32(f32(spacing) + f32(2 * f32(padding)) + f32(pp.bw / 1000))
  const numSlots = Math.round(f32((f32(reg.end) - f32(reg.start) + f32(spacing)) / slotWidth))
  const name = primary || pp.display
  let slot: number
  if (reg.override && reg.override > 0) slot = reg.override - 1
  else if (numSlots === 0) slot = 0
  else slot = djb2(name) % numSlots
  const f = f32(f32(reg.start) + f32(pp.bw / 2000) + f32(padding) + f32(slot * slotWidth))
  const power = txPower <= 0 || txPower > reg.power ? reg.power : txPower
  return {
    region, preset, preset_name: pp.display,
    frequency_mhz: Math.round(f * 1e4) / 1e4,
    bw_khz: pp.bw, sf: pp.sf, cr: pp.cr, slot, num_slots: numSlots,
    sync_word: 0x2b, preamble: 16, tx_power_dbm: power, primary_channel: name,
  }
}

export function airtimeMs(len: number, sf = 11, bw = 250, cr = 5, preamble = 16): number {
  const tSym = (1 << sf) / bw
  const de = tSym >= 16 ? 1 : 0
  const nPre = preamble + 4.25
  const num = 8 * len + 16 - 4 * sf + 28
  const den = 4 * (sf - 2 * de)
  const nPayload = 8 + Math.max(Math.ceil(num / den) * cr, 0)
  return Math.round((nPre + nPayload) * tSym)
}
