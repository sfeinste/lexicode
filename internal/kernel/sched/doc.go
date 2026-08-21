// Package sched owns the run queue, admission control and the run state machine (D-14; story
// S22 builds it). Until S22 the package holds only the request seam: RunRequest, the Requester
// interface and the Unscheduled placeholder, so that earlier stories (S10's column auto-start
// and archive-time run cancellation) call the real boundary today and change nothing when the
// scheduler arrives.
package sched
