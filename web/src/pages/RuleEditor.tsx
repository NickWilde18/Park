import type { ReactNode } from 'react'
import type { Schemas } from '../api/client'
import { comparisons, defaultValue, fieldsFor, newTerm, noValue, term, type Catalog, type Role } from '../catalog'
import { words } from '../format'

type Rule = Schemas['Rule']
type Condition = Schemas['Condition']
type Conclusion = Schemas['Conclusion']
type Field = Schemas['Field']

// Item is a rule being edited, with a key that stays with it as rules move.
export type Item = { key: number; rule: Rule }

const opLabels: Record<string, string> = {
  is_true: 'is true', is_false: 'is false', eq: '=', ne: '≠', lt: '<', le: '≤', gt: '>', ge: '≥', is: 'is', is_not: 'is not',
  is_one_of: 'is one of', starts_with: 'starts with', contains: 'contains', is_empty: 'is empty',
  is_not_empty: 'is not empty', is_set: 'is set', is_not_set: 'is not set',
}
const numeric = new Set(['number', 'optional number'])
const accountLists = new Set(['list of accounts', 'list of accounts and teams'])

type Kind = 'term' | 'all' | 'any' | 'expr' | 'always'

function kindOf(c: Condition): Kind {
  if (c.always) return 'always'
  if (c.all !== undefined) return 'all'
  if (c.any !== undefined) return 'any'
  if (c.expr !== undefined) return 'expr'
  return 'term'
}

// joined is all or any of conditions.
function joined(kind: 'all' | 'any', list: Condition[]): Condition {
  return kind === 'all' ? { all: list } : { any: list }
}

// ConditionEditor edits a condition: a term on a field; all or any of other conditions; or, as a whole condition, an
// expression or the catch-all. Changing its form keeps what it can: a term becomes the first of all or any, and all
// becomes any with the same conditions.
function ConditionEditor({ value, change, fields, top }: { value: Condition; change: (c: Condition) => void; fields: Field[]; top: boolean }) {
  const kind = kindOf(value)
  const reform = (to: Kind) => {
    const inner = value.all ?? value.any
    if (to === 'always') change({ always: true })
    else if (to === 'expr') change({ expr: '' })
    else if (to === 'term') change(inner?.[0] && kindOf(inner[0]) === 'term' ? inner[0] : newTerm(fields))
    else change(joined(to, inner ?? [kind === 'term' ? value : newTerm(fields)]))
  }
  return (
    <div className="condition">
      <select aria-label="Condition" value={kind} onChange={(e) => reform(e.target.value as Kind)}>
        <option value="term">a field</option>
        <option value="all">all of</option>
        <option value="any">any of</option>
        {top && <option value="expr">an expression</option>}
        {top && <option value="always">always: the catch-all</option>}
      </select>
      {kind === 'term' && <TermEditor value={value} change={change} fields={fields} />}
      {kind === 'expr' && (
        <textarea aria-label="Expression" className="expr" rows={2} value={value.expr} onChange={(e) => change({ expr: e.target.value })} />
      )}
      {(kind === 'all' || kind === 'any') && (
        <ConditionList key={kind} kind={kind} list={value[kind] ?? []} change={(list) => change(joined(kind, list))} fields={fields} />
      )}
    </div>
  )
}

// ConditionList edits the conditions of all or any: each one, removable, and one more added.
function ConditionList({ kind, list, change, fields }: { kind: 'all' | 'any'; list: Condition[]; change: (l: Condition[]) => void; fields: Field[] }) {
  return (
    <div className="conditions">
      <ol aria-label={kind === 'all' ? 'All of' : 'Any of'}>
        {list.map((c, i) => (
          <li key={i}>
            <ConditionEditor value={c} change={(next) => change(list.map((x, j) => (j === i ? next : x)))} fields={fields} top={false} />
            <button type="button" onClick={() => change(list.filter((_, j) => j !== i))}>
              Remove condition {i + 1}
            </button>
          </li>
        ))}
      </ol>
      <button type="button" onClick={() => change([...list, newTerm(fields)])}>
        Add a condition
      </button>
    </div>
  )
}

