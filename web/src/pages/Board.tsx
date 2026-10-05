import { useState, type ReactNode } from 'react'
import { api, problem, type Schemas } from '../api/client'
import { useFetch } from '../fetch'
import { checkStates, pathText, since, unitPath, words } from '../format'
import { Link } from '../Link'
import { useLive } from '../live'
import { asked, refreshPRs } from '../refresh'
import { themedLink, useShownTheme } from '../theme'

type BoardUnit = Schemas['BoardUnit']
type BoardPR = Schemas['BoardPullRequest']

// Column is one column of the board: its cell on a unit's row and on each of its pull requests' rows, and the value
// a unit sorts by. A pull request in no group is one row, with both cells.
type Column = {
  key: string
  label: string
  unit?: (u: BoardUnit) => ReactNode
  pr?: (p: BoardPR) => ReactNode
  sort: (u: BoardUnit) => number | string
  wrap?: boolean
}

// Look is how a value shows as a tag, and where it sorts; a hint, if any, says what the tag covers when pointed at.
type Look = { text: string; colour: string; order: number; hint?: string }

const balls: Record<BoardUnit['ball'], Look> = {
  yours: { text: 'Yours', colour: 'red', order: 0 },
  others: { text: "Others'", colour: 'green', order: 1 },
  none: { text: 'Not yours', colour: 'gray', order: 2 },
}
const checks: Record<string, Look> = {
  failing: { ...checkStates.failing, colour: 'red', order: 0 },
  pending: { ...checkStates.pending, colour: 'yellow', order: 1 },
  passing: { ...checkStates.passing, colour: 'green', order: 2 },
  none: { ...checkStates.none, colour: 'gray', order: 3 },
}
const reviews: Record<string, Look> = {
  changes_requested: { text: 'Changes requested', colour: 'red', order: 0 },
  approved: { text: 'Approved', colour: 'green', order: 1 },
  none: { text: 'None', colour: 'gray', order: 2 },
}
const taskStates: Record<string, Look> = {
  needs_person: { text: 'Needs you', colour: 'red', order: 0 },
  leased: { text: 'Running', colour: 'blue', order: 1 },
  verifying: { text: 'Verifying', colour: 'blue', order: 2 },
  queued: { text: 'Queued', colour: 'yellow', order: 3 },
}

function tag(look: Look): ReactNode {
  return (
    <span className={`tag ${look.colour}`} title={look.hint}>
      {look.text}
    </span>
  )
}

const quietDash = <span className="quiet">—</span>
const most = (u: BoardUnit, f: (p: BoardPR) => number) => Math.max(...u.pull_requests.map(f))
const least = (u: BoardUnit, f: (p: BoardPR) => number) => Math.min(...u.pull_requests.map(f))
const first = (u: BoardUnit, f: (p: BoardPR) => string) => f(u.pull_requests[0]).toLowerCase()

