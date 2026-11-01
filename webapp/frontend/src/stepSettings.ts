// What a process is set to, as lines of a name and a value: under a run's
// step (Runs.tsx), so its settings are read beside what it did rather than
// in a panel taking the run's place, and in a process's panel that can't be
// changed (ProcessPanel.tsx), which also sums each group up in a line while
// it is folded. Only what is set apart from the process's body: not its
// model, prompt or script, nor the operation a connector carries out. A
// setting left at its default says so. scripts/run-views.ts checks it.

import type { ProcessKind, ProcessSettings } from './api'

// The names of what settings pick by id, null while they load. An id
// missing from them was deleted, or picked by someone else from what they
// may use.
export type SettingNames = {
  skills: Map<number, string> | null
  credentials: Map<number, string> | null
  mcpServers: Map<number, string> | null
}

// What a process's rounds and loops are when it doesn't set them, and how
// many repositories its flow has, all of which it works on unless it picks.
export type SettingDefaults = { rounds: number; loops: number; repositories: number }

// A number of seconds as days, hours, minutes and seconds, such as 1d 12h,
// as a process's duration settings are written.
export function durationText(seconds: number): string {
  const parts: string[] = []
  for (const [unit, size] of [['d', 86400], ['h', 3600], ['m', 60], ['s', 1]] as const) {
    const n = Math.floor(seconds / size)
    if (n > 0) parts.push(`${n}${unit}`)
    seconds -= n * size
  }
  return parts.join(' ') || '0s'
}

const picked = (ids: number[] | undefined, names: Map<number, string> | null, what: (id: number) => string = (id) => names?.get(id) ?? 'one not shown here') =>
  !ids || ids.length === 0 ? 'none' : names === null ? `${ids.length} picked` : ids.map(what).join(', ')

const orDefault = (value: number | undefined, fallback: string) => (value === undefined ? `${fallback} (default)` : String(value))

// What an agent or script works with: the skills, credentials, MCP servers
// and repositories it picks, and whether its prompt describes its flow. None
// of the repositories picked stands out only while the flow has some, as on
// a flow without any it is all there is.
export function canUseRows(kind: ProcessKind, s: ProcessSettings, names: SettingNames, repositories: number): [string, string][] {
  const rows: [string, string][] = []
  if (kind === 'agent') {
    rows.push(['Skills', picked(s.skills, names.skills)])
  }
  if (kind === 'agent' || kind === 'script') {
    rows.push(['Credentials', picked(s.credentials, names.credentials)])
  }
  if (kind === 'agent') {
    rows.push([
      'MCP servers',
      picked(s.mcpServers, names.mcpServers, (id) => {
        const name = names.mcpServers?.get(id) ?? 'one not shown here'
        const tools = s.mcpTools?.[String(id)]
        return tools ? `${name} (${tools.length === 1 ? 'one tool' : `${tools.length} tools`})` : name
      }),
    ])
  }
  if (kind === 'agent' || kind === 'script') {
    rows.push([
      'Repositories',
      s.repositories === undefined
        ? "all of the flow's"
        : s.repositories.length === 0
          ? repositories > 0
            ? "none of the flow's"
            : 'none'
          : s.repositories.join(', '),
    ])
  }
  if (kind === 'agent') {
    rows.push(['Whole flow in its prompt', s.describeFlow ? 'yes' : 'no'])
  }
  return rows
}

// How far a step may go: its rounds, loops, time, retries and how many of
// its inputs it needs.
export function limitRows(kind: ProcessKind, s: ProcessSettings, defaults: Pick<SettingDefaults, 'rounds' | 'loops'>): [string, string][] {
  const rows: [string, string][] = []
  if (kind === 'agent') {
    rows.push(['Rounds', orDefault(s.rounds, String(defaults.rounds))])
  }
  if (kind === 'agent' || kind === 'script' || kind === 'connector') {
    rows.push(['Loops', orDefault(s.loops, String(defaults.loops))])
    rows.push(['Timeout', s.timeout === undefined ? "the worker's (default)" : durationText(s.timeout)])
    rows.push(['Retries', orDefault(s.retries, '0')])
  }
  if (kind !== 'human') {
    rows.push(['Needs', s.quorum === undefined ? 'all its inputs (default)' : `${s.quorum} of its inputs`])
  }
  return rows
}

// What a human process does while nobody decides it.
export function waitRows(kind: ProcessKind, s: ProcessSettings): [string, string][] {
  if (kind !== 'human') return []
  const after = (seconds: number | undefined) => (seconds === undefined ? 'never' : durationText(seconds))
  return [
    ['Remind every', after(s.remindAfter)],
    ['Fail after', after(s.waitAtMost)],
    ['Hand on after', after(s.escalateAfter)],
    ['Hand on to', s.escalateTo && s.escalateTo.length > 0 ? s.escalateTo.join(', ') : 'nobody'],
  ]
}

export function settingsRows(kind: ProcessKind, s: ProcessSettings, names: SettingNames, defaults: SettingDefaults): [string, string][] {
  return [...canUseRows(kind, s, names, defaults.repositories), ...limitRows(kind, s, defaults), ...waitRows(kind, s)]
}

// The same under the headings a process's panel groups them by, those with
// none left out.
export type SettingGroup = { title: string; rows: [string, string][] }

export function settingGroups(kind: ProcessKind, s: ProcessSettings, names: SettingNames, defaults: SettingDefaults): SettingGroup[] {
  return [
    { title: 'Can use', rows: canUseRows(kind, s, names, defaults.repositories) },
    { title: 'Limits', rows: limitRows(kind, s, defaults) },
    { title: 'While it waits', rows: waitRows(kind, s) },
  ].filter((g) => g.rows.length > 0)
}

// Whether a line's value is what the process has when nothing is set, as
// the lines show it, so what is set stands out.
export function atDefault(value: string): boolean {
  return value.endsWith('(default)') || ['none', 'no', 'never', 'nobody', "all of the flow's"].includes(value)
}

// A folded group's line: the names of what its process picks, and which
// repositories it works on when the flow has any, none picked of none
// saying nothing.
export function canUseSummary(kind: ProcessKind, s: ProcessSettings, names: SettingNames, repositories: number): string {
  const picks = canUseRows(kind, s, names, repositories)
    .filter(([label, value]) => (label === 'Skills' || label === 'Credentials' || label === 'MCP servers') && value !== 'none')
    .map(([, value]) => value)
  if (kind === 'agent' && s.describeFlow) picks.push('the whole flow in its prompt')
  const works =
    kind !== 'agent' && kind !== 'script'
      ? ''
      : s.repositories === undefined
        ? repositories > 0
          ? 'every repository of the flow'
          : ''
        : s.repositories.length === 0
          ? repositories > 0
            ? 'no repository'
            : ''
          : s.repositories.join(', ')
  return [picks.length > 0 ? picks.join(', ') : 'nothing picked', works].filter(Boolean).join(' · ')
}

// The rows set apart from their defaults, as one line; never is a human
// process's default.
export function changedSummary(rows: [string, string][], none: string): string {
  const changed = rows.filter(([, value]) => !atDefault(value))
  return changed.length === 0 ? none : changed.map(([label, value]) => `${label} ${value}`).join(', ')
}
