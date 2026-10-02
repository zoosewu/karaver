import { t } from './i18n'

// One dialog at a time, rendered by components/Dialogs.svelte (mounted in App).

export interface SheetAction {
  label: string
  run: () => void
  danger?: boolean
  disabled?: boolean
  hint?: string
}

type Dialog =
  | { kind: 'confirm'; message: string; confirmText: string; danger: boolean; resolve: (ok: boolean) => void }
  | { kind: 'sheet'; title: string; subtitle?: string; actions: SheetAction[] }

export const dialog = $state<{ current: Dialog | null }>({ current: null })

export function ask(message: string, opts: { confirmText?: string; danger?: boolean } = {}): Promise<boolean> {
  return new Promise((resolve) => {
    dialog.current = {
      kind: 'confirm',
      message,
      confirmText: opts.confirmText ?? t('common.confirm'),
      danger: opts.danger ?? false,
      resolve,
    }
  })
}

/** Two confirmations in a row, for actions that cannot be undone. */
export async function askTwice(message: string, second: string, confirmText?: string): Promise<boolean> {
  return (await ask(message, { danger: true, confirmText })) && (await ask(second, { danger: true, confirmText }))
}

export function sheet(title: string, actions: SheetAction[], subtitle?: string) {
  dialog.current = { kind: 'sheet', title, subtitle, actions }
}

export function closeDialog(ok = false) {
  const d = dialog.current
  dialog.current = null
  if (d?.kind === 'confirm') d.resolve(ok)
}
