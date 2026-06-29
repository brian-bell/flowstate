import assert from 'node:assert/strict'
import test from 'node:test'

import { normalizeSPAShell } from './normalize-spa-shell.mjs'

test('normalizes TanStack route loaded timestamps', () => {
  const html =
    '<script>$_TSR.router=($R=>$R[0]={matches:$R[8]=[$R[9]={i:"__root__\\u0000",u:1782766090249,s:"success",ssr:!0}],lastMatchId:"__root__\\u0000"})($R["tsr"])</script>'

  assert.equal(
    normalizeSPAShell(html),
    '<script>$_TSR.router=($R=>$R[0]={matches:$R[8]=[$R[9]={i:"__root__\\u0000",u:0,s:"success",ssr:!0}],lastMatchId:"__root__\\u0000"})($R["tsr"])</script>',
  )
})

test('fails when the shell no longer contains the expected timestamp shape', () => {
  assert.throws(
    () => normalizeSPAShell('<html><body>flowstate</body></html>'),
    /TanStack route loaded timestamp/,
  )
})
