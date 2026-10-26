import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { deleteForGood, listTrash, restoreFlow, type TrashedFlow } from './api'
import { FlowBuilderIcon, ObjectIcon, SubflowIcon, TrashIcon } from './icons'
import { formatTime } from './Runs'
import { useConfirm } from './Confirm'

// The trash (#/trash): the flows, procedures and structs deleted that one
// may change, the latest first, each saying when it went and by whom, and
// when the trash empties itself of it. While there, none runs, and the
// flows running a procedure there fail to. Restoring one brings it back as
// it was, schedules and runs included, under another name if a flow out of
// the trash took its own; deleting one for good, with its runs, asks first.
export default function Trash() {
  const [items, setItems] = useState<TrashedFlow[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [restored, setRestored] = useState<{ id: number; name: string } | null>(null)
  const [confirm, confirmDialog] = useConfirm()

  const report = useCallback((err: unknown) => setError(err instanceof Error ? err.message : String(err)), [])
  const refresh = useCallback(() => listTrash().then(setItems).catch(report), [report])

  useEffect(() => {
    document.title = 'Trash'
    refresh()
  }, [refresh])

  const onRestore = async (t: TrashedFlow, name?: string) => {
    setError(null)
    setRestored(null)
    try {
      const f = await restoreFlow(t.id, name)
      setRestored({ id: f.id, name: f.name })
      await refresh()
    } catch (err) {
      report(err)
      // Its name may have been taken since, which the list then says.
      await refresh()
    }
  }

  const onDelete = async (t: TrashedFlow) => {
    const runs = t.struct ? 'what makes up its object and its versions' : 'everything on its board, its versions and its runs'
    const used =
      t.usedBy === 0
        ? null
        : t.struct
          ? `The copies of its object in ${flowsLabel(t.usedBy)} stay theirs for good.`
          : `${capitalized(flowsLabel(t.usedBy))} running it will fail to run it for good.`
    const yes = await confirm({
      title: `Delete ${t.name} for good?`,
      message: (
        <>
          <p>It goes with {runs}. This can't be undone.</p>
          {used && <p>{used}</p>}
        </>
      ),
      confirm: 'Delete for good',
    })
    if (!yes) return
    setError(null)
    try {
      await deleteForGood(t.id)
      await refresh()
    } catch (err) {
      report(err)
    }
  }

  return (
    <div className="flows-page">
      <header className="toolbar">
        <h1>
          <a className="home" href="#/" title="All flows">
            <FlowBuilderIcon /> Flows
          </a>
          <span className="crumb-separator" aria-hidden>
            /
          </span>
          Trash
        </h1>
      </header>
      {error && (
        <div className="error-banner" role="alert" onClick={() => setError(null)}>
          {error} (click to dismiss)
        </div>
      )}
      {restored && (
        <div className="notice-banner" role="status">
          Restored <a href={`#/flows/${restored.id}`}>{restored.name}</a>.
        </div>
      )}
      <main className="flows">
        <section className="flow-section" aria-label="Trash">
          <h2>
            <TrashIcon /> Trash
          </h2>
          <p className="flows-note">
            The flows, procedures and structs deleted, which you may restore or delete for good; each goes for good on its
            own after a while. While here, none runs: their schedules wait, the API doesn't find them, and the flows running
            a procedure here fail to; the flows using a struct here have its copies as their own.
          </p>
          {items === null ? (
            <p className="flows-note">Loading…</p>
          ) : items.length === 0 ? (
            <p className="flows-note">The trash is empty.</p>
          ) : (
            <ul className="flow-list trash-list">
              {items.map((t) => (
                <TrashRow key={t.id} item={t} onRestore={(name) => onRestore(t, name)} onDelete={() => onDelete(t)} />
              ))}
            </ul>
          )}
        </section>
      </main>
      {confirmDialog}
    </div>
  )
}

function TrashRow({ item: t, onRestore, onDelete }: { item: TrashedFlow; onRestore: (name?: string) => Promise<void>; onDelete: () => void }) {
  // A name of its own to come back under, asked for once its own is taken.
  const [renaming, setRenaming] = useState(false)
  const [name, setName] = useState(t.name)
  const [busy, setBusy] = useState(false)
  const kind = t.struct ? 'struct' : t.module ? 'procedure' : 'flow'

  const restore = async (e?: FormEvent) => {
    e?.preventDefault()
    if (renaming && !name.trim()) return
    setBusy(true)
    try {
      await onRestore(renaming ? name.trim() : undefined)
    } finally {
      setBusy(false)
    }
  }

  return (
    <li>
      <div className="flow-card">
        <span className="flow-card-name">
          {t.struct && <ObjectIcon />}
          {t.module && <SubflowIcon />} {t.name}
        </span>
        <span className="flow-card-run">
          {capitalized(kind)} deleted {formatTime(t.deletedAt)}
          {t.deletedBy && ` by ${t.deletedBy}`} · goes for good {formatTime(t.goesAt)}
        </span>
        {t.usedBy > 0 && (
          <span className="trash-used">
            {t.struct
              ? `${capitalized(flowsLabel(t.usedBy))} using it ${t.usedBy === 1 ? 'has' : 'have'} its copies as ${t.usedBy === 1 ? 'its' : 'their'} own until it comes back.`
              : `${capitalized(flowsLabel(t.usedBy))} running it ${t.usedBy === 1 ? 'fails' : 'fail'} to run it until it comes back.`}
          </span>
        )}
        {t.nameTaken && !renaming && (
          <span className="trash-used">A flow out of the trash is named {t.name}, so it comes back under another name.</span>
        )}
        {renaming && (
          <form className="trash-rename" onSubmit={restore}>
            <input
              autoFocus
              aria-label={`The name ${t.name} comes back under`}
              value={name}
              onChange={(e) => setName(e.target.value)}
              onKeyDown={(e) => e.key === 'Escape' && setRenaming(false)}
            />
            <button type="submit" disabled={busy || !name.trim()}>
              Restore
            </button>
            <button type="button" onClick={() => setRenaming(false)}>
              Cancel
            </button>
          </form>
        )}
      </div>
      {!renaming && (
        <span className="trash-actions">
          <button type="button" disabled={busy} onClick={() => (t.nameTaken ? setRenaming(true) : void restore())}>
            {t.nameTaken ? 'Restore as…' : 'Restore'}
          </button>
          <button type="button" className="danger" disabled={busy} onClick={onDelete}>
            Delete for good
          </button>
        </span>
      )}
    </li>
  )
}

const flowsLabel = (n: number) => (n === 1 ? 'the flow' : `the ${n} flows`)

const capitalized = (s: string) => s.charAt(0).toUpperCase() + s.slice(1)
