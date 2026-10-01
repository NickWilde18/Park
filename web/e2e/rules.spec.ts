import { expect, test, type Page } from '@playwright/test'
import { call, signIn } from './session.ts'

// rule is a rule's card in a role's list, by its number.
function rule(page: Page, role: 'Author' | 'Reviewer', n: number) {
  return page.getByRole('region', { name: `${role} rules` }).getByRole('group', { name: `Rule ${n}`, exact: true })
}

// Ana, who publishes rules, starts a draft from version 1 and changes it: the failing-checks rule's reason, first
// reviews made to wait, and a reviewer rule above the others that reads a judgement. Previewing saves the draft, then
// shows the Billing group's verdict changing in its reason, with that rule unknown in Ben's path since no model is
// asked, and Carol's and Ana's pull requests awaiting Ben's first review left to the fallback. A condition that does not
// compile is a problem shown on its rule, which keeps the draft from being previewed or published, until the condition
// is built again from its field, test, and value. A rule moved down stays moved once saved, and moves back; the added
// rule is removed. The draft is not published, so the rules the later tests read stay as they were.
test('a member with the rules permission edits a draft, sees its problems, and previews it', async ({ page }) => {
  await signIn(page, 'ana')
  await page.getByRole('link', { name: 'Rules' }).click()
  await expect(page.getByRole('heading', { name: 'Rules, version 1' })).toBeVisible()
  const drafts = page.getByRole('region', { name: 'Drafts' })
  await expect(drafts.getByText('No drafts.')).toBeVisible()
  await drafts.getByRole('button', { name: 'Start a draft from version 1' }).click()
  await expect(page.getByRole('heading', { name: 'Draft 1, from version 1' })).toBeVisible()

  await expect(rule(page, 'Author', 3).getByLabel('Name')).toHaveValue('Checks failing')
  await rule(page, 'Author', 3).getByLabel('Reason').fill('Fix the failing checks first')
  const first = rule(page, 'Reviewer', 6)
  await expect(first.getByLabel('Name')).toHaveValue('First review')
  await first.getByLabel('Part').selectOption('idle')
  await first.getByLabel('Reason').fill('First reviews wait')
  await rule(page, 'Reviewer', 1).getByRole('button', { name: 'Add a rule above' }).click()
  const blocking = rule(page, 'Reviewer', 1)
  await expect(blocking.getByLabel('Name')).toHaveValue('New rule')
  await expect(rule(page, 'Reviewer', 2).getByLabel('Name')).toHaveValue('Draft')
  await blocking.getByLabel('Name').fill('Blocking comments')
  await blocking.getByLabel('Condition').selectOption('expr')
  await blocking.getByLabel('Expression').fill("judgement.comments_blocking == 'yes'")
  await blocking.getByLabel('Part').selectOption('act')
  await expect(blocking.getByLabel('Action')).toHaveValue('review')
  await blocking.getByLabel('Reason').fill('Your comments block')

  await page.getByRole('button', { name: 'Preview' }).click()
  await expect(page.getByRole('status')).toHaveText('Saved, with no problems.')
  const preview = page.getByRole('region', { name: 'Preview' })
  await expect(preview.getByRole('heading', { name: 'Would change (3)' })).toBeVisible()
  const group = (await call(page, 'GET', '/groups/1')).body as { name: string }
  const billing = preview.getByRole('row').filter({ has: page.getByRole('link', { name: group.name, exact: true }) })
  await expect(billing.getByRole('cell').nth(1)).toContainText('1 required checks failing')
  await expect(billing.getByRole('cell').nth(2)).toContainText('Fix the failing checks first')
  await expect(billing.getByRole('cell').nth(2)).toContainText('Blocking comments · would ask the model')
  await expect(preview.getByRole('heading', { name: 'Left to the fallback (2)' })).toBeVisible()
  const fallback = preview.getByRole('list', { name: 'Left to the fallback' }).getByRole('listitem')
  await expect(fallback).toHaveCount(2)
  await expect(fallback.filter({ hasText: 'Invoice layout' })).toContainText('nothing for anyone to do, so carol holds the ball')
  await expect(fallback.filter({ hasText: 'Statement emails' })).toContainText('nothing for anyone to do, so ana holds the ball')

  const failing = rule(page, 'Author', 3)
  await failing.getByLabel('Condition').selectOption('expr')
  await failing.getByLabel('Expression').fill('pr.checks ==')
  await page.getByRole('button', { name: 'Save' }).click()
  await expect(page.getByRole('status')).toHaveText('Saved, with 1 problem.')
  await expect(failing.getByRole('list', { name: 'Problems' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Publish', exact: true })).toBeDisabled()
  await page.getByRole('button', { name: 'Preview' }).click()
  await expect(page.getByRole('status')).toContainText("Fix the draft's problems to preview it.")
  await failing.getByLabel('Condition').selectOption('term')
  await failing.getByLabel('Field').selectOption('pr.checks')
  await expect(failing.getByLabel('Test')).toHaveValue('is')
  await failing.getByLabel('Value').selectOption('failing')
  await page.getByRole('button', { name: 'Save' }).click()
  await expect(page.getByRole('status')).toHaveText('Saved, with no problems.')
  await expect(failing.getByRole('list', { name: 'Problems' })).toHaveCount(0)

  await rule(page, 'Author', 1).getByRole('button', { name: 'Move down' }).click()
  await expect(rule(page, 'Author', 1).getByLabel('Name')).toHaveValue('Conflicts')
  await page.getByRole('button', { name: 'Save' }).click()
  await expect(page.getByRole('status')).toHaveText('Saved, with no problems.')
  await page.reload()
  await expect(rule(page, 'Author', 1).getByLabel('Name')).toHaveValue('Conflicts')
  await expect(rule(page, 'Author', 2).getByLabel('Name')).toHaveValue('Draft')
  await expect(rule(page, 'Author', 3).getByLabel('Reason')).toHaveValue('Fix the failing checks first')
  await rule(page, 'Author', 2).getByRole('button', { name: 'Move up' }).click()
  await rule(page, 'Reviewer', 1).getByRole('button', { name: 'Remove' }).click()
  await expect(rule(page, 'Reviewer', 1).getByLabel('Name')).toHaveValue('Draft')
  await page.getByRole('button', { name: 'Save' }).click()
  await expect(page.getByRole('status')).toHaveText('Saved, with no problems.')
  const saved = (await call(page, 'GET', '/rulesets/drafts/1')).body as { rules: { author: { name: string }[]; reviewer: { name: string }[] } }
  expect(saved.rules.author[0].name).toBe('Draft')
  expect(saved.rules.reviewer[0].name).toBe('Draft')
  expect(saved.rules.reviewer).toHaveLength(19)
})

// A draft started and published unchanged, after Ana confirms, becomes version 2, and the Rules page shows it. The
// draft she changed before is now behind it; brought up to date, it is no longer. A file that is not a rule set is
// refused, and one that is starts a draft of its own, with the file's rules: here an expression that never matches,
// first. Published as version 3, it changes no verdict, and the tree marks the rule as an expression.
test('publishing a draft puts it in force, and an older draft is brought up to date', async ({ page }) => {
  await signIn(page, 'ana')
  await page.goto('/rules')
  await page.getByRole('button', { name: 'Start a draft from version 1' }).click()
  await expect(page.getByRole('heading', { name: 'Draft 2, from version 1' })).toBeVisible()
  await page.getByRole('button', { name: 'Publish', exact: true }).click()
  await expect(page.getByText('Publish this draft as version 2? Every open unit is evaluated again with it.')).toBeVisible()
  await page.getByRole('button', { name: 'Publish version 2' }).click()
  await expect(page.getByRole('heading', { name: 'Rules, version 2' })).toBeVisible()

  const drafts = page.getByRole('region', { name: 'Drafts' })
  await expect(drafts.getByRole('listitem')).toHaveCount(1)
  await expect(drafts.getByRole('listitem')).toContainText('behind version 2')
  await drafts.getByRole('link', { name: 'Draft 1' }).click()
  const behind = page.getByRole('region', { name: 'Behind the version in force' })
  await expect(behind).toContainText('version 2 is published now')
  await behind.getByRole('button', { name: 'Bring this draft up to date' }).click()
  await expect(page.getByRole('status')).toHaveText('Saved, with no problems.')
  await expect(behind).toHaveCount(0)
  await expect(page.getByRole('heading', { name: 'Draft 1, from version 2' })).toBeVisible()

  await page.getByRole('link', { name: 'Rules', exact: true }).first().click()
  const file = page.getByLabel('Import a rule-set file')
  await file.setInputFiles({ name: 'notes.txt', mimeType: 'text/plain', buffer: Buffer.from('not json') })
  await expect(page.getByText('notes.txt is not a rule-set file')).toBeVisible()
  const v2 = (await call(page, 'GET', '/rulesets/2')).body as { rules: { author: unknown[]; reviewer: unknown[] } }
  const never = { name: 'Never matches', when: { expr: 'pr.approvals < 0' }, then: { kind: 'hold', reason: 'Never' } }
  const imported = { author: [never, ...v2.rules.author], reviewer: v2.rules.reviewer }
  await file.setInputFiles({ name: 'rules.json', mimeType: 'application/json', buffer: Buffer.from(JSON.stringify(imported)) })
  await expect(page.getByRole('heading', { name: 'Draft 3, from version 2' })).toBeVisible()
  await expect(rule(page, 'Author', 1).getByLabel('Name')).toHaveValue('Never matches')
  await expect(rule(page, 'Author', 1).getByLabel('Expression')).toHaveValue('pr.approvals < 0')
  await page.getByRole('button', { name: 'Publish', exact: true }).click()
  await page.getByRole('button', { name: 'Publish version 3' }).click()
  await expect(page.getByRole('heading', { name: 'Rules, version 3' })).toBeVisible()
  const tree = page.locator('.tree .rule').filter({ hasText: 'Never matches' })
  await expect(tree.getByText('expression', { exact: true })).toBeVisible()
  await expect(tree.getByText('pr.approvals < 0')).toBeVisible()
})

// Ben, who does not publish rules, reads the rules in force, and sees no drafts, nor any way to start one.
test('a member without the rules permission reads the rules and edits none', async ({ page }) => {
  await signIn(page, 'ben')
  await page.getByRole('link', { name: 'Rules' }).click()
  await expect(page.getByRole('heading', { name: 'Rules, version 3' })).toBeVisible()
  await expect(page.getByRole('region', { name: 'Drafts' })).toHaveCount(0)
  await page.goto('/rules/drafts/1')
  await expect(page.getByText('Only a member with the rules permission may do this.')).toBeVisible()
})
