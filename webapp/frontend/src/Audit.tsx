import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { getAuditLog, getInstanceEvents, type AuditLog, type Search } from './api'
import { indexFacets, parse as parseQuery } from './query'
import { formatTime } from './Runs'
import { ariaSort, FacetList, QueryBar, SortButton, TimeRange, useSearch } from './Search'
import ErrorBanner from './ErrorBanner'
import { SettingsHead } from './SettingsHead'
import { pageHash } from './address'

// How often the log is asked for again, as steps run.
const REFRESH_MS = 10_000

// The events the search matches, asked for again every so often; only the
// answer to the latest ask shows.
function useAuditLog(search: Search, read: (search: Search) => Promise<AuditLog>) {
  const [log, setLog] = useState<AuditLog | null>(null)
  const [error, setError] = useState<string | null>(null)
  const asked = useRef(0)
  const load = useCallback(() => {
    const n = ++asked.current
    return read(search)
      .then((l) => n === asked.current && (setLog(l), setError(null)))
      .catch((err) => setError(err instanceof Error ? err.message : String(err)))
  }, [search, read])
  useEffect(() => {
    load()
    const timer = setInterval(load, REFRESH_MS)
    return () => clearInterval(timer)
  }, [load])
  return { log, error, load }
}

// What each log is: the project's, or the instance's above the projects.
const LOGS = {
  audit: {
    label: 'Audit log',
    read: getAuditLog,
    lead: "What steps were handed and reached, what was refused, and what admins changed, searched as a flow's data is.",
    more: "The tokens steps were handed for their models, MCP servers, the internet, credentials and git, and when they ended; each internet connection and credential call; calls refused and runs stopped for spending what they may; and the limits, internet hosts, model keys, credentials and webhooks the project's owners changed. Never a token or a secret. It asks for the latest events every 10 seconds.",
    empty: 'Nothing recorded yet. Each step run, and each change an owner of the project makes, shows here.',
    placeholder: 'Search the log, e.g. kind:internet.refused run:42',
  },
  events: {
    label: 'Instance events',
    read: getInstanceEvents,
    lead: 'What happened above the projects, searched as the audit log is.',
    more: "Projects made, archived, brought back and deleted, the ceilings set over every project's limits, people made admins of Spoletta or no longer, and model and credential calls refused for a token no step was handed. What happens inside a project is in its own audit log. It asks for the latest events every 10 seconds.",
    empty: 'Nothing recorded yet. Making a project, or setting a ceiling, shows here.',
    placeholder: 'Search the events, e.g. kind:project.made',
  },
}

// How many events show of those the search matched, and whether older ones
// were left out of the search.
function countLine(log: AuditLog): string {
  const n = (x: number) => x.toLocaleString()
  const shown = log.events.length
  const all = log.matched === log.searched
  let line =
    shown < log.matched
      ? all
        ? `Showing ${n(shown)} of ${n(log.searched)} events.`
        : `Showing ${n(shown)} of ${n(log.matched)} matching events, of ${n(log.searched)}.`
      : all
        ? `${n(log.searched)} events.`
        : `${n(log.matched)} of ${n(log.searched)} events match.`
  if (log.truncated) line += ` Only the latest ${n(log.searched)} are searched; older ones are kept.`
  return line
}

// The audit log, for the project's owners: the tokens steps were handed for
// their models, MCP servers, the internet and git, and when they ended;
// each internet connection; calls refused and runs stopped for spending
// what they may; and the limits, internet hosts and model keys owners
// changed. Searched and sorted as a flow's data is, on the server over the
// latest events. A tab of the settings, #/settings/audit, taking the
// window's width; #/audit, where it was before, opens it there. The
// instance's own events, above the projects, show the same way for its
// admins at #/settings/events (instance), without the runs and steps no
// such event has.
export function AuditLogSection({ instance = false }: { instance?: boolean }) {
  const kind = instance ? 'events' : 'audit'
  const words = LOGS[kind]
  const { query, setQuery, sort, setSort, search } = useSearch(kind, 0)
  const { log, error, load } = useAuditLog(search, words.read)
  const facets = useMemo(() => indexFacets(log?.facets ?? []), [log])
  const ast = useMemo(() => parseQuery(query).ast, [query])

  const header = (key: string, label: string, newestFirst = false) => (
    <th key={key} aria-sort={ariaSort(sort, key)}>
      <SortButton facet={key} sort={sort} onSort={setSort} newestFirst={newestFirst}>
        {label}
      </SortButton>
    </th>
  )

  // Beside the search once there are events, so it sits by what it reloads.
  const refresh = (
    <button type="button" className="settings-button" onClick={load} title="Fetch the latest events">
      Refresh
    </button>
  )
  // Who is left out while no event shown names anyone, as with steps' own grants.
  const who = log?.events.some((e) => e.who) ?? false

  return (
    <section className="flow-section audit" aria-label={words.label}>
      <SettingsHead
        title={words.label}
        lead={words.lead}
        more={words.more}
        action={log === null || log.searched === 0 ? refresh : undefined}
      />
      <ErrorBanner error={error} />
      {log === null ? (
        <p className="flows-note">Loading…</p>
      ) : log.searched === 0 ? (
        <p className="flows-note">{words.empty}</p>
      ) : (
        <>
          <div className="search-row">
            <QueryBar facets={facets} query={query} onChange={setQuery} placeholder={words.placeholder} />
            <TimeRange facets={facets} query={query} field="at" onChange={setQuery} />
            {refresh}
          </div>
          <div className="search-body">
            <FacetList facets={facets} ast={ast} query={query} onChange={setQuery} rows={log.searched} folded={['run', 'step']} />
            <div className="search-results">
              <p className="flows-note">{countLine(log)}</p>
              <div className="data-table-scroll">
                <table className="data-table audit-table">
                  <thead>
                    <tr>
                      {header('at', 'When', true)}
                      {header('kind', 'Kind')}
                      {who && header('who', 'Who')}
                      {!instance && header('run', 'Run')}
                      {!instance && header('step', 'Step')}
                      <th>What</th>
                    </tr>
                  </thead>
                  <tbody>
                    {log.events.map((e) => (
                      <tr key={e.id}>
                        <td>{formatTime(e.at)}</td>
                        <td>
                          <code>{e.kind}</code>
                        </td>
                        {who && <td>{e.who || <span className="data-started">nobody</span>}</td>}
                        {!instance && (
                          <td>
                            {e.run !== null &&
                              (e.flow !== null ? <a href={pageHash(`#/flows/${e.flow}/runs/${e.run}`)}>{e.run}</a> : e.run)}
                          </td>
                        )}
                        {!instance && <td>{e.step}</td>}
                        <td className="audit-detail">{e.detail}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          </div>
        </>
      )}
    </section>
  )
}
