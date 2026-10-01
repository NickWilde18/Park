import { useState } from 'react'
import { api, problem, type Schemas } from '../api/client'
import { newKey, newRule, type Catalog, type Role } from '../catalog'
import { useFetch } from '../fetch'
import { since, type PRs } from '../format'
import { Link } from '../Link'
import { navigate } from '../router'
import { RuleCard, type Item } from './RuleEditor'
import RulePreview from './RulePreview'
import { RuleTree } from './Rules'

const roles: Role[] = ['author', 'reviewer']
type Lists = Record<Role, Item[]>
type Draft = Schemas['RuleSetDraft']

// load fetches a draft, with what its editor offers, and the open pull requests a preview names.
async function load(id: number) {
  const [d, f, q, a, p] = await Promise.all([
    api.GET('/rulesets/drafts/{id}', { params: { path: { id } } }),
    api.GET('/fields'),
    api.GET('/questions'),
    api.GET('/actions'),
    api.GET('/prs'),
  ])
  const catalog: Catalog = { fields: f.data?.fields ?? [], questions: q.data?.questions ?? [], actions: a.data?.actions ?? [] }
  const prs: PRs = new Map((p.data?.pull_requests ?? []).map((x) => [x.id, x]))
  return { draft: d.data, problem: d.error ? problem(d.error) : '', catalog, prs }
}

// RuleDraft is a draft's page: its editor while it is a draft, and its rules once it is published.
export default function RuleDraft({ id }: { id: number }) {
  const { data } = useFetch(() => load(id), String(id))
  if (!data) return <p className="quiet">Loading…</p>
  if (!data.draft) return <p className="error">{data.problem}</p>
  return <Editor key={data.draft.id} initial={data.draft} catalog={data.catalog} prs={data.prs} />
}

// moved is a list with the item of a key moved by one place, up or down.
function moved(items: Item[], key: number, by: number): Item[] {
  const i = items.findIndex((x) => x.key === key)
  const j = i + by
  if (i < 0 || j < 0 || j >= items.length) return items
  const out = [...items]
  ;[out[i], out[j]] = [out[j], out[i]]
  return out
}

function title(role: Role): string {
  return role === 'author' ? 'Author' : 'Reviewer'
}

