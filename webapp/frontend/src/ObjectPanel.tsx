import { useEffect, useRef, useState } from 'react'
import type { ObjectAttribute } from './api'
import { ExhibitionIcon, TrashIcon } from './icons'

const SAVE_DELAY_MS = 500

// Edits one object and the attributes it exhibits; mount with key={id} so
// switching objects resets the form. Names save when the field is left, so
// a half-typed one never takes another attribute's name; definitions save
// shortly after typing stops.
export default function ObjectPanel({
  name,
  attributes,
  persistent,
  many,
  editable,
  content,
  onPersistentChange,
  onManyChange,
  onRename,
  onAdd,
  onUpdate,
  onRemove,
  onClose,
}: {
  name: string
  attributes: ObjectAttribute[]
  persistent: boolean
  many: boolean
  // False while a run is in progress.
  editable: boolean
  // What the object held at the end of the latest run, if it was generated.
  content?: string
  onRename: (name: string) => void
  // Each resolves to whether the server took the change.
  onPersistentChange: (persistent: boolean) => Promise<boolean>
  onManyChange: (many: boolean) => Promise<boolean>
  onAdd: (name: string) => Promise<boolean>
  onUpdate: (id: number, patch: { name?: string; definition?: string; unique?: boolean }) => Promise<boolean>
  onRemove: (id: number) => Promise<boolean>
  onClose: () => void
}) {
  const [objectName, setObjectName] = useState(name)
  const [newName, setNewName] = useState('')

  // Saves the name unless it is empty or unchanged; also when the panel
  // closes, since a removed field may never blur.
  const committed = useRef(name)
  const commitObjectName = () => {
    const trimmed = objectName.trim()
    if (trimmed === '') setObjectName(committed.current)
    else if (trimmed !== committed.current) {
      committed.current = trimmed
      onRename(trimmed)
    }
  }
  const latestCommit = useRef(commitObjectName)
  latestCommit.current = commitObjectName
  useEffect(() => () => latestCommit.current(), [])

  const add = async () => {
    const trimmed = newName.trim()
    if (trimmed === '') return
    if (attributes.some((a) => a.name === trimmed)) return
    if (await onAdd(trimmed)) setNewName('')
  }
  const duplicate = attributes.some((a) => a.name === newName.trim())

  return (
    <aside className="panel" onKeyDown={(e) => e.key === 'Escape' && onClose()}>
      <header className="panel-header">
        <h2>Object</h2>
        <button className="panel-close" onClick={onClose} aria-label="Close panel">
          ×
        </button>
      </header>
      <label className="panel-field">
        <span>Name</span>
        <input
          value={objectName}
          onChange={(e) => setObjectName(e.target.value)}
          onBlur={commitObjectName}
          onKeyDown={(e) => e.key === 'Enter' && commitObjectName()}
        />
      </label>
      <label className="panel-check">
        <input type="checkbox" checked={many} disabled={!editable} onChange={(e) => onManyChange(e.target.checked)} />
        <span>List of items</span>
      </label>
      <p className="panel-note">
        An agent generating it replies with a list, and a flow process taking it runs once per item, each item as this
        object inside it. Stored, each item's run claims its item, so an item another run took is skipped.
      </p>
      <label className="panel-check">
        <input
          type="checkbox"
          checked={persistent}
          disabled={!editable}
          onChange={(e) => onPersistentChange(e.target.checked)}
        />
        <span>Store what runs generate</span>
      </label>
      <p className="panel-note">
        Each run that ends done keeps what it generated this object with. A unique attribute's value can be stored only
        once: a step generating a value another run stored terminates its run, so nothing is processed twice. Values
        are compared ignoring case and spacing; empty ones never collide.
      </p>
      <section className="attributes" aria-label="Attributes">
        <div className="attributes-title">
          <ExhibitionIcon size={14} />
          <span>Attributes</span>
        </div>
        <p className="panel-note">
          An agent that generates this object replies with a JSON object that has exactly these keys. The definitions tell
          it what each one holds. A reply that doesn't fit is sent back once, then the step fails.
        </p>
        {attributes.map((a) => (
          <AttributeRow
            key={a.id}
            attribute={a}
            editable={editable}
            persistent={persistent}
            taken={attributes.filter((o) => o.id !== a.id).map((o) => o.name)}
            onUpdate={(patch) => onUpdate(a.id, patch)}
            onRemove={() => onRemove(a.id)}
          />
        ))}
        <div className="attribute-add">
          <input
            value={newName}
            disabled={!editable}
            placeholder="New attribute, e.g. ticket_id"
            onChange={(e) => setNewName(e.target.value)}
            onKeyDown={(e) => e.key === 'Enter' && add()}
          />
          <button disabled={!editable || newName.trim() === '' || duplicate} onClick={add}>
            Add
          </button>
        </div>
        {duplicate && <small className="panel-warning">The object already has that attribute.</small>}
      </section>
      {attributes.length > 0 && (
        <div className="panel-run">
          <span>Agents reply with</span>
          <pre>{example(attributes)}</pre>
        </div>
      )}
      {content !== undefined && (
        <div className="panel-run">
          <span>Last run</span>
          <pre>{content || '(empty)'}</pre>
        </div>
      )}
    </aside>
  )
}

