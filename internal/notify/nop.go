package notify

import (
	"context"

	"sub_scribe/internal/library"
)

// NopNotifier is a Notifier that discards every notification. It is used when
// notifications are disabled.
type NopNotifier struct{}

// NewNopNotifier returns a NopNotifier.
func NewNopNotifier() *NopNotifier {
	return &NopNotifier{}
}

// Notify does nothing and always returns nil.
func (n *NopNotifier) Notify(ctx context.Context, title, message string) error {
	return nil
}

// Compile-time assertion that NopNotifier satisfies library.Notifier.
var _ library.Notifier = (*NopNotifier)(nil)
