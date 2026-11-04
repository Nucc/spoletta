import { useEffect, useId, useRef, useState } from 'react'
import { listTaskStructs, type AttributePatch, type AttributeType, type FlowObject, type ObjectAttribute, type ObjectFollow, type ObjectPick, type ObjectState, type Relation, type TaskStruct } from './api'
import { Data } from './Data'
import { AggregationIcon, ExhibitionIcon, GeneralizationIcon, TableIcon, TrashIcon, ZoomInIcon } from './icons'
import { pickText } from './ObjectNode'
import { quantityText, type Quantity } from './quantity'
import QuantityInput from './QuantityInput'
import { fieldsOf, ownedBy, type Field } from './structure'
import { openTab } from './tabs'
import { Hint, useHelp } from './Hints'

const SAVE_DELAY_MS = 500

// Edits one object and the attributes it exhibits; mount with key={id} so
// switching objects resets the form. Each attribute is a line, opened to edit
// it, so the object's shape reads at a glance however many it has; one opens
// at a time, and one just added opens. What else it is made of, the objects
// it owns, is edited on its own board, once unfolded.
export default function ObjectPanel({
  id,
  flow,
  objects,
  name,
  attributes,
  states,
  persistent,
  shared,
  pick,
  follows,
  quantity,
  human,
  editable,
  module,
  structRoot = false,
  jobFlow = false,
  content,
  run,
  generals,
  onPersistentChange,
  onSharedChange,
  onPickChange,
  onFollowsChange,
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
  onStateRule,
  onClose,
}: {
  id: number
  // The flow whose board it is on.
  flow: number
  // Every object of the flow, which tell what this one is made of.
  objects: FlowObject[]
  name: string
  attributes: ObjectAttribute[]
  states: ObjectState[]
  persistent: boolean
  // Every flow using a struct's object reads and writes one store of its
  // records; see shared.go.
  shared: boolean
  // For a shared struct's copy on the top board: the record each run works
  // on; see picks.go.
  pick: ObjectPick | null
  // For a shared struct's copy on the top board: the reference whose record
  // it stands for in each run; see references.go.
  follows: ObjectFollow | null
  quantity: Quantity
  human: boolean
  // False while a run is in progress.
  editable: boolean
  // For a copy of a module's object, the module: what the object has
  // changes only there, and here with it.
  module?: { id: number; name: string; struct: boolean }
  // The object a struct is: whether it is a person or stored is up to each copy.
  structRoot?: boolean
  // Whether the board is a job flow's, not a procedure's or a struct's.
  jobFlow?: boolean
  // What the object held at the end of the run the board shows, if it was
  // generated there, and which run that is.
  content?: string
  run?: number
  // The objects it can specialize.
  generals: FlowObject[]
  onRename: (name: string) => void
  // Each resolves to whether the server took the change.
  onPersistentChange: (persistent: boolean) => Promise<boolean>
  onSharedChange: (shared: boolean) => Promise<boolean>
  onPickChange: (pick: ObjectPick | null) => Promise<boolean>
  onFollowsChange: (follows: ObjectFollow | null) => Promise<boolean>
  onQuantityChange: (quantity: Quantity) => Promise<boolean>
  onHumanChange: (human: boolean) => Promise<boolean>
  onRelationChange: (relation: Relation) => Promise<boolean>
  onGeneralChange: (general: number | null) => Promise<boolean>
  // Unset for the object unfolded on the board shown.
  onUnfold?: () => void
  // Shows one of the objects it is made of unfolded.
  onUnfoldPart: (id: number) => void
  // Resolves to the new attribute's id, or null if the server refused it.
  onAdd: (name: string) => Promise<number | null>
  onUpdate: (id: number, patch: AttributePatch) => Promise<boolean>
  onRemove: (id: number) => Promise<boolean>
  // Sets a state's rule; resolves to whether the server took it.
  onStateRule: (id: number, rule: string) => Promise<boolean>
  onClose: () => void
}) {
  // Where it goes, and whether this flow stores it, are a copy's own.
  const own = editable && !module
  const [objectName, setObjectName] = useState(name)
  const [newName, setNewName] = useState('')
  const object = objects.find((o) => o.id === id)
  const owner = objects.find((o) => o.id === object?.owner)
  // A struct's copy on a board is as many as its flow says, whatever the
  // struct is, so one flow adds a list of records while another works on
  // one; the objects making it up follow the struct. See syncCopy.
  const structCopy = !!module?.struct && !owner
  const ownQuantity = own || (editable && structCopy)
  const general = object?.general ?? null
  const fields = fieldsOf(objects, id)
  const inherited = fields.filter((f) => f.inherited)
  const parts = ownedBy(objects, id)
  const help = useHelp()
  // The references whose record it can stand for: an attribute referring
  // to its struct's records, of another object on the top board picking a
  // record.
  // Or of one runs are given as they start: a plain object on the top
  // board, such as the ticket key a person enters.
  const references = shared && object?.source
    ? objects
        .filter((o) => o.id !== id && o.parent === null && o.owner === null && (o.pick || (!o.shared && !o.human && !o.many)))
        .flatMap((o) => o.attributes.filter((a) => a.ref === object.source).map((a) => ({ object: o, attribute: a.name })))
    : []
  // Whether its attributes may refer to a shared struct's records: a
  // struct's own object, or a plain one on a job flow's top board, whose
  // reference a run given it can follow.
  const refers = structRoot || (jobFlow && !owner && !human && !shared && object?.parent === null && !quantity.many)
  // The shared structs an attribute may refer to, which also name the one
  // an attribute refers to on a copy of a struct, where it can't be changed.
  const named = refers || attributes.some((a) => a.ref)
  const [structs, setStructs] = useState<TaskStruct[]>([])
  useEffect(() => {
    if (!named) return
    let live = true
    listTaskStructs().then((ss) => live && setStructs(ss), () => {})
    return () => {
      live = false
    }
  }, [named])
  // Unique only matters while the object is stored: persistent, or a
  // struct's, whose records may be shared.
  const stored = (persistent || structRoot || shared) && !owner
  // The attribute opened to edit, if any.
  const [openId, setOpenId] = useState<number | null>(null)

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
    // It opens, to be defined, while the focus stays here for the next.
    const added = await onAdd(trimmed)
    if (added === null) return
    setNewName('')
    setOpenId(added)
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
      {editable && !module && (
        <Hint id={human ? 'panel-person' : 'panel-object'} className="panel-hint">
          {human
            ? 'A person, named as the role whose holders decide the reviews of the human processes taking it.'
            : 'What it holds, its attributes, which agents generating it reply with as JSON, and the states it can be in.'}
        </Hint>
      )}
      {module && (
        <p className="panel-note module-note">
          A copy of {module.struct ? 'the struct' : 'an object of the procedure'}{' '}
          <a
            href={`#/flows/${module.id}`}
            // It opens in a new tab, leaving this flow where it is.
            onClick={(e) => {
              e.preventDefault()
              openTab(`#/flows/${module.id}`)
            }}
          >
            {module.name}
          </a>
          , which it follows: its
          name, how many it is, its states, attributes and what it is made of change only there, and here with it.
        </p>
      )}
      <label className="panel-field">
        <span>Name</span>
        <input
          value={objectName}
          disabled={!own}
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
            <span className="help-head">
              How it makes up {owner.name}
              {help.button('relation', `how it makes up ${owner.name}`)}
            </span>
            <label className="panel-check">
              <input
                type="radio"
                name={`relation-${id}`}
                checked={object?.relation !== 'aggregation'}
                disabled={!own}
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
                disabled={!own}
                onChange={() => onRelationChange('aggregation')}
              />
              <AggregationIcon size={14} />
              <span>A part it aggregates</span>
            </label>
          </div>
          {help.note(
            'relation',
            <>
              It is a field of what {owner.name} holds, named like it: a JSON object with its own fields, or any JSON value
              while it has none. Only how the board draws it tells an attribute from a part.
            </>,
          )}
        </>
      ) : structRoot ? null : (
        <>
          <div className="help-head">
            <label className="panel-check">
              <input type="checkbox" checked={human} disabled={!own} onChange={(e) => onHumanChange(e.target.checked)} />
              <span>A person</span>
            </label>
            {help.button('human', 'A person')}
          </div>
          {help.note(
            'human',
            <>
              A human object, such as a requester. Its link into a process is an agent link, ending in a filled circle: the
              person carries the process out, so it must be a human process. When the run reaches it, the person fills in
              the objects it generates without decision states, and the processes taking them go on with what they entered.
            </>,
          )}
        </>
      )}
      <div className="panel-field">
        <span className="help-head">
          <label htmlFor={`object-${id}-quantity`}>How many</label>
          {help.button('quantity', 'How many')}
        </span>
        <QuantityInput
          key={quantityText(quantity)}
          id={`object-${id}-quantity`}
          quantity={quantity}
          disabled={!ownQuantity}
          onChange={onQuantityChange}
        />
      </div>
      {help.note(
        'quantity',
        <>
          As its links write it at its end: 1 for one object, or a list of items, such as exactly 4, 2..5, at least one as
          1..*, or * for any number.{' '}
          {owner
            ? `A list's field holds a JSON array, one item per ${name}, and what is handed in must have as many.`
            : `A process generating a list hands in a JSON array with as many items, or is asked again, and a flow process
          taking it runs once per item, each item as this object inside it; a list it collects from its items' runs has as
          many as ran. Stored, each item's run claims its item, so an item another run took is skipped.`}
          {structRoot &&
            ` A copy added to a flow starts as many as this; each flow then says how many its copy is, so one may
          generate a list while another works on one.`}
          {structCopy &&
            ` It is this flow's own, whatever ${module?.name} and its other copies are, so a flow adding records may
          generate a list while another works on one at a time.`}
          {structCopy && shared && ' Only one, not a list, picks the record each run works on.'}
        </>,
      )}
      {structRoot && (
        <>
          <div className="help-head">
            <label className="panel-check">
              <input
                type="checkbox"
                checked={shared}
                disabled={!editable}
                onChange={(e) => onSharedChange(e.target.checked)}
              />
              <span>Share its records with every flow using it</span>
            </label>
            {help.button('shared', 'sharing its records')}
          </div>
          {help.note(
            'shared',
            <>
              Every copy of {name}, in whatever flow, reads and writes the same records, told apart by its unique
              attributes, so mark one unique first. A process only generating it adds new records and refreshes the
              fields of known ones, keeping their state; one that takes or queries it too moves the records it generates
              into the state it comes out in. Which records get worked on, or worked on again, is up to the processes'
              queries, which claim none, so runs querying the same state at once may take the same one; or to a copy
              picking the record each run works on, which its run holds until it ends.
            </>,
          )}
          {shared && (
            <a className="link-button records-table-link" href={`#/flows/${flow}/records`}>
              <TableIcon /> Show its records
            </a>
          )}
        </>
      )}
      {!owner && !human && !structRoot && shared && (
        <p className="panel-note">
          Shared by {module?.name ?? 'its struct'}: every flow using it reads and writes the same records. A process
          taking or querying it moves the records it generates into the state it comes out in; one only generating it
          adds and refreshes records, keeping their state.
        </p>
      )}
      {!owner && !human && !structRoot && shared && module && (
        <a className="link-button records-table-link" href={`#/flows/${module.id}/records`}>
          <TableIcon /> Show its records
        </a>
      )}
      {!owner && !human && !structRoot && shared && object?.parent === null && !quantity.many && (
        <>
          <div className="help-head">
            <label className="panel-check">
              <input
                type="checkbox"
                checked={pick !== null}
                disabled={!editable}
                onChange={(e) => onPickChange(e.target.checked ? { state: states[0]?.name, oldest: true } : null)}
              />
              <span>Each run works on one of its records</span>
            </label>
            {help.button('pick', 'the record each run works on')}
          </div>
          {help.note(
            'pick',
            <>
              As each run starts it takes one {name} record: the one it was started on, from the Records page or a task
              board, else {pick ? pickText(pick) : 'the oldest or latest in a state'} that no other run holds. Every
              process linked to {name} gets that record as it was then, so they all work on the same one, and the run
              holds it until it ends, so no other run takes it meanwhile. With none free, no run starts. A process taking{' '}
              {name} and generating it changes that record, only the fields it changes; if someone else moved it on
              meanwhile, the step fails rather than moving it back. To add new records, generate another copy of the
              struct.
            </>,
          )}
          {pick && <PickFields pick={pick} states={states} editable={editable} onChange={onPickChange} />}
        </>
      )}
      {!owner && !human && !structRoot && shared && object?.parent === null && !quantity.many && !pick && (references.length > 0 || follows) && (
        <>
          <div className="help-head">
            <label className="panel-field-label" htmlFor={`follows-${id}`}>
              Each run works on the record a reference names
            </label>
            {help.button('follows', 'following a reference')}
          </div>
          {help.note(
            'follows',
            <>
              As each run starts and takes the record another object picks, or is given what another object holds
              (Run with inputs, the API, a webhook), it takes the {name} record that reference names too, such as
              the ticket a pull request is for, or the one whose key a person entered. Every process linked to {name} gets it,
              and a process taking and generating {name} changes it, failing if someone else moved it on meanwhile. Other
              runs may follow the same record at once. A record naming none starts no run.
            </>,
          )}
          <select
            id={`follows-${id}`}
            value={follows ? `${follows.object}:${follows.attribute}` : ''}
            disabled={!editable}
            onChange={(e) => {
              const [o, ...a] = e.target.value.split(':')
              void onFollowsChange(e.target.value === '' ? null : { object: Number(o), attribute: a.join(':') })
            }}
          >
            <option value="">None</option>
            {references.map((r) => (
              <option key={`${r.object.id}:${r.attribute}`} value={`${r.object.id}:${r.attribute}`}>
                {r.object.name} · {r.attribute}
              </option>
            ))}
            {follows && !references.some((r) => r.object.id === follows.object && r.attribute === follows.attribute) && (
              <option value={`${follows.object}:${follows.attribute}`}>
                {objects.find((o) => o.id === follows.object)?.name ?? 'Gone'} · {follows.attribute} (no longer a reference)
              </option>
            )}
          </select>
        </>
      )}
      {!owner && !human && !structRoot && !shared && (
        <>
          <div className="help-head">
            <label className="panel-check">
              <input
                type="checkbox"
                checked={persistent}
                disabled={!editable}
                onChange={(e) => onPersistentChange(e.target.checked)}
              />
              <span>Store what runs generate</span>
            </label>
            {help.button('persistent', 'storing what runs generate')}
          </div>
          {help.note(
            'persistent',
            <>
              Each run that ends done keeps what it generated this object with. A unique attribute's value can be stored
              only once: a step generating a value another run stored terminates its run, so nothing is processed twice.
              Values are compared ignoring case and spacing; empty ones never collide.
            </>,
          )}
          {persistent && (
            <a
              className="link-button records-table-link"
              href={`#/flows/${flow}/data?q=${encodeURIComponent('status:done')}`}
              title="What this flow's runs that ended done stored, a run per row"
            >
              <TableIcon /> Show what runs stored
            </a>
          )}
        </>
      )}
      {!human && (
        <>
          <div className="panel-field">
            <span className="help-head">
              <label className="field-label-icon" htmlFor={`object-${id}-general`}>
                <GeneralizationIcon size={14} /> Specializes
              </label>
              {help.button('general', 'Specializes')}
            </span>
            <select
              id={`object-${id}-general`}
              value={general ?? ''}
              disabled={!own}
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
          </div>
          {help.note(
            'general',
            <>Such as a car specializing a vehicle: it has every field of the object it specializes, first, and its own.</>,
          )}
        </>
      )}
      <section className="attributes" aria-label="Attributes">
        <div className="attributes-title">
          <ExhibitionIcon size={14} />
          <span>Attributes</span>
          {attributes.length > 0 && <span className="attributes-count">{attributes.length}</span>}
          {help.button('attributes', 'Attributes')}
        </div>
        {help.note(
          'attributes',
          <>
            A process generating this object hands in a JSON object with exactly its fields: what it inherits, these
            attributes and the objects it is made of. With none, it hands in data only. The definitions say what each
            holds. An agent's output is checked as it hands it in, and it fixes what doesn't fit; a script's that doesn't
            fit fails the step. A file attribute holds the path of a file the process wrote.
          </>,
        )}
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
        {attributes.length > 0 && (
          <div className="attribute-list">
            {attributes.map((a) => (
              <AttributeRow
                key={a.id}
                attribute={a}
                open={openId === a.id}
                onToggle={() => setOpenId((o) => (o === a.id ? null : a.id))}
                editable={own}
                stored={stored}
                taken={taken(a.id)}
                refers={refers}
                structs={structs}
                onUpdate={(patch) => onUpdate(a.id, patch)}
                onRemove={() => onRemove(a.id)}
              />
            ))}
          </div>
        )}
        <div className="attribute-add">
          <input
            value={newName}
            disabled={!own}
            placeholder="New attribute, e.g. ticket_id"
            onChange={(e) => setNewName(e.target.value)}
            onKeyDown={(e) => e.key === 'Enter' && add()}
          />
          <button disabled={!own || newName.trim() === '' || duplicate} onClick={add}>
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
      {states.length > 0 && (
        <section className="attributes" aria-label="State rules">
          <div className="attributes-title">
            <span>State rules</span>
            {help.button('rules', 'State rules')}
          </div>
          {help.note(
            'rules',
            <>
              Generated in no state, the object comes out in the first state whose rule holds of what it holds, such as{' '}
              <code>confidence &gt;= 0.8</code> or <code>kind == "bug" and labels contains "p1"</code>: comparisons of its
              fields, by name, with ==, !=, &lt;, &lt;=, &gt;, &gt;= or contains, joined by and. An object without
              attributes holds its value in <code>data</code>.
            </>,
          )}
          {states.map((s) => (
            <StateRuleRow key={s.id} state={s} editable={own} onSave={(rule) => onStateRule(s.id, rule)} />
          ))}
        </section>
      )}
      <div className="panel-run">
        <span>{owner ? `Its field in ${owner.name}` : 'Processes hand in'}</span>
        <pre>{example(fields, quantity.many)}</pre>
      </div>
      {content !== undefined && (
        <div className="panel-run">
          <Data label={run === undefined ? 'Last run' : `In run #${run}`} text={content || '(empty)'} />
        </div>
      )}
    </aside>
  )
}

