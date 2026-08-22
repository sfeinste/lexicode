// Package notify is the notification-delivery module (story S28): the ports.Notifier
// implementations behind the kernel's notifier registry (architecture §3.1; contracts §2.7).
// V1 ships one channel, "inapp" — the notification row and the inbox badge; Slack, email and
// push are the extension the port buys (S36+).
//
// The module does not write the row itself: the in-app "row updated in place, never stacked"
// behaviour already lives in the S24 notify service, and the dependency rule (module →
// kernel/ports → domain) keeps this package from importing internal/service. cmd/lexicode
// injects the service's DeliverInApp through Options.Deliver — the same seam inversion as
// module/actions' ticket funcs.
package notify
