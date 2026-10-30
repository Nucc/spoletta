import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { getAuditLog, type AuditLog, type Search } from './api'
import { FlowBuilderIcon } from './icons'
import { indexFacets, parse as parseQuery } from './query'
import { formatTime } from './Runs'
import { ariaSort, FacetList, QueryBar, SortButton, TimeRange, useSearch } from './Search'

// How often the log is asked for again, as steps run.
const REFRESH_MS = 10_000

// The events the search matches, asked for again every so often; only the
// answer to the latest ask shows.
function useAuditLog(search: Search) {
  const [log, setLog] = useState<AuditLog | null>(null)
  const [error, setError] = useState<string | null>(null)
  const asked = useRef(0)
  const load = useCallback(() => {
    const n = ++asked.current
    return getAuditLog(search)
      .then((l) => n === asked.current && (setLog(l), setError(null)))
      .catch((err) => setError(err instanceof Error ? err.message : String(err)))
  }, [search])
  useEffect(() => {
    load()
    const timer = setInterval(load, REFRESH_MS)
    return () => clearInterval(timer)
  }, [load])
  return { log, error, load }
}

// The audit log, for admins: the tokens steps were handed for their models,
// MCP servers, the internet and git, and when they ended; each internet
// connection; calls refused and runs stopped for spending what they may; and
// the limits, internet hosts and model keys admins changed. Searched and
// sorted as a flow's data is, on the server over the latest events.
export default function AuditLogPage() {
  const { query, setQuery, sort, setSort, search } = useSearch('audit', 0)
  const { log, error, load } = useAuditLog(search)
  const facets = useMemo(() => indexFacets(log?.facets ?? []), [log])
  const ast = useMemo(() => parseQuery(query).ast, [query])

  useEffect(() => {
    document.title = 'Audit log · Spoletta'
  }, [])

  const header = (key: string, label: string, newestFirst = false) => (
    <th key={key} aria-sort={ariaSort(sort, key)}>
      <SortButton facet={key} sort={sort} onSort={setSort} newestFirst={newestFirst}>
        {label}
      </SortButton>
    </th>
  )

  return (
    <div className="flows-page data-page">
      <header className="toolbar">
        <h1>
          <a className="home" href="#/" title="Every flow, task board, procedure and struct">
            <FlowBuilderIcon /> Home
          </a>
          <span className="crumb-separator" aria-hidden>
            /
          </span>
          <a href="#/settings">Settings</a>
          <span className="crumb-separator" aria-hidden>
            /
          </span>
          Audit log
        </h1>
        <button onClick={load} title="Fetch the latest events">
          Refresh
        </button>
      </header>
      {error && (
        <div className="error-banner" role="alert">
          {error}
        </div>
      )}
      <main className="data-main">
        {log === null ? (
          <p className="flows-note">Loading…</p>
        ) : log.searched === 0 ? (
          <p className="flows-note">Nothing recorded yet. Each step run, and each change an admin makes, shows here.</p>
        ) : (
          <>
            <div className="search-row">
              <QueryBar
                facets={facets}
                query={query}
                onChange={setQuery}
                placeholder="Search the log, e.g. kind:internet.refused run:42"
              />
              <TimeRange facets={facets} query={query} field="at" onChange={setQuery} />
            </div>
            <div className="search-body">
              <FacetList facets={facets} ast={ast} query={query} onChange={setQuery} rows={log.searched} />
              <div className="search-results">
                <p className="flows-note">
                  {log.matched === log.searched
                    ? `${log.searched} events`
                    : `${log.matched} of ${log.searched} events match`}
                  {log.events.length < log.matched && `; the first ${log.events.length} show`}
                  {log.truncated && '; older events are kept but not searched'}
                </p>
                <div className="data-table-scroll">
                  <table className="data-table">
                    <thead>
                      <tr>
                        {header('at', 'When', true)}
                        {header('kind', 'Kind')}
                        {header('who', 'Who')}
                        {header('run', 'Run')}
                        {header('step', 'Step')}
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
                          <td>{e.who || <span className="data-started">nobody</span>}</td>
                          <td>
                            {e.run !== null &&
                              (e.flow !== null ? <a href={`#/flows/${e.flow}/runs/${e.run}`}>{e.run}</a> : e.run)}
                          </td>
                          <td>{e.step}</td>
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
      </main>
    </div>
  )
}