// A state's rule, saved when the field is left or Enter is pressed; one the
// server refuses goes back to what it was.
function StateRuleRow({ state, editable, onSave }: { state: ObjectState; editable: boolean; onSave: (rule: string) => Promise<boolean> }) {
  const [rule, setRule] = useState(state.rule ?? '')
  const saved = useRef(state.rule ?? '')
  const save = async () => {
    const trimmed = rule.trim()
    if (trimmed === saved.current) return
    if (await onSave(trimmed)) saved.current = trimmed
    else setRule(saved.current)
  }
  return (
    <label className="panel-field">
      <span>{state.name}</span>
      <input
        value={rule}
        readOnly={!editable}
        placeholder="No rule; set by a process"
        spellCheck={false}
        onChange={(e) => setRule(e.target.value)}
        onBlur={save}
        onKeyDown={(e) => e.key === 'Enter' && save()}
      />
    </label>
  )
}

// An attribute folded to a line: its name, what it is set to beyond holding
// a plain value, and its definition's start; opened, it edits them. It stays
// mounted folded, so its line shows what was just typed. Names save when the
// field is left, so a half-typed one never takes another attribute's name;
// definitions save shortly after typing stops, and both when Escape folds it
// or the panel closes.
function AttributeRow({
  attribute,
  open,
  onToggle,
  editable,
  stored,
  taken,
  refers,
  structs,
  onUpdate,
  onRemove,
}: {
  attribute: ObjectAttribute
  open: boolean
  // Opens it, or folds it while open.
  onToggle: () => void
  editable: boolean
  // Whether the object is stored, which unique needs.
  stored: boolean
  // The names of the object's other attributes.
  taken: string[]
  // Whether it may refer to a shared struct's records; see references.go.
  refers: boolean
  // The shared structs the person sees, which name what it refers to.
  structs: TaskStruct[]
  onUpdate: (patch: AttributePatch) => Promise<boolean>
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
  const help = useHelp()
  const editor = useId()
  const summary = useRef<HTMLButtonElement>(null)

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

  // A text's pattern, saved as the name is; one the server refuses, such as
  // one that isn't a regular expression, goes back to what it was.
  const [pattern, setPattern] = useState(attribute.pattern ?? '')
  const savedPattern = useRef(attribute.pattern ?? '')
  useEffect(() => {
    savedPattern.current = attribute.pattern ?? ''
    setPattern(savedPattern.current)
  }, [attribute.pattern])
  const commitPattern = async () => {
    const p = pattern.trim()
    if (removing.current || p === savedPattern.current) return
    if (await onUpdate({ pattern: p })) savedPattern.current = p
    else setPattern(savedPattern.current)
  }
  const latestPattern = useRef(commitPattern)
  latestPattern.current = commitPattern
  useEffect(() => () => void latestPattern.current(), [])

  // A file holds a path, and a reference keys, so neither has a type.
  const typeable = !attribute.file && !attribute.ref
  const typed = attribute.type !== undefined

  const chips = attributeChips(attribute, stored, structs)
  const described = definition.trim()

  return (
    <div className={open ? 'attribute open' : 'attribute'}>
      <div className="attribute-summary-row">
        <button
          ref={summary}
          type="button"
          className="attribute-summary"
          aria-expanded={open}
          aria-controls={open ? editor : undefined}
          onClick={onToggle}
        >
          <span className="attribute-line">
            <span className="attribute-summary-name">{trimmed || attribute.name}</span>
            {chips.map((c) => (
              <span key={c.text} className={['attribute-chip', c.muted && 'muted', c.mono && 'mono'].filter(Boolean).join(' ')} title={c.title}>
                {c.text}
              </span>
            ))}
          </span>
          {/* Open, the definition shows in full beneath instead. */}
          {!open && (
            <span className={described ? 'attribute-summary-definition' : 'attribute-summary-definition empty'}>
              {described || 'No definition; agents go by its name'}
            </span>
          )}
        </button>
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
      {open && (
        <div
          id={editor}
          className="attribute-editor"
          onKeyDown={(e) => {
            if (e.key !== 'Escape') return
            // Folds the row rather than closing the panel; the focus leaving
            // the field first saves it.
            e.stopPropagation()
            summary.current?.focus()
            onToggle()
          }}
        >
          <label htmlFor={`${editor}-name`}>Name</label>
          <input
            id={`${editor}-name`}
            className="attribute-name"
            value={name}
            disabled={!editable}
            spellCheck={false}
            onChange={(e) => setName(e.target.value)}
            onBlur={commitName}
            onKeyDown={(e) => e.key === 'Enter' && e.currentTarget.blur()}
          />
          {clash && <small className="panel-warning">Another attribute has that name.</small>}
          <label htmlFor={`${editor}-definition`} className="attribute-editor-top">
            Definition
          </label>
          <textarea
            id={`${editor}-definition`}
            value={definition}
            rows={3}
            disabled={!editable}
            placeholder="What it holds"
            onChange={(e) => setDefinition(e.target.value)}
            onBlur={() => flush(definition)}
          />
          {typeable && (
            <>
              <span className="help-head">
                <label htmlFor={`${editor}-type`}>Type</label>
                {help.button('type', 'its type')}
              </span>
              <select
                id={`${editor}-type`}
                value={attribute.type ?? ''}
                disabled={!editable}
                onChange={(e) => onUpdate({ type: e.target.value as AttributeType | '' })}
              >
                <option value="">Any value</option>
                {ATTRIBUTE_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {TYPE_NAMES[t]}
                  </option>
                ))}
              </select>
              {help.note(
                'type',
                <>
                  What each of its values must be. A process handing in another is told what is wrong: an agent fixes it, a
                  script's step fails; a person entering one, a request starting a run and a hand edit of a record are
                  refused. Integer and Number are JSON numbers, Yes or no is true or false, and Date and time a date such as
                  2026-11-03, or a date and time such as 2026-11-03T09:00:00Z. Any value takes whatever fits its definition.
                  A file or an attribute referring to records has no type.
                </>,
              )}
            </>
          )}
          {attribute.type === 'text' && (
            <>
              <span className="help-head">
                <label htmlFor={`${editor}-pattern`}>Pattern</label>
                {help.button('pattern', 'its pattern')}
              </span>
              <input
                id={`${editor}-pattern`}
                className="attribute-pattern"
                value={pattern}
                disabled={!editable}
                spellCheck={false}
                placeholder="Any text"
                onChange={(e) => setPattern(e.target.value)}
                onBlur={commitPattern}
                onKeyDown={(e) => e.key === 'Enter' && e.currentTarget.blur()}
              />
              {help.note(
                'pattern',
                <>
                  A regular expression the whole of the text must match, such as <code>[A-Z]+-\d+</code> for TALK-123; empty
                  takes any text. It is written as Go's regular expressions are, which have no lookarounds or backreferences.
                </>,
              )}
            </>
          )}
          {refers && !typed && (!attribute.unique || attribute.ref) && (
            <>
              <span className="help-head">
                <label htmlFor={`${editor}-ref`}>Refers to</label>
                {help.button('ref', 'what it refers to')}
              </span>
              <select
                id={`${editor}-ref`}
                value={attribute.ref ?? 0}
                disabled={!editable || attribute.unique}
                onChange={(e) => onUpdate({ ref: Number(e.target.value) })}
              >
                <option value={0}>Nothing</option>
                {structs.map((st) => (
                  <option key={st.object} value={st.object}>
                    a {st.name} record
                  </option>
                ))}
                {attribute.ref && !structs.some((st) => st.object === attribute.ref) && (
                  <option value={attribute.ref}>a struct you don’t see</option>
                )}
              </select>
              {help.note(
                'ref',
                <>
                  Holds the unique value of a record of another shared struct, such as a pull request's ticket, or a list of
                  them, each naming a record: a step writing one no record holds fails, and a hand edit is refused. A unique
                  attribute is its record's own key, so it refers to none.
                </>,
              )}
            </>
          )}
          <div className="attribute-options">
            <span className="help-head">
              <label className="panel-check">
                <input
                  type="checkbox"
                  checked={attribute.unique}
                  disabled={!editable || !stored}
                  onChange={(e) => onUpdate({ unique: e.target.checked })}
                />
                <span>Unique</span>
              </label>
              {help.button('unique', 'Unique')}
            </span>
            <span className="help-head">
              <label className="panel-check">
                <input
                  type="checkbox"
                  checked={attribute.file}
                  disabled={!editable || typed}
                  onChange={(e) => onUpdate({ file: e.target.checked })}
                />
                <span>File</span>
              </label>
              {help.button('file', 'File')}
            </span>
          </div>
          {help.note(
            'unique',
            <>
              No two stored values share it. A shared struct's records are told apart by it, so generating a value a record
              holds refreshes that record; on an object storing what runs generate, a step generating a value another run
              stored terminates its run, so nothing is handled twice.
              {!stored && ' Store what runs generate to make an attribute unique.'}
            </>,
          )}
          {help.note(
            'file',
            <>
              Holds the path of a file the step wrote, which must exist, rather than what it holds. A script writes it in
              $OUTPUTS.{typed && ' A file has no type, so set Type to Any value first.'}
            </>,
          )}
        </div>
      )}
    </div>
  )
}

