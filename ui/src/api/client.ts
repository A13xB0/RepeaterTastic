// Tiny fetch wrapper: base URL, bearer token, JSON in/out, {"error"} unwrapping and 401 handling.
import { ref } from 'vue'

export const API_BASE = (import.meta.env.VITE_API_BASE ?? '').replace(/\/$/, '') + '/api/v1'
const TOKEN_KEY = 'rt-token'

function readToken(): string | null {
  try {
    return localStorage.getItem(TOKEN_KEY)
  } catch {
    return null
  }
}

export const token = ref<string | null>(readToken())

/** The site's main radio: requests without ?radio= are about it. */
export const MAIN_RADIO = 'main'

/** Adds ?radio= to a path: a radio's id, or "all" for every radio of the site. */
export function withRadio(path: string, radio: string): string {
  if (!radio || radio === MAIN_RADIO) return path
  return path + (path.includes('?') ? '&' : '?') + 'radio=' + encodeURIComponent(radio)
}

export function setToken(t: string | null) {
  token.value = t
  try {
    if (t) localStorage.setItem(TOKEN_KEY, t)
    else localStorage.removeItem(TOKEN_KEY)
  } catch {
    /* storage unavailable: keep the session in memory only */
  }
}

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

let onUnauthorized: () => void = () => {}
export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn
}

type Method = 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'

export async function request<T>(method: Method, path: string, body?: unknown, opts: { auth?: boolean; raw?: boolean } = {}): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  if (opts.auth !== false && token.value) headers.Authorization = `Bearer ${token.value}`
  let res: Response
  try {
    res = await fetch(API_BASE + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) })
  } catch {
    throw new ApiError(0, 'Cannot reach the RepeaterTastic daemon')
  }
  if (res.status === 401 && opts.auth !== false) {
    setToken(null)
    onUnauthorized()
  }
  if (!res.ok) {
    let msg = `${res.status} ${res.statusText}`
    try {
      const j = await res.json()
      if (j && typeof j.error === 'string') msg = j.error
    } catch {
      /* not JSON */
    }
    throw new ApiError(res.status, msg)
  }
  if (opts.raw) return res as unknown as T
  if (res.status === 204) return undefined as T
  const text = await res.text()
  return (text ? JSON.parse(text) : undefined) as T
}

/** Upload a file as multipart form data (field name `field`). */
export async function upload<T>(path: string, field: string, file: File): Promise<T> {
  const form = new FormData()
  form.append(field, file)
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (token.value) headers.Authorization = `Bearer ${token.value}`
  let res: Response
  try {
    res = await fetch(API_BASE + path, { method: 'POST', headers, body: form })
  } catch {
    throw new ApiError(0, 'Cannot reach the RepeaterTastic daemon')
  }
  const text = await res.text()
  let body: unknown
  try {
    body = text ? JSON.parse(text) : undefined
  } catch {
    body = undefined
  }
  if (!res.ok) {
    const msg = body && typeof (body as { error?: unknown }).error === 'string' ? (body as { error: string }).error : `${res.status} ${res.statusText}`
    if (res.status === 401) {
      setToken(null)
      onUnauthorized()
    }
    throw new ApiError(res.status, msg)
  }
  return body as T
}

export const api = {
  get: <T>(p: string) => request<T>('GET', p),
  post: <T>(p: string, b?: unknown) => request<T>('POST', p, b ?? {}),
  put: <T>(p: string, b?: unknown) => request<T>('PUT', p, b ?? {}),
  patch: <T>(p: string, b?: unknown) => request<T>('PATCH', p, b ?? {}),
  del: <T = void>(p: string) => request<T>('DELETE', p),
}

/** Build a query string, skipping empty values. */
export function qs(params: Record<string, string | number | undefined | null | false>): string {
  const s = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== null && v !== '' && v !== false) s.set(k, String(v))
  const out = s.toString()
  return out ? `?${out}` : ''
}

export const enc = encodeURIComponent
