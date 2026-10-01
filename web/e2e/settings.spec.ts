import { expect, test } from '@playwright/test'
import { call, signIn } from './session.ts'

type Automation = { actions: string[]; repos?: number[] }

// Ana limits her automation to one repository through the API, then turns one more action on from the settings page:
// the saved automation has the new action and is still limited to that repository.
test('saving automation keeps the repositories it is limited to', async ({ page }) => {
  await signIn(page, 'ana')
  const prs = (await call(page, 'GET', '/prs')).body as { pull_requests: { repo: { id: number } }[] }
  const repo = prs.pull_requests[0].repo.id
  const limited = await call(page, 'PUT', '/me/automation', { actions: ['fix_checks', 'address_comments'], repos: [repo] })
  expect(limited.status).toBe(200)

  await page.getByRole('link', { name: 'Settings' }).click()
  await page.getByLabel('review', { exact: true }).check()
  await page.getByRole('button', { name: 'Save' }).click()
  await expect(page.getByText('Automation saved.')).toBeVisible()
  const saved = (await call(page, 'GET', '/me/automation')).body as Automation
  expect([...saved.actions].sort()).toEqual(['address_comments', 'fix_checks', 'review'])
  expect(saved.repos).toEqual([repo])
})

// Unlinking an account from Settings. Park's refusal of a last way in is shown as Park words it; an unlink Park allows
// is said, and the list asked for again. Park's answers are given here, so that the seeded links stay.
test('settings unlink an account, unless it is the last way in', async ({ page }) => {
  const refusal = 'You sign in with this account, and have no other way in; connect another account first.'
  const answers = [{ status: 409, json: { error: { code: 'last_sign_in', message: refusal } } }, { status: 204 }]
  let unlinks = 0
  await page.route(/\/api\/v1\/me\/identities\/\d+$/, async (route) => {
    expect(route.request().method()).toBe('DELETE')
    await route.fulfill(answers[unlinks++])
  })
  await signIn(page, 'ana')
  await page.getByRole('link', { name: 'Settings' }).click()
  const unlink = page.getByRole('button', { name: 'Unlink ana on Hub' })
  await unlink.click()
  await expect(page.locator('.notice')).toHaveText(refusal)
  const listed = page.waitForRequest((r) => r.method() === 'GET' && new URL(r.url()).pathname === '/api/v1/me/identities')
  await unlink.click()
  await expect(page.locator('.notice')).toHaveText('Account unlinked.')
  await listed
  expect(unlinks).toBe(2)
})

// Ana, an admin, invites a teammate: the invite's code and link are shown once. The link opens the sign-in page
// ready to join, the code filled in; Dana joins with a local account, and lands in her queue, the link's query gone.
// Back in Settings, Ana lets Dana publish rules, which stays so, and the invite says who used it.
test('an admin invites a teammate, who joins with its link, and is given a permission', async ({ page, browser }) => {
  await signIn(page, 'ana')
  await page.getByRole('link', { name: 'Settings' }).click()
  const admin = page.getByRole('region', { name: 'Administration' })
  await admin.getByRole('button', { name: 'Create an invite' }).click()
  const shown = admin.locator('.notice code').last()
  await expect(shown).toContainText('/?invite=')
  const link = new URL((await shown.textContent()) ?? '')

  const theirs = await browser.newContext()
  const dana = await theirs.newPage()
  await dana.goto(link.pathname + link.search)
  await expect(dana.getByRole('heading', { name: 'Join Park' })).toBeVisible()
  await expect(dana.getByLabel('Invite')).toHaveValue(link.searchParams.get('invite') ?? '')
  await dana.getByLabel('Username').fill('dana')
  await dana.getByLabel('Password').fill('a long enough password')
  await dana.getByLabel('Your name').fill('Dana')
  await dana.getByRole('button', { name: 'Join', exact: true }).click()
  await expect(dana.getByRole('heading', { name: 'My queue' })).toBeVisible()
  await expect(dana).toHaveURL(/\/$/)
  await theirs.close()

  await page.reload()
  const rules = admin.getByRole('checkbox', { name: 'Dana publishes rules' })
  await expect(rules).not.toBeChecked()
  await rules.click()
  await expect(rules).toBeChecked()
  await page.reload()
  await expect(admin.getByRole('checkbox', { name: 'Dana publishes rules' })).toBeChecked()
  await expect(admin.getByText(/used by Dana/)).toBeVisible()
})

// Ana adds a sign-in provider: Settings shows the address its forge calls back to, and a link to connect an account
// through it; the sign-in page offers it, and, with an invite typed in, joins through it with the invite. Removed, it
// is offered no more.
test('an admin adds a sign-in provider, which Park offers, and removes it', async ({ page, browser }) => {
  await signIn(page, 'ana')
  await page.getByRole('link', { name: 'Settings' }).click()
  const admin = page.getByRole('region', { name: 'Administration' })
  await admin.getByLabel('Provider ID').fill('hub-sso')
  await admin.getByLabel('Provider name').fill('Hub SSO')
  await admin.getByLabel('Provider client ID').fill('client-id')
  await admin.getByLabel('Provider client secret').fill('client-secret')
  await admin.getByRole('button', { name: 'Add a provider' }).click()
  await expect(admin.getByText(/\/api\/v1\/auth\/hub-sso\/callback$/)).toBeVisible()
  const connect = page.getByRole('link', { name: 'Connect an account on Hub SSO' })
  await expect(connect).toHaveAttribute('href', '/api/v1/auth/hub-sso/start?mode=link')

  const theirs = await browser.newContext()
  const visitor = await theirs.newPage()
  await visitor.goto('/')
  await expect(visitor.getByRole('link', { name: 'Sign in with Hub SSO' })).toHaveAttribute('href', '/api/v1/auth/hub-sso/start')
  await visitor.getByRole('button', { name: 'Have an invite? Join Park' }).click()
  await visitor.getByLabel('Invite').fill('CODE-1234')
  await expect(visitor.getByRole('link', { name: 'Join with Hub SSO' })).toHaveAttribute('href', '/api/v1/auth/hub-sso/start?invite=CODE-1234')
  await theirs.close()

  await admin.getByRole('button', { name: 'Remove Hub SSO' }).click()
  await expect(admin.getByText('Hub SSO removed.')).toBeVisible()
  await expect(connect).toHaveCount(0)
})

// A sign-in through a provider that failed comes back with a code, and the sign-in page says what it means; a code
// it does not know still says the sign-in did not work.
test('the sign-in page says why a sign-in through a provider failed', async ({ page }) => {
  await page.goto('/?sign_in_error=not_invited')
  await expect(page.getByText('This forge account is not a member of Park. Ask an admin for an invite.')).toBeVisible()
  await page.goto('/?sign_in_error=something_new')
  await expect(page.getByText('The sign-in did not work. Try again.')).toBeVisible()
})
