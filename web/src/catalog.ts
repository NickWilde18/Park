import type { Schemas } from './api/client'

export type Role = 'author' | 'reviewer'
type Field = Schemas['Field']
type Condition = Schemas['Condition']

// Catalog is what the rule editor offers: the fields a condition reads, the questions judgements answer, and the
// actions a part names, as Park lists them.
export type Catalog = { fields: Field[]; questions: Schemas['Question'][]; actions: Schemas['Action'][] }

// The ops that take no value, and the ones that compare numbers.
export const noValue = new Set(['is_true', 'is_false', 'is_set', 'is_not_set', 'is_empty', 'is_not_empty'])
export const comparisons = new Set(['eq', 'ne', 'lt', 'le', 'gt', 'ge'])

// fieldsFor are the fields a role's conditions may read: every field, but the judgements of questions asked for the
// other role.
export function fieldsFor(catalog: Catalog, role: Role): Field[] {
  const foreign = new Set(catalog.questions.filter((q) => q.role !== role).map((q) => `judgement.${q.name}`))
  return catalog.fields.filter((f) => !foreign.has(f.name))
}

// defaultValue is the value a term starts with for its field and op: none, 0, no choices, the first choice, or text.
export function defaultValue(f: Field | undefined, op: string): unknown {
  if (noValue.has(op)) return undefined
  if (comparisons.has(op)) return 0
  if (op === 'is_one_of') return []
  return f?.choices?.[0] ?? ''
}

// term is a condition on a field, with the op given if the field takes it, and the field's first op if not.
export function term(f: Field, op?: string): Condition {
  const chosen = op && f.operators.includes(op) ? op : f.operators[0]
  return { field: f.name, op: chosen, value: defaultValue(f, chosen) }
}

// newTerm is the term a new condition starts as: on the first boolean field.
export function newTerm(fields: Field[]): Condition {
  return term(fields.find((f) => f.type === 'boolean') ?? fields[0])
}

// newRule is a rule as it is added: a name to change, a term, and an idle part with a reason to write.
export function newRule(catalog: Catalog, role: Role): Schemas['Rule'] {
  return { name: 'New rule', when: newTerm(fieldsFor(catalog, role)), then: { kind: 'idle', reason: 'Say why' } }
}

let keys = 0

// newKey is a key for a rule being edited, unique on the page.
export function newKey(): number {
  return keys++
}