function AttributeRow({
  attribute,
  editable,
  persistent,
  taken,
  onUpdate,
  onRemove,
}: {
  attribute: ObjectAttribute
  editable: boolean
  // Unique only matters while the object is persistent.
  persistent: boolean
  // The names of the object's other attributes.
  taken: string[]
  onUpdate: (patch: { name?: string; definition?: string; unique?: boolean }) => Promise<boolean>
  onRemove: () => Promise<boolean>
}) {
  const [name, setName] = useState(attribute.name)
  const [definition, setDefinition] = useState(attribute.definition)
  const saved = useRef(attribute.definition)
  const timer = useRef<number>(undefined)
  const update = useRef(onUpdate)
  update.current = onUpdate
  // Set while the attribute is being deleted, when there's nothing left to save to.
  const removing = useRef(false)

  const flush = (value: string) => {
    window.clearTimeout(timer.current)
    if (removing.current || value === saved.current) return
    saved.current = value
    update.current({ definition: value })
  }

  useEffect(() => {
    if (definition === saved.current) return
    window.clearTimeout(timer.current)
    timer.current = window.setTimeout(() => flush(definition), SAVE_DELAY_MS)
  }, [definition])

  // Whatever is left saves when the panel closes.
  const latest = useRef(definition)
  latest.current = definition
  useEffect(() => () => flush(latest.current), [])

  const trimmed = name.trim()
  const clash = trimmed !== attribute.name && taken.includes(trimmed)
  const commitName = async () => {
    if (removing.current || trimmed === attribute.name) return
    if (trimmed === '' || clash || !(await onUpdate({ name: trimmed }))) setName(attribute.name)
  }
  const latestCommit = useRef(commitName)
  latestCommit.current = commitName
  useEffect(() => () => void latestCommit.current(), [])

  return (
    <div className="attribute">
      <div className="attribute-head">
        <input
          className="attribute-name"
          value={name}
          disabled={!editable}
          aria-label="Attribute name"
          onChange={(e) => setName(e.target.value)}
          onBlur={commitName}
          onKeyDown={(e) => e.key === 'Enter' && e.currentTarget.blur()}
        />
        <button
          className="attribute-remove"
          title="Delete attribute"
          aria-label={`Delete attribute ${attribute.name}`}
          disabled={!editable}
          onClick={async () => {
            removing.current = true
            if (!(await onRemove())) removing.current = false
          }}
        >
          <TrashIcon />
        </button>
      </div>
      {clash && <small className="panel-warning">Another attribute has that name.</small>}
      <label
        className="panel-check"
        title={persistent ? undefined : 'Store what runs generate to make an attribute unique'}
      >
        <input
          type="checkbox"
          checked={attribute.unique}
          disabled={!editable || !persistent}
          onChange={(e) => onUpdate({ unique: e.target.checked })}
        />
        <span>Unique</span>
      </label>
      <textarea
        value={definition}
        rows={2}
        disabled={!editable}
        aria-label={`Definition of ${attribute.name}`}
        placeholder="What it holds, e.g. the ticket's number as a string"
        onChange={(e) => setDefinition(e.target.value)}
        onBlur={() => flush(definition)}
      />
    </div>
  )
}

// What a reply looks like, keys in order, as the agent is shown it.
function example(attributes: ObjectAttribute[]): string {
  return `{\n${attributes.map((a) => `  ${JSON.stringify(a.name)}: …`).join(',\n')}\n}`
}
