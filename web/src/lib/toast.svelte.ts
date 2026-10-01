export interface Toast {
  id: number
  text: string
  kind: 'info' | 'error'
}

let nextId = 0
export const toasts = $state<Toast[]>([])

export function toast(text: string, kind: Toast['kind'] = 'info') {
  const id = nextId++
  toasts.push({ id, text, kind })
  setTimeout(() => {
    const i = toasts.findIndex((t) => t.id === id)
    if (i >= 0) toasts.splice(i, 1)
  }, 2600)
}
