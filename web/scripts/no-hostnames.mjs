// The frontend knows nothing about where Park runs (A2): no address with a scheme and a host appears in its source,
// or in the page that loads it. The generated types are left out: their comments quote the contract's descriptions,
// and they hold no code.
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'

const files = ['index.html']
const walk = (dir) => {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name)
    if (entry.isDirectory()) walk(path)
    else if (/\.(ts|tsx|css|html)$/.test(entry.name) && path !== join('src', 'api', 'schema.d.ts')) files.push(path)
  }
}
walk('src')

const found = []
for (const path of files) {
  readFileSync(path, 'utf8')
    .split('\n')
    .forEach((line, i) => {
      if (/[a-z][a-z0-9+.-]*:\/\/[^\s'"`/]+/i.test(line)) found.push(`${path}:${i + 1}: ${line.trim()}`)
    })
}
if (found.length > 0) {
  console.error('The frontend names a host:\n' + found.join('\n'))
  process.exit(1)
}
console.log(`no hostnames in the frontend's ${files.length} files`)
