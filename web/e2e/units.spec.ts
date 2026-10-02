import { fileURLToPath } from 'node:url'
import { expect, test } from '@playwright/test'
import { call, signIn } from './session.ts'

// These tests change the groups, so they run after the others: files run in order of name.

// The suggestions inbox. Each suggestion names its pull request, the signal's reason, and the group it is suggested
// for, under that group's name now: an earlier file renames it. Accepting and dismissing each send their request, and
// the inbox asks again, until none is left and it goes. Park's suggestions and answers are given here, so that the
// groups are left as they were.
test('the groups page lists suggestions, to accept or dismiss', async ({ page }) => {
  let open = [
    { id: 7, pull_request: 3, group: 1, reason: 'is in the same stack', created_at: '2026-10-01T12:00:00Z' },
    { id: 8, pull_request: 6, group: 1, reason: 'refers to them, and they to it', created_at: '2026-10-01T12:00:00Z' },
  ]
  const sent: string[] = []
  await page.route('**/api/v1/suggestions', (route) => route.fulfill({ json: { suggestions: open } }))
  await page.route(/\/api\/v1\/suggestions\/\d+\/(accept|dismiss)$/, async (route) => {
    const path = new URL(route.request().url()).pathname.replace('/api/v1', '')
    sent.push(path)
    open = open.filter((s) => s.id !== Number(path.split('/')[2]))
    await route.fulfill({ status: 204 })
  })
  await signIn(page, 'ana')
  const group = ((await call(page, 'GET', '/groups/1')).body as { name: string }).name
  await page.getByRole('link', { name: 'Groups' }).click()
  const inbox = page.getByRole('region', { name: 'Suggested' })
  await expect(inbox.getByRole('listitem')).toHaveCount(2)
  await expect(inbox.getByRole('listitem').first()).toContainText(`api#13 Invoice layout is in the same stack — suggested for ${group}`)
  await inbox.getByRole('button', { name: `Accept api#13 into ${group}` }).click()
  await expect(inbox.getByRole('listitem')).toHaveCount(1)
  await inbox.getByRole('button', { name: `Dismiss api#16 for ${group}` }).click()
  await expect(page.getByRole('region', { name: 'Suggested' })).toHaveCount(0)
  expect(sent).toEqual(['/suggestions/7/accept', '/suggestions/8/dismiss'])
})

// Ana's pull request 11 merged yesterday: its page names it, and says how its unit closed.
test('a closed unit shows how it closed', async ({ page }) => {
  await signIn(page, 'ana')
  await page.goto('/units/pr/1')
  await expect(page.getByRole('heading', { name: 'api#11', exact: true })).toBeVisible()
  await expect(page.getByText('Closed out: merged')).toBeVisible()
})

// Ana groups Carol's pull request on its own, then merges that group into the first: the merged group's page is the
// page of the group it joined, with that group's verdict.
test('a merged group\'s page is the page of the group it joined', async ({ page }) => {
  await signIn(page, 'ana')
  const made = (await call(page, 'POST', '/groups', { name: 'Invoices', pull_requests: [3] })).body as { id: number }
  expect((await call(page, 'POST', '/groups/1/merge', { from: made.id })).status).toBe(200)
  await page.goto(`/units/group/${made.id}`)
  await expect(page).toHaveURL(/\/units\/group\/1$/)
  await expect(page.locator('table.parts')).toBeVisible()
  await expect(page.getByText('No verdict yet.')).toHaveCount(0)
})

// Taking a pull request out of its group opens the pull request's own page. While that page loads, slowed here, it
// shows nothing of the group's: no name, no rename.
test('a unit\'s page shows nothing of the unit before it', async ({ page }) => {
  await signIn(page, 'ana')
  await page.goto('/units/group/1')
  await expect(page.locator('table.parts')).toBeVisible()
  await page.route('**/api/v1/prs/4', async (route) => {
    await new Promise((resolve) => setTimeout(resolve, 3000))
    await route.continue()
  })
  await page.getByRole('button', { name: 'Take api#14 out of the group' }).click()
  await expect(page).toHaveURL(/\/units\/pr\/4$/)
  await expect(page.getByText('Loading…')).toBeVisible()
  await expect(page.getByLabel('Group name')).toHaveCount(0)
  await expect(page.getByRole('heading', { name: 'api#14', exact: true })).toBeVisible({ timeout: 10_000 })
})

