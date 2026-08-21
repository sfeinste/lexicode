// The Go binary embeds this directory's build output, so web/ sits inside the Go module. That
// means `go build ./...` walks web/node_modules — and some npm packages ship Go source (flatted,
// for one), which Go then tries to compile. One broken .go file in a transitive dependency would
// break the build of an unrelated Go project.
//
// Go skips any directory that declares its own module, so dropping a go.mod into node_modules
// takes the whole tree out of ./... . This runs from npm's postinstall hook so that it survives
// `npm ci`, `npm install`, and anyone who runs either by hand instead of through the Makefile.

import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const webRoot = dirname(dirname(fileURLToPath(import.meta.url)))
const target = join(webRoot, 'node_modules', 'go.mod')

mkdirSync(dirname(target), { recursive: true })
writeFileSync(
  target,
  [
    '// Not a real module. This file exists only so that `go build ./...` in the repository root',
    '// skips node_modules. See web/scripts/exclude-node-modules-from-go.mjs.',
    'module lexicode.invalid/node_modules',
    '',
    'go 1.27',
    '',
  ].join('\n'),
)
