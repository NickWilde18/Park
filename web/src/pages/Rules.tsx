import { api, problem } from '../api/client'
import { useFetch } from '../fetch'
import { conditionText, words } from '../format'

// The rule set in force: each role's list drawn as its first-match tree, the passed-over label on each edge.
export default function Rules() {
  const { data } = useFetch(async () => {
    const list = await api.GET('/rulesets')
    const latest = list.data?.versions[0]
    if (!latest) return { problem: problem(list.error) }
    const { data, error } = await api.GET('/rulesets/{version}', { params: { path: { version: latest.version } } })
    return { version: data, problem: error ? problem(error) : '' }
  })
  if (!data) return <p className="quiet">Loading…</p>
  const version = data.version
  if (!version) return <p className="error">{data.problem}</p>
  return (
    <section>
      <h1>Rules, version {version.version}</h1>
      {(['author', 'reviewer'] as const).map((role) => (
        <div key={role}>
          <h2>{role}</h2>
          <ol className="tree">
            {version.rules[role].map((r, i) => (
              <li key={i}>
                <div className="rule">
                  <strong>{r.name}</strong> <code>{conditionText(r.when)}</code> →{' '}
                  {[words(r.then.kind), words(r.then.action), r.then.priority === 'low' ? 'low' : '', r.then.remind ? 'remind' : '']
                    .filter(Boolean)
                    .join(' · ')}
                  <span className="quiet"> — {r.then.reason}</span>
                </div>
                {i < version.rules[role].length - 1 && <div className="edge">↓ {r.passed_over ?? ''}</div>}
              </li>
            ))}
          </ol>
        </div>
      ))}
    </section>
  )
}