// The types an attribute can have, beside any value, as they are named.
const ATTRIBUTE_TYPES: AttributeType[] = ['text', 'integer', 'number', 'boolean', 'datetime']
const TYPE_NAMES: Record<AttributeType, string> = {
  text: 'Text',
  integer: 'Integer',
  number: 'Number',
  boolean: 'Yes or no',
  datetime: 'Date and time',
}

type Chip = { text: string; title: string; muted?: boolean; mono?: boolean }

// What an attribute is set to beyond holding any value, a word each, as its
// folded line shows it: unique, its type, or for text its pattern, a file,
// the struct it refers to. Unique on an object that isn't stored is muted,
// as it changes nothing there.
function attributeChips(a: ObjectAttribute, stored: boolean, structs: TaskStruct[]): Chip[] {
  const chips: Chip[] = []
  if (a.unique)
    chips.push(
      stored
        ? { text: 'unique', title: 'No two stored values share it' }
        : { text: 'unique', title: 'Unique only once what runs generate is stored', muted: true },
    )
  if (a.type === 'text' && a.pattern) chips.push({ text: `/${a.pattern}/`, title: `Text the whole of which matches ${a.pattern}`, mono: true })
  else if (a.type) chips.push({ text: TYPE_NAMES[a.type].toLowerCase(), title: `Each value is ${TYPE_WORDS[a.type]}` })
  if (a.file) chips.push({ text: 'file', title: 'Holds the path of a file the step wrote' })
  if (a.ref) {
    const st = structs.find((s) => s.object === a.ref)
    chips.push(
      st
        ? { text: `→ ${st.name}`, title: `Holds the key of a ${st.name} record, or a list of them` }
        : { text: '→ a record', title: 'Holds the key of a record of a struct you don’t see' },
    )
  }
  return chips
}

