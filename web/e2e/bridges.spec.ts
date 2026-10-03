import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { expect, test } from '@playwright/test'
import { signIn } from './session.ts'

// A bridge Park runs through the reference plugin, cmd/park-bridge-file, which scripts/e2e.sh puts in Park's plugin
// directory: it reads its account and chats from a file, which these tests write, and add a thread to before each
// sync, so that each round they ask for finds one change. The threads link pull requests of the watched acme/api
// that Park has not seen, at the seeded forge's address: Park asks the forge for them, which never answers, so the
// bridge's assertions leave the groups as the later files expect them. The tests run in order; the second reads the
// bridge the first sets up.
test.describe.configure({ mode: 'serial' })

const dir = mkdtempSync(join(tmpdir(), 'park-e2e-bridge-'))
const file = join(dir, 'chats.json')
const created = new Date().toISOString()
test.afterAll(() => rmSync(dir, { recursive: true, force: true }))

// thread is a thread of the Reviews chat that links pull requests of acme/api by number.
function thread(anchor: string, ...numbers: number[]) {
  const links = numbers.map((n) => `http://127.0.0.1:9/acme/api/pull/${n}`)
  return { anchor, created, text: `Please review ${links.join(' and ')}`, plain: `Please review ${links.join(' and ')}`, links }
}

// chats writes the plugin's file: the account that signs in, the Reviews chat with the threads given, and an empty
// General chat.
function chats(...threads: ReturnType<typeof thread>[]) {
  writeFileSync(
    file,
    JSON.stringify({
      account: { id: 'reader', name: 'Review reader' },
      chats: [
        { id: 'reviews', name: 'Reviews', threads },
        { id: 'general', name: 'General', threads: [] },
      ],
    }),
  )
}

