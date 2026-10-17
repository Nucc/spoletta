import { useEffect, useState } from 'react'
import { listSharedRecords, type SharedRecord } from './api'
import { formatTime } from './Runs'

// How often the list asks for records again, as flows write them.
const REFRESH_MS = 10_000

// The records of a shared struct's object, as every flow using it reads and
// writes them, the latest written first, each named by its key: the value of
// its first unique attribute.
export function SharedRecords({ struct, keyName }: { struct: number; keyName: string | null }) {
  const [records, setRecords] = useState<SharedRecord[] | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let live = true
    const load = () =>
      listSharedRecords(struct)
        .then((r) => live && (setRecords(r), setError(null)))
        .catch((err) => live && setError(String(err)))
    load()
    const timer = setInterval(load, REFRESH_MS)
    return () => {
      live = false
      clearInterval(timer)
    }
  }, [struct])

  if (error) return <p className="panel-warning">{error}</p>
  if (records === null) return null
  if (records.length === 0) return <p className="panel-note">No records yet. Flows using this struct write them as they run.</p>
  return (
    <ol className="run-list">
      {records.map((r) => (
        <li key={r.id} className="record-item" title={JSON.stringify(r.content, null, 2)}>
          <span className="run-item-title">
            <span className="record-key">{keyOf(r.content, keyName) ?? `Record ${r.id}`}</span>
            <span className="run-item-status">{r.state || 'no state'}</span>
          </span>
          <time dateTime={r.updated}>
            {formatTime(r.updated)}
            {r.run !== null && ` · run #${r.run}`}
          </time>
        </li>
      ))}
    </ol>
  )
}

function keyOf(content: unknown, keyName: string | null): string | null {
  if (keyName === null || typeof content !== 'object' || content === null) return null
  const v = (content as Record<string, unknown>)[keyName]
  if (v === undefined || v === null || v === '') return null
  return typeof v === 'string' ? v : JSON.stringify(v)
}
