// Package contextmod is the context-provider module: the sources of agent context behind the
// run prompt and the Context panel (contracts §2.6, architecture §11). S22 ships the
// `project` (priority 10) and `ticket` (priority 30) providers; the `wiki` and `repofiles`
// providers (decision D-11) arrive with story S34.
//
// The directory is named context to match architecture §5; the package is named contextmod so
// that it does not shadow the standard library context package at its use sites.
package contextmod