// TermEditor edits a term: its field, its op, which the field's type allows, and the value the op takes — none, a
// whole number or another number field, the choices, or text.
function TermEditor({ value, change, fields }: { value: Condition; change: (c: Condition) => void; fields: Field[] }) {
  const f = fields.find((x) => x.name === value.field)
  const op = value.op ?? ''
  const set = (v: unknown) => change({ field: value.field, op, value: v })
  let input: ReactNode = null
  if (comparisons.has(op)) {
    const other = typeof value.value === 'object' && value.value !== null ? String((value.value as { field?: unknown }).field ?? '') : ''
    input = (
      <>
        <select aria-label="Compare with" value={other} onChange={(e) => set(e.target.value ? { field: e.target.value } : 0)}>
          <option value="">a number</option>
          {fields
            .filter((x) => numeric.has(x.type) && x.name !== value.field)
            .map((x) => (
              <option key={x.name} value={x.name}>
                {x.name}
              </option>
            ))}
        </select>
        {!other && (
          <input
            type="number"
            step={1}
            aria-label="Number"
            value={typeof value.value === 'number' ? value.value : 0}
            onChange={(e) => set(Number(e.target.value))}
          />
        )}
      </>
    )
  } else if (op === 'is_one_of') {
    const chosen = Array.isArray(value.value) ? value.value.map(String) : []
    input = (
      <span role="group" aria-label="Values">
        {(f?.choices ?? []).map((c) => (
          <label key={c} className="choice">
            <input
              type="checkbox"
              checked={chosen.includes(c)}
              onChange={(e) => set(e.target.checked ? [...chosen, c] : chosen.filter((x) => x !== c))}
            />{' '}
            {c}
          </label>
        ))}
      </span>
    )
  } else if (!noValue.has(op)) {
    input = f?.choices ? (
      <select aria-label="Value" value={String(value.value ?? '')} onChange={(e) => set(e.target.value)}>
        {f.choices.map((c) => (
          <option key={c} value={c}>
            {c}
          </option>
        ))}
      </select>
    ) : (
      <input aria-label="Value" value={String(value.value ?? '')} onChange={(e) => set(e.target.value)} />
    )
  }
  return (
    <>
      <select
        aria-label="Field"
        value={value.field ?? ''}
        title={f?.meaning}
        onChange={(e) => {
          const next = fields.find((x) => x.name === e.target.value)
          if (next) change(term(next, op))
        }}
      >
        {!f && <option value={value.field ?? ''}>{value.field || 'choose a field'}</option>}
        {fields.map((x) => (
          <option key={x.name} value={x.name}>
            {x.name}
          </option>
        ))}
      </select>
      <select aria-label="Test" value={op} onChange={(e) => change({ field: value.field, op: e.target.value, value: defaultValue(f, e.target.value) })}>
        {!f?.operators.includes(op) && <option value={op}>{op || 'choose a test'}</option>}
        {(f?.operators ?? []).map((o) => (
          <option key={o} value={o}>
            {opLabels[o] ?? words(o)}
          </option>
        ))}
      </select>
      {input}
    </>
  )
}