// Ana, an admin, comes back to Settings from a connection that failed: the bridges section says why, by the code in
// the address, which is then cleaned away. She switches the File plugin on and registers Review chat to run through
// it. Its credential, the file, is saved and cleared from the form, shown only as set. Connecting its account goes
// through the plugin's sign-in and back to Settings, which says it connected, and the bridge names the account.
// The schedule and the model are saved; the model's key, like the credential, is cleared and shown only as set,
// while its address stays. Choosing the Reviews chat, from those the account lists, makes the bridge ready, and its
// first round runs at once: the page shows it from the round's notice, having read the one thread and asserted it.
// With a second thread in the chat, syncing it from the section runs a round that asserts that one too. A bridge
// registered as external is then moved into Park, which asks again before it revokes its tokens: Park runs it
// through the plugin from then on.
test('an admin runs a bridge in Park through a plugin', async ({ page }) => {
  chats(thread('m1', 90, 91))
  await signIn(page, 'ana')
  await page.goto('/settings?bridge_error=plugin_off')
  const connections = page.getByRole('region', { name: 'Connections' })
  await expect(connections.getByText("The bridge's plugin is switched off.")).toBeVisible()
  await expect(page).toHaveURL(/\/settings$/)

  await connections.getByRole('button', { name: 'Switch File on' }).click()
  await expect(connections.getByText('The File plugin is on.')).toBeVisible()
  await connections.getByLabel("New bridge's name").fill('Review chat')
  await connections.getByLabel("New bridge's plugin").selectOption({ label: 'Run by Park, through File' })
  await connections.getByRole('button', { name: 'Register a bridge' }).click()
  const bridge = connections.getByRole('article', { name: 'Review chat', exact: true })
  await expect(bridge).toContainText('Owned by Ana, run by Park through File. No round yet.')
  await expect(bridge).toContainText('No account is connected.')

  await bridge.getByLabel('File of Review chat').fill(file)
  await bridge.getByRole('button', { name: 'Save the credentials of Review chat' }).click()
  await expect(connections.getByText('Review chat: credentials saved.')).toBeVisible()
  await expect(bridge.getByLabel('File of Review chat')).toHaveValue('')
  await expect(bridge.getByLabel('File of Review chat')).toHaveAttribute('placeholder', 'File: set')

  await bridge.getByRole('link', { name: 'Connect an account for Review chat' }).click()
  await expect(connections.getByText('Connected the account of Review chat.')).toBeVisible()
  await expect(page).toHaveURL(/\/settings$/)
  await expect(bridge).toContainText('Reads as Review reader, connected by Ana < 1 min ago.')

  await bridge.getByLabel('Minutes between rounds of Review chat').fill('30')
  await bridge.getByRole('button', { name: 'Save the schedule of Review chat' }).click()
  await expect(connections.getByText('Review chat: schedule saved.')).toBeVisible()
  await bridge.getByLabel('Model address of Review chat').fill('http://127.0.0.1:9/v1')
  await bridge.getByLabel('Model name of Review chat').fill('reader-model')
  await bridge.getByLabel('Model key of Review chat').fill('a-model-key')
  await bridge.getByRole('button', { name: 'Save the model of Review chat' }).click()
  await expect(connections.getByText('Review chat: model saved.')).toBeVisible()
  await expect(bridge.getByLabel('Model key of Review chat')).toHaveValue('')
  await expect(bridge.getByLabel('Model key of Review chat')).toHaveAttribute('placeholder', 'Model key: set')
  await expect(bridge.getByLabel('Model address of Review chat')).toHaveValue('http://127.0.0.1:9/v1')
  await expect(bridge.getByLabel('Minutes between rounds of Review chat')).toHaveValue('30')
  await expect(bridge).toContainText('No round yet.')

  await bridge.getByRole('button', { name: 'List the chats Review chat can read' }).click()
  await expect(bridge.getByLabel('Chat Review chat reads').getByRole('option')).toHaveText(['Reviews', 'General'])
  await bridge.getByLabel('Chat Review chat reads').selectOption({ label: 'Reviews' })
  await bridge.getByRole('button', { name: 'Save the chat of Review chat' }).click()
  await expect(bridge).toContainText('Reads the chat Reviews.')
  await expect(bridge).toContainText('Last round < 1 min ago: read 1 message, 1 change.')

  chats(thread('m1', 90, 91), thread('m2', 92, 93))
  await bridge.getByRole('button', { name: 'Sync Review chat now' }).click()
  await expect(bridge).toContainText('Last round < 1 min ago: read 2 messages, 1 change.')
  await expect(bridge.getByRole('button', { name: 'Sync Review chat now' })).toBeEnabled()

  await connections.getByLabel("New bridge's name").fill('Old chat')
  await connections.getByLabel("New bridge's plugin").selectOption({ label: 'External, with a token' })
  await connections.getByRole('button', { name: 'Register a bridge' }).click()
  const old = connections.getByRole('article', { name: 'Old chat', exact: true })
  await expect(old).toContainText('Owned by Ana, posting with a token.')
  await old.getByRole('button', { name: 'Run Old chat in Park' }).click()
  await old.getByLabel('Plugin to run Old chat through').selectOption({ label: 'File' })
  await old.getByRole('button', { name: 'Move Old chat into Park, and revoke its tokens' }).click()
  await expect(connections.getByText('Park runs Old chat now; its tokens are revoked.')).toBeVisible()
  await expect(old).toContainText('Owned by Ana, run by Park through File. No round yet.')
})

// Ben, a member, sees on the groups page the bridges Park runs, each with its last round; the external one the seed
// registered is not among them. A third thread is in the chat when he syncs Review chat. While the page has not yet
// fetched the bridges again, the button says the bridge is syncing, and cannot be pressed; once the round's notice
// brings the page the round, it shows the thread asserted, and the button is back.
test('a member syncs a bridge from the groups page', async ({ page }) => {
  chats(thread('m1', 90, 91), thread('m2', 92, 93), thread('m3', 94, 95))
  await signIn(page, 'ben')
  await page.getByRole('link', { name: 'Groups' }).click()
  const bridges = page.getByRole('region', { name: 'Bridges' })
  await expect(bridges.getByRole('listitem')).toHaveText([/^Review chat, through File\. Last round/, /^Old chat, through File\. No round yet\./])
  const bridge = bridges.getByRole('listitem').filter({ hasText: /^Review chat/ })
  await expect(bridge).toContainText('read 2 messages, 1 change.')

  await page.route('**/api/v1/bridges', async (route) => {
    await new Promise((resolve) => setTimeout(resolve, 1500))
    await route.continue()
  })
  await bridge.getByRole('button', { name: 'Sync Review chat now' }).click()
  await expect(bridge.getByRole('button', { name: 'Syncing Review chat…' })).toBeDisabled()
  await expect(bridge).toContainText('read 3 messages, 1 change.', { timeout: 10_000 })
  await expect(bridge.getByRole('button', { name: 'Sync Review chat now' })).toBeEnabled()
})