// What a value of each type is, in words and as an example.
const TYPE_WORDS: Record<AttributeType, string> = {
  text: 'text',
  integer: 'a whole number',
  number: 'a number',
  boolean: 'true or false',
  datetime: 'a date, or a date and time',
}
const TYPE_EXAMPLES: Record<AttributeType, string> = {
  text: '"…"',
  integer: '3',
  number: '0.8',
  boolean: 'true',
  datetime: '"2026-11-03T09:00:00Z"',
}

// What a process hands in, keys in order: {"data": …} without fields, an
// array of such objects for a list; the objects it is made of nest theirs,
// and a typed attribute shows a value of its type.
function example(fields: Field[], many: boolean): string {
  const value = (fs: Field[], indent: string, type?: AttributeType): string =>
    fs.length === 0
      ? type
        ? TYPE_EXAMPLES[type]
        : '…'
      : `{\n${fs.map((f) => `${indent}  ${JSON.stringify(f.name)}: ${f.many ? `[${value(f.fields, indent + '  ')}]` : value(f.fields, indent + '  ', f.attribute?.type)}`).join(',\n')}\n${indent}}`
  const object = (indent: string) => value(fields.length > 0 ? fields : [{ name: 'data', definition: '', file: false, many: false, min: 0, max: null, fields: [], inherited: false }], indent)
  return many ? `[\n  ${object('  ')}\n]` : object('')
}