const columns: Column[] = [
  {
    key: 'ball',
    label: 'Ball',
    unit: (u) => {
      // On a group of several pull requests, the ones its holders hold work on: what put the ball where it is.
      const on = u.unit.kind === 'group' && u.pull_requests.length > 1 ? u.pull_requests.filter((p) => p.holders.length > 0).map(nameOf) : []
      return (
        <>
          {tag(balls[u.ball])}
          {u.ball !== 'yours' && u.holders.length > 0 && <span className="quiet"> {u.holders.map((h) => h.login).join(', ')}</span>}
          {on.length > 0 && <span className="quiet"> on {on.join(', ')}</span>}
        </>
      )
    },
    sort: (u) => balls[u.ball].order,
  },
  {
    key: 'held',
    label: 'Held',
    unit: (u) => (u.held_since ? since(u.held_since) : ''),
    sort: (u) => (u.held_since ? Date.parse(u.held_since) : Number.MAX_SAFE_INTEGER),
  },
  {
    key: 'todo',
    label: 'To do',
    wrap: true,
    pr: (p) => {
      const mine = p.part
      if (mine && mine.kind !== 'idle') {
        return (
          <>
            <strong>{words(mine.action ?? mine.kind)}</strong> — {mine.reason}
          </>
        )
      }
      // With nothing to do of my own, the holders' parts on this pull request say what the ball waits for.
      const held = p.holders.flatMap((h) => h.parts.map((hp) => `${h.account.login}: ${words(hp.action ?? hp.kind)} — ${hp.reason}`))
      if (held.length > 0) return <span className="quiet">{held.join('; ')}</span>
      return mine ? (
        <>
          <strong>{words(mine.kind)}</strong> — {mine.reason}
        </>
      ) : (
        quietDash
      )
    },
    // My own parts first, then the holders', then my waiting, then nothing.
    sort: (u) =>
      first(u, (p) => {
        if (p.part && p.part.kind !== 'idle') return '0' + (p.part.action ?? p.part.kind)
        const hp = p.holders[0]?.parts[0]
        if (hp) return '1' + (hp.action ?? hp.kind)
        return p.part ? '2' + p.part.kind : '~'
      }),
  },
  {
    key: 'task',
    label: 'Task',
    wrap: true,
    unit: (u) =>
      u.task && (
        <>
          <Link to={`/tasks#task-${u.task.id}`}>{tag(taskStates[u.task.state] ?? { text: words(u.task.state), colour: 'gray', order: 4 })}</Link>
          {u.task.note && <span className="quiet"> {u.task.note}</span>}
        </>
      ),
    sort: (u) => (u.task ? (taskStates[u.task.state]?.order ?? 4) : 9),
  },
  {
    key: 'checks',
    label: 'CI',
    pr: (p) => tag(checks[p.summary.checks]),
    sort: (u) => least(u, (p) => checks[p.summary.checks].order),
  },
  {
    key: 'on-you',
    label: 'On you',
    pr: (p) => (p.threads_on_you === undefined ? quietDash : p.threads_on_you > 0 ? <strong>{p.threads_on_you}</strong> : 0),
    sort: (u) => -most(u, (p) => p.threads_on_you ?? -1),
  },
  {
    key: 'unresolved',
    label: 'Open threads',
    pr: (p) => p.threads_unresolved,
    sort: (u) => -most(u, (p) => p.threads_unresolved),
  },
  {
    key: 'review',
    label: 'Your review',
    pr: (p) => (
      <>
        {p.your_review && p.part?.role !== 'author' && tag(reviews[p.your_review])}
        {p.requested && <> {tag({ text: 'Asked of you', colour: 'yellow', order: 0 })}</>}
      </>
    ),
    sort: (u) => least(u, (p) => (p.your_review ? reviews[p.your_review].order : 3)),
  },
  {
    key: 'chain',
    label: 'Chain',
    pr: (p) => (p.chain.length > 1 ? `${p.chain.position}/${p.chain.length}` : ''),
    sort: (u) => -most(u, (p) => p.chain.length),
  },
  {
    key: 'author',
    label: 'Author',
    pr: (p) => p.summary.author.login,
    sort: (u) => first(u, (p) => p.summary.author.login),
  },
  {
    key: 'activity',
    label: 'Last activity',
    pr: (p) => since(p.updated_at),
    sort: (u) => -most(u, (p) => Date.parse(p.updated_at)),
  },
  {
    key: 'path',
    label: 'Path',
    wrap: true,
    pr: (p) => (p.part ? <span className="path">{pathText(p.part)}</span> : ''),
    sort: (u) => first(u, (p) => (p.part ? pathText(p.part) : '~')),
  },
]

// hiddenKey keeps, in the browser, which columns a member chose not to see; until they choose, the path is hidden.
const hiddenKey = 'park.board.hidden'

function hiddenColumns(): Set<string> {
  try {
    const saved: unknown = JSON.parse(localStorage.getItem(hiddenKey) ?? 'null')
    if (Array.isArray(saved)) return new Set(saved.map(String))
  } catch {
    // A choice that cannot be read is no choice.
  }
  return new Set(['path'])
}

function toggled<T>(set: Set<T>, item: T): Set<T> {
  const next = new Set(set)
  if (next.has(item)) next.delete(item)
  else next.add(item)
  return next
}

