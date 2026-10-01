import { storage } from './storage'
import type { Song } from './types'

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
  ) {
    super(code)
  }
}

const TOKEN_KEY = 'karaver.token'

export async function api<T = void>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {}
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  const token = storage.get(TOKEN_KEY)
  if (token) headers['Authorization'] = `Bearer ${token}`
  let res: Response
  try {
    res = await fetch(path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      credentials: 'same-origin',
    })
  } catch {
    throw new ApiError(0, 'network')
  }
  if (!res.ok) {
    let code = 'internal'
    try {
      code = (await res.json()).error ?? code
    } catch {
      /* not JSON */
    }
    throw new ApiError(res.status, code)
  }
  if (res.status === 204) return undefined as T
  return res.json()
}

export function errorCode(e: unknown): string {
  return e instanceof ApiError ? e.code : 'internal'
}

let session: Promise<string> | null = null

/** Returns the anonymous user id, creating an identity on first visit. */
export function ensureSession(): Promise<string> {
  session ??= api<{ userId: string; token: string }>('POST', '/api/session', {
    token: storage.get(TOKEN_KEY) ?? '',
  }).then((r) => {
    storage.set(TOKEN_KEY, r.token)
    return r.userId
  })
  session.catch(() => (session = null))
  return session
}

export function userToken(): string {
  return storage.get(TOKEN_KEY) ?? ''
}

export function searchSongs(q: string, offset: number) {
  const p = new URLSearchParams({ q, offset: String(offset), limit: '50' })
  return api<{ items: Song[]; more: boolean }>('GET', `/api/songs?${p}`)
}
