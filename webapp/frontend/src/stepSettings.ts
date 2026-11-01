// What a process was set to in the version a run ran, as lines of a name
// and a value under the run's step (Runs.tsx), so its settings are read
// beside what it did rather than in a panel taking the run's place. Only
// what the step doesn't show already: not its model, prompt or script, nor
// the operation a connector carried out. A setting left at its default says
// so. scripts/run-views.ts checks it.

import type { ProcessKind, ProcessSettings } from './api'

// The names of what settings pick by id, null while they load. An id
// missing from them was deleted, or picked by someone else from what they
// may use.
export type SettingNames = {
  skills: Map<number, string> | null
  credentials: Map<number, string> | null
  mcpServers: Map<number, string> | null
}

// What a process's rounds and loops are when it doesn't set them.
export type SettingDefaults = { rounds: number; loops: number }

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

export function settingsRows(kind: ProcessKind, s: ProcessSettings, names: SettingNames, defaults: SettingDefaults): [string, string][] {
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
    rows.push(['Repositories', s.repositories === undefined ? "all of the flow's" : s.repositories.length === 0 ? 'none' : s.repositories.join(', ')])
  }
  if (kind === 'agent') {
    rows.push(['Told about its flow', s.describeFlow ? 'yes, in OPL' : 'no'])
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
  if (kind === 'human') {
    const after = (seconds: number | undefined) => (seconds === undefined ? 'never' : durationText(seconds))
    rows.push(['Remind every', after(s.remindAfter)])
    rows.push(['Fail after', after(s.waitAtMost)])
    rows.push(['Hand on after', after(s.escalateAfter)])
    rows.push(['Hand on to', s.escalateTo && s.escalateTo.length > 0 ? s.escalateTo.join(', ') : 'nobody'])
  }
  return rows
}
