import type { Schemas } from '../api/client'
import { pathText, prName, unitPath, words, type PRs } from '../format'
import { Link } from '../Link'

type Verdict = Schemas['PreviewVerdict']

// VerdictText writes a verdict a preview computed: where the ball is, who holds it and what for, the reminders it
// offers, and, folded, every person's path through the rules, where a rule that reads a judgement nobody has answered
// would ask the model.
function VerdictText({ v, prs }: { v: Verdict; prs: PRs }) {
  return (
    <>
      <div>
        The ball is on the {v.side} side
        {v.fallback && <span className="badge stale">fallback</span>}
      </div>
      <ul>
        {v.holders.map((h) => (
          <li key={h.account.id}>
            <strong>{h.account.login}</strong>:{' '}
            {h.parts.map((p) => `${prName(prs, p.pull_request)} ${words(p.action ?? p.kind)} — ${p.reason}`).join('; ')}
          </li>
        ))}
        {v.reminders.map((r, i) => (
          <li key={`reminder ${i}`} className="quiet">
            {r.account.login} may nudge them: {r.reason}
          </li>
        ))}
      </ul>
      <details>
        <summary>Paths</summary>
        <ul className="path">
          {v.parts.map((p, i) => (
            <li key={i}>
              {p.account.login} on {prName(prs, p.pull_request)}: {pathText(p, 'would ask the model')}
            </li>
          ))}
        </ul>
      </details>
    </>
  )
}

// RulePreview shows what publishing a draft would change, from its preview: every unit whose verdict would change,
// as the version in force concludes now and as the draft would, and every unit the draft would leave to the fallback.
export default function RulePreview({ preview, prs }: { preview: Schemas['RuleSetPreview']; prs: PRs }) {
  const name = (u: Schemas['Unit'], given: string) => given || (u.kind === 'group' ? `group ${u.id}` : prName(prs, u.id))
  const at = new Date(preview.evaluated_at).toLocaleTimeString()
  return (
    <section aria-label="Preview">
      <h2>Preview</h2>
      <p className="quiet">
        {preview.evaluated_units} open units, evaluated at {at} with version {preview.version} and with this draft, from the same facts.
        Nothing is stored, and no model is asked.
      </p>
      <h3>Would change ({preview.changes.length})</h3>
      {preview.changes.length === 0 ? (
        <p className="quiet">No verdict would change.</p>
      ) : (
        <table className="parts preview">
          <thead>
            <tr>
              <th>Unit</th>
              <th>Version {preview.version}</th>
              <th>This draft</th>
            </tr>
          </thead>
          <tbody>
            {preview.changes.map((c) => (
              <tr key={`${c.unit.kind} ${c.unit.id}`}>
                <td>
                  <Link to={unitPath(c.unit)}>{name(c.unit, c.name)}</Link>
                </td>
                <td>
                  <VerdictText v={c.before} prs={prs} />
                </td>
                <td>
                  <VerdictText v={c.after} prs={prs} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <h3>Left to the fallback ({preview.fallback.length})</h3>
      {preview.fallback.length === 0 ? (
        <p className="quiet">The draft leaves no ball to the fallback.</p>
      ) : (
        <ul aria-label="Left to the fallback">
          {preview.fallback.map((f) => (
            <li key={`${f.unit.kind} ${f.unit.id}`}>
              <Link to={unitPath(f.unit)}>{name(f.unit, f.name)}</Link>: the rules find nothing for anyone to do, so{' '}
              {f.verdict.holders.map((h) => h.account.login).join(', ')} {f.verdict.holders.length === 1 ? 'holds' : 'hold'} the ball.
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}