// ConclusionEditor edits what a rule concludes: the part's kind, which a reviewer's list keeps to act or idle; the
// action, for act, or merge or none, for finish; whom request_review hands back to; the priority; the reason; whether
// an idle part offers a reminder; and whether the part may become a task, for the actions that can. Changing the kind
// or the action drops what the new one does not take.
function ConclusionEditor({ value, change, role, catalog }: { value: Conclusion; change: (c: Conclusion) => void; role: Role; catalog: Catalog }) {
  const actions = catalog.actions.filter((a) => a.role === role)
  const action = catalog.actions.find((a) => a.name === value.action)
  const kinds = role === 'author' ? ['act', 'hold', 'finish', 'idle'] : ['act', 'idle']
  const targets = catalog.fields.filter((f) => accountLists.has(f.type))
  const tidy = (c: Conclusion): Conclusion => {
    const a = catalog.actions.find((x) => x.name === c.action)
    return {
      kind: c.kind,
      action: c.action || undefined,
      targets: c.action === 'request_review' ? c.targets || undefined : undefined,
      priority: c.priority === 'low' ? 'low' : undefined,
      reason: c.reason,
      remind: c.kind === 'idle' && c.remind ? true : undefined,
      task: a?.task && c.task ? true : undefined,
    }
  }
  const setKind = (kind: string) => {
    let next = value.action
    if (kind === 'act') next = actions.some((a) => a.name === next) ? next : actions[0]?.name
    else if (kind === 'finish') next = next === 'merge' ? next : undefined
    else next = undefined
    change(tidy({ ...value, kind, action: next }))
  }
  return (
    <div className="conclusion">
      <label>
        Part{' '}
        <select value={value.kind} onChange={(e) => setKind(e.target.value)}>
          {!kinds.includes(value.kind) && <option value={value.kind}>{value.kind}</option>}
          {kinds.map((k) => (
            <option key={k} value={k}>
              {k}
            </option>
          ))}
        </select>
      </label>
      {(value.kind === 'act' || value.kind === 'finish') && (
        <label>
          Action{' '}
          <select value={value.action ?? ''} onChange={(e) => change(tidy({ ...value, action: e.target.value }))}>
            {value.kind === 'finish' ? (
              <>
                <option value="">none</option>
                <option value="merge">merge</option>
              </>
            ) : (
              actions.map((a) => (
                <option key={a.name} value={a.name}>
                  {words(a.name)}
                </option>
              ))
            )}
          </select>
        </label>
      )}
      {value.action === 'request_review' && (
        <label>
          Hand back to{' '}
          <select value={value.targets ?? ''} onChange={(e) => change(tidy({ ...value, targets: e.target.value }))}>
            <option value="">the reviewers already asked</option>
            {targets.map((f) => (
              <option key={f.name} value={f.name}>
                {f.name}
              </option>
            ))}
          </select>
        </label>
      )}
      <label>
        Priority{' '}
        <select value={value.priority ?? 'normal'} onChange={(e) => change(tidy({ ...value, priority: e.target.value }))}>
          <option value="normal">normal</option>
          <option value="low">low</option>
        </select>
      </label>
      <label className="reason">
        Reason <input value={value.reason} onChange={(e) => change({ ...value, reason: e.target.value })} />
      </label>
      {value.kind === 'idle' && (
        <label>
          <input type="checkbox" checked={Boolean(value.remind)} onChange={(e) => change(tidy({ ...value, remind: e.target.checked }))} /> Offer
          a reminder to nudge
        </label>
      )}
      {action?.task && (
        <label>
          <input type="checkbox" checked={Boolean(value.task)} onChange={(e) => change(tidy({ ...value, task: e.target.checked }))} /> May
          become a task
        </label>
      )}
    </div>
  )
}

type CardProps = {
  role: Role
  number: number
  count: number
  rule: Rule
  catalog: Catalog
  problems: Schemas['Problem'][]
  change: (r: Rule) => void
  move: (by: number) => void
  remove: () => void
  insert: () => void
}

// RuleCard edits one rule: its name, the label a path shows when it does not match, its condition, and its
// conclusion, with the problems saving found in it. It moves up or down its list, is removed, or has a rule added
// above it.
export function RuleCard({ role, number, count, rule, catalog, problems, change, move, remove, insert }: CardProps) {
  return (
    <fieldset className="rule-edit">
      <legend>Rule {number}</legend>
      <div className="row">
        <label>
          Name <input value={rule.name} onChange={(e) => change({ ...rule, name: e.target.value })} />
        </label>
        <label>
          Label when passed over{' '}
          <input value={rule.passed_over ?? ''} onChange={(e) => change({ ...rule, passed_over: e.target.value || undefined })} />
        </label>
      </div>
      <fieldset>
        <legend>When</legend>
        <ConditionEditor value={rule.when} change={(when) => change({ ...rule, when })} fields={fieldsFor(catalog, role)} top />
      </fieldset>
      <fieldset>
        <legend>Then</legend>
        <ConclusionEditor value={rule.then} change={(then) => change({ ...rule, then })} role={role} catalog={catalog} />
      </fieldset>
      {problems.length > 0 && (
        <ul className="error" aria-label="Problems">
          {problems.map((p, i) => (
            <li key={i}>{p.message}</li>
          ))}
        </ul>
      )}
      <div className="row">
        <button type="button" disabled={number === 1} onClick={() => move(-1)}>
          Move up
        </button>
        <button type="button" disabled={number === count} onClick={() => move(1)}>
          Move down
        </button>
        <button type="button" onClick={insert}>
          Add a rule above
        </button>
        <button type="button" onClick={remove}>
          Remove
        </button>
      </div>
    </fieldset>
  )
}
