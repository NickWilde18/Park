import { expect, test } from '@playwright/test'
import { signIn } from './session.ts'

// Ana's queue: the Billing group, where her failing check and Ben's request for changes wait on her, on the two pull
// requests of its chain, with its task; her draft; and, collapsed, the pull request waiting for Ben's first review.
// The task's badge opens it on the tasks page.
test('the queue shows the units whose ball I hold, and those I wait on', async ({ page }) => {
  await signIn(page, 'ana')
  const holding = page.getByRole('list', { name: 'Holding' })
  await expect(holding.getByRole('link', { name: 'Billing', exact: true })).toBeVisible()
  await expect(holding.getByText('fix checks')).toBeVisible()
  await expect(holding.getByText('1 required checks failing')).toBeVisible()
  await expect(holding.getByText('ben requested changes; 2 items wait on you')).toBeVisible()
  await expect(holding.getByText('1/2')).toBeVisible()
  await expect(holding.getByText('2/2')).toBeVisible()
  await expect(holding.getByText('hold')).toBeVisible()
  const waiting = page.getByRole('list', { name: 'Waiting' })
  await expect(waiting).toBeHidden()
  await page.getByText('Waiting on others (1)').click()
  await expect(waiting.getByText('held by ben (First review)')).toBeVisible()
  await holding.getByText('task queued').click()
  await expect(page.getByRole('heading', { name: 'Tasks' })).toBeVisible()
  await expect(page.locator('#task-1')).toContainText('fix checks (open)')
})

// The Billing group's page: where its ball is, and every person's part with its path through the rules; each pull
// request's fact events, and the history of its task.
test('a unit shows every part, with its path through the rules', async ({ page }) => {
  await signIn(page, 'ana')
  await page.getByRole('link', { name: 'Billing', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Billing', exact: true })).toBeVisible()
  await expect(page.getByText('The ball is on the author side')).toBeVisible()
  const parts = page.locator('table.parts')
  await expect(parts.getByText('author → not a draft → no conflicts → Checks failing')).toBeVisible()
  await expect(parts.getByText('reviewer → not a draft → checks not running → First review')).toBeVisible()
  await expect(page.getByRole('link', { name: 'version 1' })).toBeVisible()

  await page.getByText('Timeline (2)').click()
  const timeline = page.getByRole('list', { name: 'Timeline of api#12' })
  await expect(timeline.getByText('first seen')).toBeVisible()
  await expect(timeline.getByText('checks, merge blockers, review requests, updated at')).toBeVisible()
  await page.getByText('History (3)').click()
  const history = page.getByRole('list', { name: 'History of task 1' })
  await expect(history.getByText('created, queued')).toBeVisible()
  await expect(history.getByText('item added, queued')).toHaveCount(2)
})

// Ben renames the group in his own browser; Ana's groups page shows the new name without being reloaded, from the
// notice Park streams to it.
test('a page follows what changes, as Park streams notices', async ({ page, browser }) => {
  await signIn(page, 'ana')
  await page.getByRole('link', { name: 'Groups' }).click()
  await expect(page.getByRole('heading', { name: 'Billing', exact: true })).toBeVisible()
  const bens = await browser.newContext()
  const ben = await bens.newPage()
  await signIn(ben, 'ben')
  await ben.goto('/units/group/1')
  await ben.getByLabel('Group name').fill('Billing and refunds')
  await ben.getByRole('button', { name: 'Rename' }).click()
  await expect(ben.getByRole('heading', { name: 'Billing and refunds', exact: true })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Billing and refunds', exact: true })).toBeVisible()
  await bens.close()
})

// Ana's stream is held back until after Ben renames the group, so its notice comes before the stream begins: the
// page, fetched before the rename, shows the new name once its stream opens.
test('a page fetches again when its stream opens', async ({ page, browser }) => {
  let open = () => {}
  const opened = new Promise<void>((resolve) => (open = resolve))
  await page.route('**/api/v1/events', async (route) => {
    await opened
    await route.continue()
  })
  await signIn(page, 'ana')
  await page.getByRole('link', { name: 'Groups' }).click()
  await expect(page.locator('article.group h2')).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Ledger', exact: true })).toHaveCount(0)
  const bens = await browser.newContext()
  const ben = await bens.newPage()
  await signIn(ben, 'ben')
  await ben.goto('/units/group/1')
  await ben.getByLabel('Group name').fill('Ledger')
  await ben.getByRole('button', { name: 'Rename' }).click()
  await expect(ben.getByRole('heading', { name: 'Ledger', exact: true })).toBeVisible()
  await bens.close()
  open()
  await expect(page.getByRole('heading', { name: 'Ledger', exact: true })).toBeVisible()
})
