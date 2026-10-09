import { useEffect, useRef, useState } from 'react'
import type { FlowObject, ObjectAttribute, Relation } from './api'
import { Data } from './Data'
import { AggregationIcon, ExhibitionIcon, GeneralizationIcon, TrashIcon, ZoomInIcon } from './icons'
import { quantityText, type Quantity } from './quantity'
import QuantityInput from './QuantityInput'
import { fieldsOf, ownedBy, type Field } from './structure'

const SAVE_DELAY_MS = 500

// Edits one object and the attributes it exhibits; mount with key={id} so
// switching objects resets the form. Names save when the field is left, so
// a half-typed one never takes another attribute's name; definitions save
// shortly after typing stops. What else it is made of, the objects it owns,
// is edited on its own board, once unfolded.
export default function ObjectPanel({
  id,
  objects,
  name,
  attributes,
  persistent,
  quantity,
  human,
  editable,
  content,
  generals,
  onPersistentChange,
  onQuantityChange,
  onHumanChange,
  onRelationChange,
  onGeneralChange,
  onUnfold,
  onUnfoldPart,
  onRename,
  onAdd,
  onUpdate,
  onRemove,
  onClose,
}: {
  id: number
  // Every object of the flow, which tell what this one is made of.
  objects: FlowObject[]
  name: string
  attributes: ObjectAttribute[]
  persistent: boolean
  quantity: Quantity
  human: boolean
  // False while a run is in progress.
  editable: boolean
  // What the object held at the end of the latest run, if it was generated.
  content?: string
  // The objects it can specialize.
  generals: FlowObject[]
  onRename: (name: string) => void
  // Each resolves to whether the server took the change.
  onPersistentChange: (persistent: boolean) => Promise<boolean>
  onQuantityChange: (quantity: Quantity) => Promise<boolean>
  onHumanChange: (human: boolean) => Promise<boolean>
  onRelationChange: (relation: Relation) => Promise<boolean>
  onGeneralChange: (general: number | null) => Promise<boolean>
  // Unset for the object unfolded on the board shown.
  onUnfold?: () => void
  // Shows one of the objects it is made of unfolded.
  onUnfoldPart: (id: number) => void
  onAdd: (name: string) => Promise<boolean>
  onUpdate: (id: number, patch: { name?: string; definition?: string; unique?: boolean; file?: boolean }) => Promise<boolean>
  onRemove: (id: number) => Promise<boolean>
  onClose: () => void
}) {
  const [objectName, setObjectName] = useState(name)
  const [newName, setNewName] = useState('')
  const object = objects.find((o) => o.id === id)
  const owner = objects.find((o) => o.id === object?.owner)
  const general = object?.general ?? null
  const fields = fieldsOf(objects, id)
  const inherited = fields.filter((f) => f.inherited)
  const parts = ownedBy(objects, id)

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

  // Every field of the object needs a name of its own, its own attributes'
  // as much as those it inherits and the objects it is made of.
  const taken = (except?: number) => fields.filter((f) => f.attribute?.id !== except || except === undefined).map((f) => f.name)
  const add = async () => {
    const trimmed = newName.trim()
    if (trimmed === '') return
    if (taken().includes(trimmed)) return
    if (await onAdd(trimmed)) setNewName('')
  }
  const duplicate = taken().includes(newName.trim())

  return (
    <aside className="panel" onKeyDown={(e) => e.key === 'Escape' && onClose()}>
      <header className="panel-header">
        <h2>
          {human
            ? 'Person'
            : owner
              ? `${object?.relation === 'aggregation' ? 'Part' : 'Attribute'} of ${owner.name}`
              : 'Object'}
        </h2>
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
      {onUnfold && (
        <button className="panel-button" onClick={onUnfold}>
          <ZoomInIcon /> Unfold what it is made of
        </button>
      )}
      {owner ? (
        <>
          <div className="panel-field" role="radiogroup" aria-label={`How it makes up ${owner.name}`}>
            <span>How it makes up {owner.name}</span>
            <label className="panel-check">
              <input
                type="radio"
                name={`relation-${id}`}
                checked={object?.relation !== 'aggregation'}
                disabled={!editable}
                onChange={() => onRelationChange('exhibition')}
              />
              <ExhibitionIcon size={14} />
              <span>An attribute it exhibits</span>
            </label>
            <label className="panel-check">
              <input
                type="radio"
                name={`relation-${id}`}
                checked={object?.relation === 'aggregation'}
                disabled={!editable}
                onChange={() => onRelationChange('aggregation')}
              />
              <AggregationIcon size={14} />
              <span>A part it aggregates</span>
            </label>
          </div>
          <p className="panel-note">
            It is a field of what {owner.name} holds, named like it: a JSON object with its own fields, or any JSON value
            while it has none. Only how the board draws it tells an attribute from a part.
          </p>
        </>
      ) : (
        <>
          <label className="panel-check">
            <input type="checkbox" checked={human} disabled={!editable} onChange={(e) => onHumanChange(e.target.checked)} />
            <span>A person</span>
          </label>
          <p className="panel-note">
            A human object, such as a requester. Its link into a process is an agent link, ending in a filled circle: the
            person carries the process out, so it must be a human process. When the run reaches it, the person fills in
            the objects it generates without decision states, and the processes taking them go on with what they entered.
          </p>
        </>
      )}
      <label className="panel-field">
        <span>How many</span>
        <QuantityInput key={quantityText(quantity)} quantity={quantity} disabled={!editable} onChange={onQuantityChange} />
      </label>
      <p className="panel-note">
        As its links write it at its end: 1 for one object, or a list of items, such as exactly 4, 2..5, at least one as
        1..*, or * for any number.{' '}
        {owner
          ? `A list's field holds a JSON array, one item per ${name}, and what is handed in must have as many.`
          : `A process generating a list hands in a JSON array with as many items, or is asked again, and a flow process
        taking it runs once per item, each item as this object inside it; a list it collects from its items' runs has as
        many as ran. Stored, each item's run claims its item, so an item another run took is skipped.`}
      </p>
      {!owner && !human && (
        <>
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
            Each run that ends done keeps what it generated this object with. A unique attribute's value can be stored
            only once: a step generating a value another run stored terminates its run, so nothing is processed twice.
            Values are compared ignoring case and spacing; empty ones never collide.
          </p>
        </>
      )}
      {!human && (
        <>
          <label className="panel-field">
            <span className="field-label-icon">
              <GeneralizationIcon size={14} /> Specializes
            </span>
            <select
              value={general ?? ''}
              disabled={!editable}
              onChange={(e) => onGeneralChange(e.target.value === '' ? null : Number(e.target.value))}
            >
              <option value="">Nothing</option>
              {generals
                .filter((g) => g.id !== id)
                .map((g) => (
                  <option key={g.id} value={g.id}>
                    {objects.find((o) => o.id === g.owner) ? `${g.name} (of ${objects.find((o) => o.id === g.owner)!.name})` : g.name}
                  </option>
                ))}
            </select>
          </label>
          <p className="panel-note">
            Such as a car specializing a vehicle: it has every field of the object it specializes, first, and its own.
          </p>
        </>
      )}
      <section className="attributes" aria-label="Attributes">
        <div className="attributes-title">
          <ExhibitionIcon size={14} />
          <span>Attributes</span>
        </div>
        <p className="panel-note">
          A process generating this object hands in a JSON object with exactly its fields: what it inherits, these
          attributes and the objects it is made of. With none, it hands in data only. The definitions say what each
          holds. An agent's output is checked as it hands it in, and it fixes what doesn't fit; a script's that doesn't fit
          fails the step. A file attribute holds the path of a file the process wrote.
        </p>
        {inherited.length > 0 && (
          <ul className="inherited-fields" aria-label="Inherited fields">
            {inherited.map((f) => (
              <li key={`${f.object?.id ?? 'a'}-${f.attribute?.id ?? f.name}`} title={f.definition || undefined}>
                {f.name}
                <small> · from {objects.find((o) => o.id === (f.attribute?.object ?? f.object?.owner))?.name}</small>
              </li>
            ))}
          </ul>
        )}
        {attributes.map((a) => (
          <AttributeRow
            key={a.id}
            attribute={a}
            editable={editable}
            persistent={persistent && !owner}
            taken={taken(a.id)}
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
        {duplicate && <small className="panel-warning">The object already has a field of that name.</small>}
        {parts.length > 0 && (
          <>
            <div className="attributes-title">
              <AggregationIcon size={14} />
              <span>Made of</span>
            </div>
            <ul className="inherited-fields" aria-label="Made of">
              {parts.map((p) => (
                <li key={p.id}>
                  <button className="link-button" title={`Unfold ${p.name}`} onClick={() => onUnfoldPart(p.id)}>
                    {p.name}
                    {p.many && ` [${quantityText(p)}]`}
                  </button>
                  <small> · {p.relation === 'aggregation' ? 'part' : 'attribute'}</small>
                </li>
              ))}
            </ul>
          </>
        )}
        {onUnfold && (
          <small className="panel-note">Unfold it to add the objects it is made of: attributes with fields of their own, or parts.</small>
        )}
      </section>
      <div className="panel-run">
        <span>{owner ? `Its field in ${owner.name}` : 'Processes hand in'}</span>
        <pre>{example(fields, quantity.many)}</pre>
      </div>
      {content !== undefined && (
        <div className="panel-run">
          <span>Last run</span>
          <Data text={content || '(empty)'} />
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
  onUpdate: (patch: { name?: string; definition?: string; unique?: boolean; file?: boolean }) => Promise<boolean>
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
      <div className="attribute-flags">
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
        <label className="panel-check" title="Holds the path of a file the process wrote, which must exist">
          <input
            type="checkbox"
            checked={attribute.file}
            disabled={!editable}
            onChange={(e) => onUpdate({ file: e.target.checked })}
          />
          <span>File</span>
        </label>
      </div>
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

// What a process hands in, keys in order: {"data": …} without fields, an
// array of such objects for a list; the objects it is made of nest theirs.
function example(fields: Field[], many: boolean): string {
  const value = (fs: Field[], indent: string): string =>
    fs.length === 0
      ? '…'
      : `{\n${fs.map((f) => `${indent}  ${JSON.stringify(f.name)}: ${f.many ? `[${value(f.fields, indent + '  ')}]` : value(f.fields, indent + '  ')}`).join(',\n')}\n${indent}}`
  const object = (indent: string) => value(fields.length > 0 ? fields : [{ name: 'data', definition: '', file: false, many: false, min: 0, max: null, fields: [], inherited: false }], indent)
  return many ? `[\n  ${object('  ')}\n]` : object('')
}
