import { useState } from 'react'
import { api, problem, type Schemas } from '../api/client'
import { useFetch } from '../fetch'
import { conditionText, since, words } from '../format'
import { Link } from '../Link'
import { navigate } from '../router'
import { useSession } from '../sessionContext'

// RuleTree draws each role's list as its first-match tree, the passed-over label on each edge. A rule whose condition
// is an expression is marked as one.
export function RuleTree({ rules }: { rules: Schemas['RuleSet'] }) {
  return (
    <>
      {(['author', 'reviewer'] as const).map((role) => (
        <div key={role}>
          <h2>{role}</h2>
          <ol className="tree">
            {rules[role].map((r, i) => (
              <li key={i}>
                <div className="rule">
                  <strong>{r.name}</strong> {r.when.expr !== undefined && <span className="tag gray">expression</span>}{' '}
                  <code>{conditionText(r.when)}</code> →{' '}
                  {[words(r.then.kind), words(r.then.action), r.then.priority === 'low' ? 'low' : '', r.then.remind ? 'remind' : '']
                    .filter(Boolean)
                    .join(' · ')}
                  <span className="quiet"> — {r.then.reason}</span>
                </div>
                {i < rules[role].length - 1 && <div className="edge">↓ {r.passed_over ?? ''}</div>}
              </li>
            ))}
          </ol>
        </div>
      ))}
    </>
  )
}

// The rule set in force, drawn as its trees. A member with the rules permission also sees the drafts not yet
// published, and starts one, from the version in force or from a rule-set file.
export default function Rules() {
  const { me } = useSession()
  const [notice, setNotice] = useState('')
  const { data } = useFetch(async () => {
    const list = await api.GET('/rulesets')
    const latest = list.data?.versions[0]
    if (!latest) return { problem: problem(list.error) }
    const [v, d] = await Promise.all([
      api.GET('/rulesets/{version}', { params: { path: { version: latest.version } } }),
      me.member.rules ? api.GET('/rulesets/drafts') : null,
    ])
    return { version: v.data, drafts: d?.data?.drafts ?? [], problem: v.error ? problem(v.error) : '' }
  })
  const start = async (rules?: Schemas['RuleSetDocument']) => {
    const { data, error } = await api.POST('/rulesets/drafts', { body: rules ? { rules } : {} })
    if (error) setNotice(problem(error))
    else navigate(`/rules/drafts/${data.id}`)
  }
  const importFile = async (file: File) => {
    let rules: unknown
    try {
      rules = JSON.parse(await file.text())
    } catch {
      rules = null
    }
    if (!rules || typeof rules !== 'object' || Array.isArray(rules)) {
      setNotice(`${file.name} is not a rule-set file: one JSON object, with an author and a reviewer list.`)
      return
    }
    await start(rules as Schemas['RuleSetDocument'])
  }
  if (!data) return <p className="quiet">Loading…</p>
  const version = data.version
  if (!version) return <p className="error">{data.problem}</p>
  return (
    <section>
      <h1>Rules, version {version.version}</h1>
      {me.member.rules && (
        <section aria-label="Drafts">
          <h2>Drafts</h2>
          {notice && <p className="error">{notice}</p>}
          {data.drafts.length === 0 ? (
            <p className="quiet">No drafts.</p>
          ) : (
            <ul>
              {data.drafts.map((d) => {
                const n = d.problems.filter((p) => p.code !== 'stale_draft').length
                return (
                  <li key={d.id}>
                    <Link to={`/rules/drafts/${d.id}`}>Draft {d.id}</Link>
                    <span className="quiet">
                      {d.base_version ? `, from version ${d.base_version}` : ''}, started by {d.created_by_name}, changed {since(d.updated_at)} ago
                    </span>
                    {d.problems.some((p) => p.code === 'stale_draft') && <span className="badge stale">behind version {version.version}</span>}
                    {n > 0 && <span className="badge stale">{n === 1 ? '1 problem' : `${n} problems`}</span>}
                  </li>
                )
              })}
            </ul>
          )}
          <p>
            <button type="button" onClick={() => void start()}>
              Start a draft from version {version.version}
            </button>{' '}
            <label className="inline">
              Import a rule-set file{' '}
              <input
                type="file"
                accept="application/json,.json"
                onChange={(e) => {
                  const file = e.target.files?.[0]
                  if (file) void importFile(file)
                }}
              />
            </label>
          </p>
        </section>
      )}
      <RuleTree rules={version.rules} />
    </section>
  )
}