const keyOf = (u: BoardUnit) => `${u.unit.kind}:${u.unit.id}`
const repoOf = (p: BoardPR) => `${p.summary.repo.owner}/${p.summary.repo.name}`
const nameOf = (p: BoardPR) => `${p.summary.repo.name}#${p.summary.number}`

// Board: every open unit in one table, as it stands for me — whose ball, since when, my task — and each of its open
// pull requests: my action and its reason, its checks, the threads waiting on me, the open threads, my review, its
// chain, author, and last activity, and the path of rules behind my action. Rows sort by a column, filter by the
// ball, the repository, and words, and fold by group; the columns shown are kept in the browser. The forge can be
// asked now about every repository, or the one the board shows, and about a unit's pull requests.
export default function Board() {
  const theme = useShownTheme()
  const { data, refresh } = useFetch(async () => {
    const { data, error } = await api.GET('/board')
    return { units: data?.units, problem: error ? problem(error) : '' }
  })
  useLive(['verdict', 'task', 'pr', 'group'], refresh)
  const [hidden, setHidden] = useState(hiddenColumns)
  const [folded, setFolded] = useState(new Set<string>())
  const [sort, setSort] = useState<{ key: string; down: boolean }>()
  const [ball, setBall] = useState('')
  const [repo, setRepo] = useState('')
  const [query, setQuery] = useState('')
  const [status, setStatus] = useState('')
  const [polling, setPolling] = useState(false)
  if (!data) return <p className="quiet">Loading…</p>
  if (!data.units) return <p className="error">{data.problem}</p>

  const shown = columns.filter((c) => !hidden.has(c.key))
  const repos = [...new Set(data.units.flatMap((u) => u.pull_requests.map(repoOf)))].sort()
  const repoIDs = new Map(data.units.flatMap((u) => u.pull_requests.map((p) => [repoOf(p), p.summary.repo.id] as const)))
  const find = query.trim().toLowerCase()
  let units = data.units.filter(
    (u) =>
      (!ball || u.ball === ball) &&
      (!repo || u.pull_requests.some((p) => repoOf(p) === repo)) &&
      (!find || u.name.toLowerCase().includes(find) || u.pull_requests.some((p) => `${nameOf(p)} ${p.summary.title}`.toLowerCase().includes(find))),
  )
  const sorting = sort && columns.find((c) => c.key === sort.key)
  if (sort && sorting) {
    units = [...units].sort((a, b) => {
      const [x, y] = [sorting.sort(a), sorting.sort(b)]
      const order = x < y ? -1 : x > y ? 1 : 0
      return sort.down ? -order : order
    })
  }
  const choose = (column: string) => {
    const next = toggled(hidden, column)
    localStorage.setItem(hiddenKey, JSON.stringify([...next]))
    setHidden(next)
  }
  const sortBy = (column: string) =>
    setSort((s) => (s?.key !== column ? { key: column, down: false } : s.down ? undefined : { key: column, down: true }))
  // poll asks the forge now for the repository the board shows, or for every repository.
  const poll = async () => {
    const id = repoIDs.get(repo)
    setPolling(true)
    const { error } = await api.POST('/prs/refresh', { params: { query: id === undefined ? {} : { repo: id } } })
    setPolling(false)
    setStatus(error ? problem(error) : asked(id === undefined ? 'every repository' : repo))
  }
  const refreshUnit = async (u: BoardUnit, name: string) => {
    const failed = await refreshPRs(u.pull_requests.map((p) => p.summary.id))
    setStatus(failed || asked(name))
  }
  const refreshButton = (u: BoardUnit, name: string) => (
    <button className="refresh" aria-label={`Refresh ${name}`} title="Refresh from the forge" onClick={() => void refreshUnit(u, name)}>
      ↻
    </button>
  )
  const cells = (u: BoardUnit, p?: BoardPR) =>
    shown.map((c) => (
      <td key={c.key} className={c.wrap ? 'wrap' : undefined}>
        {p === undefined || u.unit.kind === 'pull_request' ? c.unit?.(u) : null}
        {p !== undefined ? c.pr?.(p) : null}
      </td>
    ))

  return (
    <section>
      <h1>Board</h1>
      <div className="board-tools">
        <label>
          Ball{' '}
          <select value={ball} onChange={(e) => setBall(e.target.value)}>
            <option value="">Any</option>
            <option value="yours">Yours</option>
            <option value="others">Others'</option>
            <option value="none">Not yours</option>
          </select>
        </label>
        <label>
          Repository{' '}
          <select value={repo} onChange={(e) => setRepo(e.target.value)}>
            <option value="">Any</option>
            {repos.map((r) => (
              <option key={r} value={r}>
                {r}
              </option>
            ))}
          </select>
        </label>
        <input
          type="search"
          className="find"
          placeholder="Find a unit or pull request"
          aria-label="Find a unit or pull request"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <button onClick={() => setFolded(new Set(units.filter((u) => u.unit.kind === 'group').map(keyOf)))}>Fold all</button>
        <button onClick={() => setFolded(new Set())}>Unfold all</button>
        <button disabled={polling} onClick={() => void poll()}>
          {repoIDs.has(repo) ? `Refresh ${repo}` : 'Refresh all repositories'}
        </button>
        <details className="columns">
          <summary>Columns</summary>
          {columns.map((c) => (
            <label key={c.key}>
              <input type="checkbox" checked={!hidden.has(c.key)} onChange={() => choose(c.key)} /> {c.label}
            </label>
          ))}
        </details>
        <span className="quiet">
          {units.length} of {data.units.length} units
        </span>
        <span role="status" className="quiet">
          {status}
        </span>
      </div>
      {units.length === 0 ? (
        <p className="quiet">No open unit matches.</p>
      ) : (
        <div className="board-wrap">
          <table className="board">
            <thead>
              <tr>
                <th>Unit</th>
                {shown.map((c) => (
                  <th key={c.key} aria-sort={sort?.key !== c.key ? undefined : sort.down ? 'descending' : 'ascending'}>
                    <button className="sort" onClick={() => sortBy(c.key)}>
                      {c.label}
                      {sort?.key === c.key && <span aria-hidden="true">{sort.down ? ' ↓' : ' ↑'}</span>}
                    </button>
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {units.map((u) => {
                if (u.unit.kind === 'pull_request') {
                  const p = u.pull_requests[0]
                  return (
                    <tr key={keyOf(u)} className="alone">
                      <td>
                        <Link to={unitPath(u.unit)}>{nameOf(p)}</Link> {p.summary.title} {refreshButton(u, nameOf(p))}
                      </td>
                      {cells(u, p)}
                    </tr>
                  )
                }
                const open = !folded.has(keyOf(u))
                return [
                  <tr key={keyOf(u)} className="unit">
                    <td>
                      <button
                        className="fold"
                        aria-expanded={open}
                        aria-label={`${open ? 'Fold' : 'Unfold'} ${u.name}`}
                        onClick={() => setFolded(toggled(folded, keyOf(u)))}
                      >
                        {open ? '▾' : '▸'}
                      </button>{' '}
                      <Link to={unitPath(u.unit)}>{u.name}</Link>
                      <span className="badge">{u.pull_requests.length}</span> {refreshButton(u, u.name)}
                      {u.doc_link && (
                        <a className="doc" href={themedLink(u.doc_link, theme)} target="_blank" rel="noopener noreferrer" aria-label={`Description of ${u.name}`}>
                          📄
                        </a>
                      )}
                    </td>
                    {cells(u)}
                  </tr>,
                  ...(open
                    ? u.pull_requests.map((p) => (
                        <tr key={`${keyOf(u)}/${p.summary.id}`} className="pr">
                          <td>
                            <a href={p.summary.url}>{nameOf(p)}</a> {p.summary.title}
                          </td>
                          {cells(u, p)}
                        </tr>
                      ))
                    : []),
                ]
              })}
            </tbody>
          </table>
        </div>
      )}
    </section>
  )
}
