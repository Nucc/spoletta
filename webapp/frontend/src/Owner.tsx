import type { Flow, FlowOwner, Me, Team } from './api'

// What is owned as a flow is: a flow, or a skill or command.
type Owned = Pick<Flow, 'name' | 'owner' | 'access'>

// What an owner is called on a card: "Only you", a team's name, "Everyone",
// or for an admin, whose it is.
export function ownerLabel(owner: FlowOwner, me: Me | null): string {
  switch (owner.kind) {
    case 'global':
      return 'Everyone'
    case 'team':
      return owner.name
    case 'user':
      return owner.id === me?.user?.id ? 'Only you' : owner.name
  }
}

type Choice = { key: string; label: string; owner: { kind: 'global' } | { kind: 'team' | 'user'; id: number } }

const keyOf = (o: FlowOwner | Choice['owner']) => (o.kind === 'global' ? 'global' : `${o.kind}:${o.id}`)

// Who a flow can go to from whoever is signed in: themselves, their teams,
// or everyone; an admin gives it to any team too. The owner it has now is
// always among them.
function choices(flow: Owned, me: Me, teams: Team[]): Choice[] {
  const user = me.user!
  const out: Choice[] = []
  const add = (c: Choice) => out.some((x) => x.key === c.key) || out.push(c)
  if (flow.owner.kind === 'user' && flow.owner.id !== user.id) {
    add({ key: keyOf(flow.owner), label: `${flow.owner.name} only`, owner: flow.owner })
  }
  add({ key: `user:${user.id}`, label: 'Only me', owner: { kind: 'user', id: user.id } })
  for (const t of teams) {
    if (
      user.admin ||
      t.members.some((m) => m.id === user.id) ||
      (flow.owner.kind === 'team' && flow.owner.id === t.id)
    ) {
      add({ key: `team:${t.id}`, label: `Team ${t.name}`, owner: { kind: 'team', id: t.id } })
    }
  }
  add({ key: 'global', label: 'Everyone', owner: { kind: 'global' } })
  return out
}

// Whether whoever is signed in can give the flow away at all: its owner, a
// member of the team owning it, or an admin; nobody but an admin takes a
// global one back.
function canGive(flow: Owned, me: Me): boolean {
  const user = me.user!
  if (user.admin) return true
  switch (flow.owner.kind) {
    case 'global':
      return false
    case 'user':
      return flow.owner.id === user.id
    case 'team':
      return flow.access === 'edit'
  }
}

// Who sees the flow and may change it, to pick another; nothing without
// sign-in, when everything is everyone's. Compact, it is the select alone,
// as on the main page's cards.
export function OwnerPicker({
  flow,
  me,
  teams,
  compact,
  onChange,
}: {
  flow: Owned
  me: Me | null
  teams: Team[]
  compact?: boolean
  onChange: (owner: Choice['owner']) => void
}) {
  if (!me?.user) return null
  const able = canGive(flow, me)
  const select = (
    <select
      className={compact ? `flow-owner ${flow.owner.kind}` : undefined}
      value={keyOf(flow.owner)}
      disabled={!able}
      aria-label={`Who sees ${flow.name}`}
      title={compact ? 'Who sees it and may change it' : undefined}
      onChange={(e) => {
        const c = choices(flow, me, teams).find((c) => c.key === e.target.value)
        if (c) onChange(c.owner)
      }}
    >
      {choices(flow, me, teams).map((c) => (
        <option key={c.key} value={c.key}>
          {c.label}
        </option>
      ))}
    </select>
  )
  if (compact) return select
  return (
    <label className="panel-field">
      <span>Who sees it</span>
      {select}
      <small className="panel-note">
        {!able
          ? flow.owner.kind === 'global'
            ? 'Everyone sees it and may change it; only an admin takes it back.'
            : 'Only its owner or an admin gives it to someone else.'
          : 'Those it belongs to see it, change it and run it; a team’s are its members’.'}
      </small>
    </label>
  )
}
