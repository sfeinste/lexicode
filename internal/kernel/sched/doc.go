// Package sched is the kernel-owned run scheduler (D-14, story S22): the run queue,
// admission control (§10.2), the run state machine (§10.1), execution supervision, the
// failure-artifact rule (§10.5) and boot crash reconciliation (§10.6). Nothing else may
// start a run or write runs.state — modules and services request runs through Requester
// (or Scheduler.Enqueue) and the kernel decides.
package sched
