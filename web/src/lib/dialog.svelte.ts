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
  | {
      kind: 'sheet'
      title: string
      // Functions are re-evaluated on every render, so a sheet that stays open shows
      // the current values (e.g. the volume) after each choice.
      subtitle?: string | (() => string)
      actions: SheetAction[] | (() => SheetAction[])
      keepOpen: boolean
    }

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

/** keepOpen: choosing an option does not close the sheet (for repeated adjustments). */
export function sheet(
  title: string,
  actions: SheetAction[] | (() => SheetAction[]),
  subtitle?: string | (() => string),
  opts: { keepOpen?: boolean } = {},
) {
  dialog.current = { kind: 'sheet', title, subtitle, actions, keepOpen: opts.keepOpen ?? false }
}

export function closeDialog(ok = false) {
  const d = dialog.current
  dialog.current = null
  if (d?.kind === 'confirm') d.resolve(ok)
}
