//go:build !windows && !darwin && !linux

package desktop

// NewSystemNotifier on a platform with no implementation: every notification is a
// skip, recorded once (see Dispatcher.report), so the channel is simply inert.
func NewSystemNotifier() Notifier { return unsupportedNotifier{} }

type unsupportedNotifier struct{}

func (unsupportedNotifier) Show(Notification) error {
	return &SkipError{Reason: "desktop notifications are not supported on this platform"}
}
