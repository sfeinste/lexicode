// Package contextmod is the context-provider module: the sources of agent context behind the
// run prompt and the Context panel (contracts §2.6, architecture §11). All four providers of
// §11's table live here: `project` (priority 10), `wiki` (20), `ticket` (30) and
// `repofiles` (40, listed-not-injected per decision D-11) — together with `event` (25), the
// occurrence that caused a trigger-spawned run, which is the only thing such a run knows
// about its own task.
//
// The directory is named context to match architecture §5; the package is named contextmod so
// that it does not shadow the standard library context package at its use sites.
package contextmod