// Editor edits a draft: each role's rules, added, removed, reordered, and changed in place. Saving replaces the
// draft's rules, and shows each problem on the rule it is about, even after the rules move. A draft that another
// version was published after is brought up to date, once its editor has taken that version's changes in, so that
// publishing does not undo them. The preview runs on the draft as saved, saving it first. Publishing asks once
// more, and needs a draft saved with no problems.
function Editor({ initial, catalog, prs }: { initial: Draft; catalog: Catalog; prs: PRs }) {
  const keyed = (rules: Schemas['Rule'][]) => rules.map((rule) => ({ key: newKey(), rule }))
  const [lists, setLists] = useState<Lists>(() => ({ author: keyed(initial.rules.author), reviewer: keyed(initial.rules.reviewer) }))
  const [saved, setSaved] = useState(initial)
  // savedKeys are the rules' keys as they were saved, which a problem's rule number counts.
  const [savedKeys, setSavedKeys] = useState(() => ({ author: lists.author.map((i) => i.key), reviewer: lists.reviewer.map((i) => i.key) }))
  // edits counts the changes made; savedEdits and previewedEdits, how many there were when the draft was last saved
  // and previewed.
  const [edits, setEdits] = useState(0)
  const [savedEdits, setSavedEdits] = useState(0)
  const [previewedEdits, setPreviewedEdits] = useState(0)
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState(false)
  const [preview, setPreview] = useState<Schemas['RuleSetPreview'] | null>(null)
  const [confirming, setConfirming] = useState(false)
  const dirty = edits !== savedEdits
  const path = { params: { path: { id: saved.id } } }

  const edit = (role: Role, change: (items: Item[]) => Item[]) => {
    setLists((l) => ({ ...l, [role]: change(l[role]) }))
    setEdits((n) => n + 1)
    setConfirming(false)
  }
  const save = async (base?: number): Promise<Draft | null> => {
    const at = edits
    const keys = { author: lists.author.map((i) => i.key), reviewer: lists.reviewer.map((i) => i.key) }
    const rules = { author: lists.author.map((i) => i.rule), reviewer: lists.reviewer.map((i) => i.rule) }
    const { data, error } = await api.PUT('/rulesets/drafts/{id}', { ...path, body: { rules, base_version: base } })
    if (error) {
      setNotice(problem(error))
      return null
    }
    setSaved(data)
    setSavedKeys(keys)
    setSavedEdits(at)
    const n = data.problems.length
    setNotice(n ? `Saved, with ${n} ${n === 1 ? 'problem' : 'problems'}.` : 'Saved, with no problems.')
    return data
  }
  const run = async (step: () => Promise<unknown>) => {
    setBusy(true)
    try {
      await step()
    } finally {
      setBusy(false)
    }
  }
  const runPreview = async () => {
    const at = edits
    if (dirty && !(await save())) return
    const { data, error } = await api.POST('/rulesets/drafts/{id}/preview', path)
    if (error) {
      setNotice(problem(error))
      setPreview(null)
      return
    }
    setPreview(data)
    setPreviewedEdits(at)
  }
  const bringUpToDate = async () => {
    const { data } = await api.GET('/rulesets')
    const current = data?.versions[0]?.version
    if (current) await save(current)
  }
  const publish = async () => {
    const { error } = await api.POST('/rulesets/drafts/{id}/publish', path)
    if (error) {
      setNotice(problem(error))
      setConfirming(false)
      return
    }
    navigate('/rules')
  }

  const heading = (
    <>
      <p>
        <Link to="/rules">Rules</Link>
      </p>
      <h1>
        Draft {saved.id}
        {saved.base_version ? `, from version ${saved.base_version}` : ''}
      </h1>
      <p className="quiet">
        Started by {saved.created_by_name}; changed {since(saved.updated_at)} ago.
      </p>
    </>
  )
  if (saved.published_version) {
    return (
      <section>
        {heading}
        <p className="notice">
          Published as <Link to="/rules">version {saved.published_version}</Link>.
        </p>
        <RuleTree rules={saved.rules} />
      </section>
    )
  }
  const stale = saved.problems.find((p) => p.code === 'stale_draft')
  const ofRule = (role: Role, p: Schemas['Problem']) => p.role === role && p.rule !== undefined
  // A problem whose rule was removed since saving is shown with its list, under the number it had.
  const unplaced = (role: Role) =>
    saved.problems.filter((p) => ofRule(role, p) && !lists[role].some((i) => i.key === savedKeys[role][(p.rule ?? 0) - 1]))
  const upcoming = (saved.base_version ?? 0) + 1
  return (
    <section>
      {heading}
      {notice && (
        <p className="notice" role="status">
          {notice}
        </p>
      )}
      {stale && (
        <section className="behind" aria-label="Behind the version in force">
          <p>{stale.message}</p>
          <p>
            Take that version's changes into this draft, then:{' '}
            <button type="button" disabled={busy} onClick={() => void run(bringUpToDate)}>
              Bring this draft up to date
            </button>
          </p>
        </section>
      )}
      {saved.problems
        .filter((p) => !p.role && p.code !== 'stale_draft')
        .map((p, i) => (
          <p key={i} className="error">
            {p.message}
          </p>
        ))}
      {roles.map((role) => (
        <section key={role} aria-label={`${title(role)} rules`}>
          <h2>{title(role)}</h2>
          {[...saved.problems.filter((p) => p.role === role && p.rule === undefined), ...unplaced(role)].map((p, i) => (
            <p key={i} className="error">
              {p.rule ? `Rule ${p.rule}, as saved: ` : ''}
              {p.message}
            </p>
          ))}
          <ol className="rule-list">
            {lists[role].map((item, i) => (
              <li key={item.key}>
                <RuleCard
                  role={role}
                  number={i + 1}
                  count={lists[role].length}
                  rule={item.rule}
                  catalog={catalog}
                  problems={saved.problems.filter((p) => ofRule(role, p) && savedKeys[role][(p.rule ?? 0) - 1] === item.key)}
                  change={(rule) => edit(role, (items) => items.map((x) => (x.key === item.key ? { ...x, rule } : x)))}
                  move={(by) => edit(role, (items) => moved(items, item.key, by))}
                  remove={() => edit(role, (items) => items.filter((x) => x.key !== item.key))}
                  insert={() =>
                    edit(role, (items) => {
                      const at = items.findIndex((x) => x.key === item.key)
                      return [...items.slice(0, at), { key: newKey(), rule: newRule(catalog, role) }, ...items.slice(at)]
                    })
                  }
                />
              </li>
            ))}
          </ol>
          {lists[role].length === 0 && (
            <button type="button" onClick={() => edit(role, () => [{ key: newKey(), rule: newRule(catalog, role) }])}>
              Add a rule
            </button>
          )}
        </section>
      ))}
      <div className="draft-actions">
        <button type="button" disabled={busy} onClick={() => void run(save)}>
          Save
        </button>
        <button type="button" disabled={busy} onClick={() => void run(runPreview)}>
          Preview
        </button>
        {confirming ? (
          <>
            <span>Publish this draft as version {upcoming}? Every open unit is evaluated again with it.</span>
            <button type="button" disabled={busy} onClick={() => void run(publish)}>
              Publish version {upcoming}
            </button>
            <button type="button" onClick={() => setConfirming(false)}>
              Cancel
            </button>
          </>
        ) : (
          <button type="button" disabled={busy || dirty || saved.problems.length > 0} onClick={() => setConfirming(true)}>
            Publish
          </button>
        )}
        <span className="quiet">
          {dirty ? 'Unsaved changes.' : saved.problems.length > 0 ? 'Fix the problems to publish.' : ''}
        </span>
      </div>
      {preview && (
        <>
          {previewedEdits !== edits && <p className="quiet">The draft has changed since this preview.</p>}
          <RulePreview preview={preview} prs={prs} />
        </>
      )}
    </section>
  )
}