// Which record each run takes: in which state, the oldest or the latest,
// and a rule its fields hold, saved as each changes.
function PickFields({
  pick,
  states,
  editable,
  onChange,
}: {
  pick: ObjectPick
  states: ObjectState[]
  editable: boolean
  onChange: (pick: ObjectPick) => Promise<boolean>
}) {
  const [where, setWhere] = useState(pick.where ?? '')
  useEffect(() => setWhere(pick.where ?? ''), [pick.where])
  const set = (patch: ObjectPick) => onChange({ ...pick, ...patch })
  return (
    <div className="query-link">
      <div className="panel-row">
        <select
          aria-label="In which state"
          value={pick.state ?? ''}
          disabled={!editable}
          onChange={(e) => set({ state: e.target.value || undefined })}
        >
          <option value="">In any state</option>
          {states.map((s) => (
            <option key={s.id} value={s.name}>
              In {s.name}
            </option>
          ))}
        </select>
        <select
          aria-label="Which first"
          value={pick.oldest ? 'oldest' : 'latest'}
          disabled={!editable}
          onChange={(e) => set({ oldest: e.target.value === 'oldest' || undefined })}
        >
          <option value="oldest">The oldest</option>
          <option value="latest">The latest</option>
        </select>
      </div>
      <label className="panel-field" title="A rule its fields must hold, as a state's: comparisons joined by and. Empty takes any.">
        <span>Where</span>
        <input
          type="text"
          value={where}
          placeholder='priority == "high"'
          readOnly={!editable}
          onChange={(e) => setWhere(e.target.value)}
          onBlur={() => where.trim() !== (pick.where ?? '') && set({ where: where.trim() || undefined })}
        />
      </label>
    </div>
  )
}
