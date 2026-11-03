import type { ReferringGroup } from './api'

// The records referring to a record, in its dialog's tab of its own: for
// each struct and attribute naming it, such as a ticket's pull requests,
// each record with its state and whether a run is going on it, and a link to
// all of them on that struct's Records page; see references.go.

// What a referring record is called in its row: the first of its fields
// holding text, else its id.
function label(content: unknown, skip: string, id: number): string {
  if (content && typeof content === 'object' && !Array.isArray(content)) {
    for (const [k, v] of Object.entries(content as Record<string, unknown>)) {
      if (k !== skip && typeof v === 'string' && v.trim() !== '') return v
    }
  }
  return `#${id}`
}

// The tab's name: the one struct referring to it, or what refers to it.
export function referencesTabLabel(groups: ReferringGroup[]): string {
  const n = groups.reduce((sum, g) => sum + g.records.length, 0)
  return groups.length === 1 ? `${groups[0].name} (${n}${groups[0].more ? '+' : ''})` : `Referred to by (${n})`
}

export function RecordReferences({ groups, hidden, onNavigate }: { groups: ReferringGroup[]; hidden: boolean; onNavigate: () => void }) {
  return (
    <div id="record-references" className="record-references" role="tabpanel" hidden={hidden}>
      {groups.map((g) => (
        <section key={`${g.struct}:${g.attribute}`}>
          <h3>
            {g.name} records whose {g.attribute} is {g.key}
            <a
              className="link-button"
              href={`#/flows/${g.struct}/records?q=${encodeURIComponent(`${g.attribute}:${JSON.stringify(g.key)}`)}`}
              onClick={onNavigate}
            >
              Show on its Records page
            </a>
          </h3>
          {g.records.length === 0 ? (
            <p className="panel-note">None yet.</p>
          ) : (
            <ul>
              {g.records.map((r) => (
                <li key={r.id}>
                  <span className="record-references-name">{label(r.content, g.attribute, r.id)}</span>
                  {r.going && <span className="record-references-going">run going</span>}
                  <span className="record-state">{r.state || 'no state'}</span>
                </li>
              ))}
            </ul>
          )}
          {g.more && <p className="panel-note">Only the latest {g.records.length} are listed.</p>}
        </section>
      ))}
    </div>
  )
}