// Ana's groups page lists the bridge's two proposals, each linked to its thread, and one the bridge posts while the
// page is open, from its notice. She dismisses two, and they go. She leaves a pull request out of the third, whose
// group then has too few to group, and Park says so; with it back and the group renamed, accepting makes the group
// under her name, and the proposal goes too.
test('a bridge\'s proposals are dismissed or accepted on the groups page', async ({ page }) => {
  await signIn(page, 'ana')
  await page.getByRole('link', { name: 'Groups' }).click()
  const proposed = page.getByRole('region', { name: 'Proposed' })
  const current = page.getByRole('region', { name: 'Current groups' })
  await expect(proposed.getByRole('link', { name: 'Exports and statement emails' })).toHaveAttribute('href',
    'https://chat.example.test/thread/thread-exports')
  await expect(proposed.locator('article.group')).toHaveCount(0)
  await expect(current.locator('article.proposal')).toHaveCount(0)
  await expect(current.locator('article.group').first()).toBeVisible()
  await expect(proposed.locator('article.proposal').first()).toHaveCSS('border-top-style', 'dashed')
  await expect(proposed.getByRole('link', { name: 'Invoice follow-ups' })).toBeVisible()
  await expect(page.getByLabel('Name of group 1 proposed in Invoice follow-ups')).toHaveAttribute('placeholder',
    'Named after its oldest pull request')

  const bridges = (await call(page, 'GET', '/admin/bridges')).body as { bridges: { id: number }[] }
  const token = (await call(page, 'POST', '/me/tokens', { name: 'threads', scopes: ['assertions:write'], bridge_id: bridges.bridges[0].id }))
    .body as { token: string }
  const posted = await page.request.post('/api/v1/proposals', {
    headers: { Authorization: `Bearer ${token.token}` },
    data: { anchor: 'thread-late', title: 'A late thread', groups: [{ pull_requests: [{ repo: 'acme/api', number: 14 }, { repo: 'acme/api', number: 15 }] }] },
  })
  expect(posted.status()).toBe(201)
  await expect(proposed.getByRole('link', { name: 'A late thread' })).toBeVisible({ timeout: 10_000 })

  await page.getByRole('button', { name: 'Dismiss A late thread' }).click()
  await page.getByRole('button', { name: 'Dismiss Invoice follow-ups' }).click()
  await expect(proposed.getByRole('link', { name: 'Invoice follow-ups' })).toHaveCount(0)
  await expect(proposed.getByRole('link', { name: 'A late thread' })).toHaveCount(0)

  const exports = proposed.locator('article', { has: page.getByRole('link', { name: 'Exports and statement emails' }) })
  await exports.getByLabel(/api#16/).uncheck()
  await page.getByRole('button', { name: 'Accept Exports and statement emails' }).click()
  await expect(page.getByText('No group of the proposal has two open pull requests.')).toBeVisible()
  await exports.getByLabel(/api#16/).check()
  await page.getByLabel('Name of group 1 proposed in Exports and statement emails').fill('Exports')
  await page.getByRole('button', { name: 'Accept Exports and statement emails' }).click()
  await expect(page.getByRole('region', { name: 'Proposed' })).toHaveCount(0)
  const made = current.locator('article.group', { has: page.getByRole('heading', { name: 'Exports', exact: true }) })
  await expect(made.getByText('api#15 Export formats')).toBeVisible()
  await expect(made.getByText('api#16 Statement emails')).toBeVisible()
})

// Ana wrote a pull request of the first group, so she may give it a description: a zip of a static site, uploaded
// from the group's page. It shows beside the group, framed in a sandbox whose script runs in an origin of its own: it
// can read neither the viewer's cookies nor Park's answers as them. The groups page and the board link to it, and
// once removed it is gone, and the page offers the upload again.
test("a group's description is uploaded, shown beside it in a sandbox, and removed", async ({ page }) => {
  await signIn(page, 'ana')
  await page.goto('/units/group/1')
  await page.getByLabel('Upload: a zip of a static site').setInputFiles(fileURLToPath(new URL('./fixtures/description.zip', import.meta.url)))
  await page.getByRole('button', { name: 'Upload', exact: true }).click()
  const framed = page.locator('aside.doc iframe')
  const frame = page.frameLocator('aside.doc iframe')
  await expect(frame.getByRole('heading', { name: 'Billing, explained' })).toBeVisible()
  await expect(frame.locator('#scripted')).toHaveText('scripts run, cookies closed')
  await expect(frame.locator('#me')).toHaveText('me: refused')
  expect(await framed.getAttribute('sandbox')).toContain('allow-scripts')
  expect(await framed.getAttribute('sandbox')).not.toContain('allow-same-origin')
  const link = await page.getByRole('link', { name: 'Open in a new tab' }).getAttribute('href')
  expect(link).toMatch(/^\/docs\/[A-Za-z0-9_-]{22}\/\?theme=light$/)

  await page.getByRole('link', { name: 'Groups', exact: true }).click()
  await expect(page.getByRole('link', { name: 'Description', exact: true })).toHaveAttribute('href', link ?? '')
  await page.getByRole('link', { name: 'Board', exact: true }).click()
  await expect(page.getByRole('link', { name: /^Description of / })).toHaveAttribute('href', link ?? '')

  await page.goto('/units/group/1')
  await page.getByRole('button', { name: 'Remove', exact: true }).click()
  await expect(page.locator('aside.doc')).toHaveCount(0)
  await expect(page.getByLabel('Upload: a zip of a static site')).toBeVisible()
})

// The theme, switched in the header, colors the dashboard and carries into a group's description. The frame starts in
// the theme shown, and is told each change: its page, which styles itself for [data-theme="dark"], follows without
// reloading, and so does a page reached inside it. The link that opens the description on its own names the theme.
// The choice outlives a reload. System follows the operating system, emulated light, then dark.
test("the theme colors the dashboard and carries into a group's description", async ({ page }) => {
  await signIn(page, 'ana')
  await page.goto('/units/group/1')
  await page.getByLabel('Upload: a zip of a static site').setInputFiles(fileURLToPath(new URL('./fixtures/description.zip', import.meta.url)))
  await page.getByRole('button', { name: 'Upload', exact: true }).click()
  const frame = page.frameLocator('aside.doc iframe')
  await expect(frame.getByRole('heading', { name: 'Billing, explained' })).toBeVisible()
  const dashboard = page.locator('html')
  const root = frame.locator('html')
  const body = frame.locator('body')
  const opened = page.getByRole('link', { name: 'Open in a new tab' })
  const theme = page.getByLabel('Theme')
  await expect(theme).toHaveValue('system')
  await expect(dashboard).toHaveCSS('background-color', 'rgb(250, 251, 252)')
  await expect(root).toHaveAttribute('data-theme', 'light')
  await expect(body).toHaveCSS('background-color', 'rgb(255, 255, 255)')

  await theme.selectOption('dark')
  await expect(dashboard).toHaveCSS('background-color', 'rgb(13, 17, 23)')
  await expect(root).toHaveAttribute('data-theme', 'dark')
  await expect(root).toHaveCSS('color-scheme', 'dark')
  await expect(body).toHaveCSS('background-color', 'rgb(1, 2, 3)')
  await expect(opened).toHaveAttribute('href', /\?theme=dark$/)
  await frame.getByRole('link', { name: 'More' }).click()
  await expect(frame.getByRole('heading', { name: 'More about billing' })).toBeVisible()
  await expect(body).toHaveCSS('background-color', 'rgb(1, 2, 3)')

  await page.reload()
  await expect(theme).toHaveValue('dark')
  await expect(body).toHaveCSS('background-color', 'rgb(1, 2, 3)')
  await theme.selectOption('light')
  await expect(dashboard).toHaveCSS('background-color', 'rgb(250, 251, 252)')
  await expect(body).toHaveCSS('background-color', 'rgb(255, 255, 255)')
  await page.emulateMedia({ colorScheme: 'dark' })
  await theme.selectOption('system')
  await expect(dashboard).toHaveCSS('background-color', 'rgb(13, 17, 23)')
  await expect(body).toHaveCSS('background-color', 'rgb(1, 2, 3)')

  await page.getByRole('button', { name: 'Remove', exact: true }).click()
  await expect(page.locator('aside.doc')).toHaveCount(0)
})
