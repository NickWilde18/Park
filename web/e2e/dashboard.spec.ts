import { expect, test } from '@playwright/test'
import { call, signIn } from './session.ts'

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
  await expect(page.locator('article.group h3')).toBeVisible()
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

// Ana's first stream ends at once, and the browser's own attempt to resume it is answered 502, as a proxy answers
// while Park restarts: the browser then gives the stream up. The page opens it again, and follows Ben's rename.
test('a page opens its stream again when the browser gives it up', async ({ page, browser }) => {
  let streams = 0
  await page.route('**/api/v1/events', async (route) => {
    streams++
    if (streams === 1) await route.fulfill({ status: 200, contentType: 'text/event-stream', body: 'retry: 100\n\n' })
    else if (streams === 2) await route.fulfill({ status: 502, body: '' })
    else await route.continue()
  })
  await signIn(page, 'ana')
  await page.getByRole('link', { name: 'Groups' }).click()
  await expect(page.locator('article.group h3')).toBeVisible()
  await expect.poll(() => streams).toBeGreaterThanOrEqual(2)
  const bens = await browser.newContext()
  const ben = await bens.newPage()
  await signIn(ben, 'ben')
  await call(ben, 'PATCH', '/groups/1', { name: 'Accounts' })
  await bens.close()
  await expect(page.getByRole('heading', { name: 'Accounts', exact: true })).toBeVisible({ timeout: 10_000 })
})

// Ana's first fetch of who she is fails, as when Park cannot be reached: the page tries again, and shows the sign-in.
test('a page fetches again when a fetch fails', async ({ page }) => {
  let asked = 0
  await page.route('**/api/v1/me', async (route) => {
    if (asked++ === 0) await route.abort('connectionrefused')
    else await route.continue()
  })
  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible({ timeout: 10_000 })
})

// Ben renames the group again and again while each of Ana's fetches of the groups takes longer than the notices take
// to come: her page still shows a newer name within a few seconds, rather than only once the renames stop.
test('a page keeps up with changes that come faster than it fetches', async ({ page, browser }) => {
  await signIn(page, 'ana')
  await page.getByRole('link', { name: 'Groups' }).click()
  await expect(page.locator('article.group h3')).toBeVisible()
  await page.route('**/api/v1/groups', async (route) => {
    await new Promise((resolve) => setTimeout(resolve, 1500))
    await route.continue()
  })
  const bens = await browser.newContext()
  const ben = await bens.newPage()
  await signIn(ben, 'ben')
  const renaming = (async () => {
    for (let i = 1; i <= 20; i++) {
      await call(ben, 'PATCH', '/groups/1', { name: `Accounts ${i}` })
      await new Promise((resolve) => setTimeout(resolve, 400))
    }
  })()
  await expect(page.getByRole('heading', { name: /^Accounts \d+$/ })).toBeVisible({ timeout: 5_000 })
  await renaming
  await bens.close()
})
