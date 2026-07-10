package core

import "context"

// NotificationsService is the bell / email / Discord pipeline
// every plugin shares. Specific notification kinds (offer
// claimed, forum quote, thanks given) are domain-specific and
// are routed by the Kind string — the recipient's
// notification-preferences row controls which channel(s) fire.
type NotificationsService interface {
	// Notify enqueues a notification for delivery. See the
	// Notification field docs for the envelope contract.
	// Returns an error only on impossible-to-recover failures —
	// transient channel issues are logged and swallowed so a
	// flaky webhook doesn't break the user-facing flow.
	Notify(ctx context.Context, userID int64, n Notification) error
}

// Notification is the plugin-facing envelope. It mirrors the
// host's bell-notification row shape closely enough that domain
// notifications (forum quotes, wiki approvals) render exactly as
// they did before their surfaces became plugins.
type Notification struct {
	// Kind is the preferences + dedup key (e.g. "forum_quote",
	// "wiki:edit_approved"). Recipients who disabled the kind's
	// inbox channel are silently skipped. Required.
	Kind string

	// Title is the bell headline ("alice quoted you"). Required.
	Title string

	// Body is the secondary line (thread title, excerpt). May be
	// empty.
	Body string

	// Link is the click-through target ("/community/forums/
	// thread/7#post-42"). May be empty — the bell row is then
	// non-navigable.
	Link string

	// ActorID / ActorName identify the user whose action caused
	// the notification. ActorID 0 means "no actor" (system
	// events). When ActorID equals the recipient, the host skips
	// delivery — you don't get notified about your own actions.
	ActorID   int64
	ActorName string
}

// NotificationsAdapter is the function-bundle the host hands to
// NewNotifications. The single NotifyFn callback carries the full
// envelope; the host maps it onto its concrete notification row +
// preference gate.
type NotificationsAdapter struct {
	NotifyFn func(ctx context.Context, userID int64, n Notification) error
}

// NewNotifications constructs a NotificationsService from the
// given adapter. A nil callback yields a no-op service — useful
// for tests.
func NewNotifications(a NotificationsAdapter) NotificationsService {
	return &notificationsAdapter{a: a}
}

type notificationsAdapter struct{ a NotificationsAdapter }

func (n *notificationsAdapter) Notify(ctx context.Context, userID int64, notif Notification) error {
	if n.a.NotifyFn == nil {
		return nil
	}
	return n.a.NotifyFn(ctx, userID, notif)
}
