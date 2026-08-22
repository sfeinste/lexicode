// Package cron is the schedule.cron event source (story S32, architecture §7.1): the second
// ports.EventSource, which is what proves the port boundary is real — its Catalog() puts the
// `schedule` event in the trigger editor with no frontend change.
//
// What it does, and the decisions behind it:
//
//   - Every minute (UTC, aligned to the minute boundary) it scans the enabled triggers stored
//     for this source and, for each one whose cron expression matches a minute since its
//     cursor, emits one `schedule` · `cron` event carrying the contracts §4 payload
//     {"schedule": {"cron", "fired_at", "trigger_id"}}.
//
//   - Events are PER TRIGGER, addressed by subject: subject_kind "trigger", subject_id the
//     trigger's own id. Two schedule triggers with different expressions must not fire each
//     other, but the engine evaluates every event against every enabled trigger of the
//     project — so the match stage (service/triggers/match.go) has one generic rule for
//     addressed events: an event whose subject is a trigger is matched only by that trigger.
//
//   - Time is UTC. A cron expression is five fields (minute hour day-of-month month
//     day-of-week) with *, lists, ranges, steps and 3-letter month/day names — parsed by
//     expr.go, hand-rolled because the grammar is ~150 lines and a dependency would be larger
//     than the code. Day-of-month/day-of-week follow the POSIX rule: a field is unrestricted
//     only when it is exactly `*`; when BOTH are restricted the day matches when EITHER does.
//
//   - The dedupe key is sha256(source|trigger|cron|minute), so a restart inside a minute (or a
//     crash between emit and cursor write) collapses onto the bus's unique index and never
//     double-fires.
//
//   - Catch-up after downtime is AT MOST ONE firing per trigger: the per-trigger cursor (a
//     poll_cursors row, resource "cron:<trigger id>" — reused rather than a new table because
//     it is exactly a poll cursor) records the last emitted scheduled minute; a scan emits
//     only the MOST RECENT match since the cursor, with fired_at naming that missed minute.
//     Older missed firings are skipped, never a storm. A trigger seen for the first time
//     baselines silently, like the poller's cold start: schedules never fire for the past.
//
//   - Save-time validation: the source implements ports.TriggerVetter, so the trigger CRUD
//     refuses an invalid expression with a field error naming the bad segment without the
//     service importing this module.
//
// Loop-guard interplay: the catalog's SubjectKey is "schedule:{{schedule.trigger_id}}", so
// debounce and cancel-in-progress key each schedule trigger on itself. Note the default
// loop config's 90s debounce debounces run_agent firings closer together than 90s — an
// every-minute cron that starts runs will see alternate firings `debounced`, which is the
// guard doing its job, visible in the firing history.
package cron
