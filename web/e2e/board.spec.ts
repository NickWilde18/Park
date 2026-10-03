import { expect, test } from '@playwright/test'
import { signIn } from './session.ts'

// Ana's board: her units first — the Billing group, its pull requests each with her action, and her draft — then the
// unit she waits on, with who holds it, then the one she has no part in; on those two, To do says what the holder is to
// do. Sorting by To do puts her own first. She filters by the ball, folds the group, sorts by a column, both ways, and
// hides a column, which stays hidden after a reload.
test('the board shows every open unit as it stands for me', async ({ page }) => {
  await signIn(page, 'ana')
  await page.getByRole('link', { name: 'Board' }).click()
  const rows = page.getByRole('table').locator('tbody tr')
  await expect(rows).toHaveCount(6)
  const row = (name: string) => rows.filter({ hasText: name })
  await expect(rows.first()).toContainText('Billing')
  await expect(rows.first().locator('.tag', { hasText: 'Yours' })).toBeVisible()
  await expect(row('api#12 Billing export')).toContainText('fix checks — 1 required checks failing')
  // Checks are named by where they stand on the latest commit, and each says what it covers.
  const failed = row('api#12 Billing export').locator('.tag', { hasText: 'Failed' })
  await expect(failed).toBeVisible()
  await expect(failed).toHaveAttribute('title', 'A required check on the latest commit failed.')
  const passed = row('api#14 Retry failed payments').locator('.tag', { hasText: 'Passed' })
  await expect(passed).toBeVisible()
  await expect(passed).toHaveAttribute('title', 'Every required check on the latest commit finished, and passed.')
  await expect(row('api#14 Retry failed payments')).toContainText('address comments')
  await expect(row('api#15 Export formats')).toContainText('hold — Draft')
  await expect(row('api#16 Statement emails').locator('.tag', { hasText: "Others'" })).toBeVisible()
  await expect(row('api#16 Statement emails')).toContainText('ben')
  await expect(row('api#13 Invoice layout').locator('.tag', { hasText: 'Not yours' })).toBeVisible()
  // Where Ana has nothing to do — waiting on her own pull request, or with no part at all — To do shows, quietly, what
  // the holder holds there.
  for (const name of ['api#16 Statement emails', 'api#13 Invoice layout']) {
    await expect(row(name).getByText('ben: review — First review', { exact: true })).toHaveClass(/quiet/)
  }
  await page.getByRole('button', { name: 'To do', exact: true }).click()
  await expect(rows.first()).toContainText('Billing')
  await expect(rows.last()).toContainText('ben: review — First review')
  await page.getByRole('button', { name: 'To do', exact: true }).click()
  await expect(rows.first()).toContainText('ben: review — First review')

  await page.getByRole('combobox', { name: 'Ball' }).selectOption('yours')
  await expect(rows).toHaveCount(4)
  await expect(page.getByText('2 of 4 units')).toBeVisible()
  await page.getByRole('combobox', { name: 'Ball' }).selectOption('')

  await page.getByRole('button', { name: 'Fold Billing' }).click()
  await expect(row('api#12 Billing export')).toHaveCount(0)
  await page.getByRole('button', { name: 'Unfold Billing' }).click()
  await expect(row('api#12 Billing export')).toBeVisible()

  const author = page.getByRole('button', { name: 'Author', exact: true })
  await author.click()
  await expect(page.locator('th[aria-sort="ascending"]')).toHaveText('Author ↑')
  await expect(rows.last()).toContainText('api#13')
  await author.click()
  await expect(page.locator('th[aria-sort="descending"]')).toHaveText('Author ↓')
  await expect(rows.first()).toContainText('api#13')

  await page.getByText('Columns').click()
  await page.getByRole('checkbox', { name: 'Author' }).uncheck()
  await expect(page.getByRole('button', { name: 'Author', exact: true })).toHaveCount(0)
  await page.reload()
  await expect(page.getByRole('table')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Author', exact: true })).toHaveCount(0)
  await page.getByText('Columns').click()
  await page.getByRole('checkbox', { name: 'Author' }).check()
  await expect(page.getByRole('button', { name: 'Author', exact: true })).toBeVisible()
})

// Ben reviews the Billing group's two pull requests. On api#12 his first review is his own to do. On api#14 he waits
// for Ana to answer his request for changes, so To do says what she holds there, and only there: not her failing
// checks, which are on api#12.
test('where I only wait, To do says what the holder holds on that pull request', async ({ page }) => {
  await signIn(page, 'ben')
  await page.getByRole('link', { name: 'Board' }).click()
  const rows = page.getByRole('table').locator('tbody tr')
  const row = (name: string) => rows.filter({ hasText: name })
  await expect(row('api#12 Billing export')).toContainText('review — First review')
  await expect(row('api#14 Retry failed payments').getByText(/^ana: /)).toHaveText(
    'ana: address comments — ben requested changes; 2 items wait on you',
  )
})

// Asking the forge now. From the board: about every repository; with the board filtered to one, about that one; and
// from the Billing group's row, about its two pull requests. From the group's page, which says when Park last
// checked each pull request: about its open ones, the same two. Park's answers are recorded here, so that the seeded forge, where
// nothing listens, is never asked, and the database the other tests share stays as it was.
test('the board and a unit ask the forge now', async ({ page }) => {
  const asked: string[] = []
  await page.route(
    (url) => /^\/api\/v1\/prs\/(\d+\/)?refresh$/.test(url.pathname),
    async (route) => {
      const url = new URL(route.request().url())
      asked.push(url.pathname.replace('/api/v1', '') + url.search)
      await route.fulfill({ status: 202 })
    },
  )
  await signIn(page, 'ana')
  await page.getByRole('link', { name: 'Board' }).click()
  const status = page.getByRole('status')
  await page.getByRole('button', { name: 'Refresh all repositories' }).click()
  await expect(status).toContainText('Asked the forge about every repository')
  await page.getByRole('combobox', { name: 'Repository' }).selectOption('acme/api')
  await page.getByRole('button', { name: 'Refresh acme/api' }).click()
  await expect(status).toContainText('Asked the forge about acme/api')
  await page.getByRole('button', { name: 'Refresh Billing', exact: true }).click()
  await expect(status).toContainText('Asked the forge about Billing')
  expect(asked.slice(0, 2)).toEqual(['/prs/refresh', expect.stringMatching(/^\/prs\/refresh\?repo=\d+$/)])
  expect(asked.slice(2)).toHaveLength(2)
  expect(asked.slice(2).every((a) => /^\/prs\/\d+\/refresh$/.test(a))).toBe(true)

  asked.length = 0
  await page.getByRole('link', { name: 'Billing', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Billing', exact: true })).toBeVisible()
  await expect(page.locator('article.pr h3', { hasText: /checked .+ ago/ })).toHaveCount(2)
  await page.getByRole('button', { name: 'Refresh from the forge' }).click()
  await expect(page.getByRole('status')).toContainText('Asked the forge about its 2 open pull requests')
  expect(asked).toHaveLength(2)
  expect(asked.every((a) => /^\/prs\/\d+\/refresh$/.test(a))).toBe(true)
})
