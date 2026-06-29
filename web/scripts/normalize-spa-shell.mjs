import { readFileSync, writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const loadedAtPattern = /([,{]u:)\d+(,s:"success")/g

export function normalizeSPAShell(html) {
  let replacements = 0
  const normalized = html.replace(loadedAtPattern, (_match, prefix, suffix) => {
    replacements += 1
    return `${prefix}0${suffix}`
  })

  if (replacements === 0) {
    throw new Error('SPA shell did not contain a TanStack route loaded timestamp to normalize')
  }
  return normalized
}

export function normalizeSPAShellFile(path) {
  const html = readFileSync(path, 'utf8')
  writeFileSync(path, normalizeSPAShell(html))
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  const shellPath = process.argv[2]
  if (!shellPath) {
    console.error('usage: normalize-spa-shell.mjs <shell.html>')
    process.exit(2)
  }

  try {
    normalizeSPAShellFile(shellPath)
  } catch (error) {
    console.error(error instanceof Error ? error.message : String(error))
    process.exit(1)
  }
}
