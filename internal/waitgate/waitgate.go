// Package waitgate holds the one time rule of the 「等我」 judgement: a turn that asks for
// a person must have been open for the grace period before it counts. The today queue (and so
// the inbox, home page and badge) and the workbench's attention both ask it, so a terminal
// turn and a managed ACP turn are held back by the same clock and the surfaces cannot
// disagree. It reads no state: callers pass the stored time the turn ended.
package waitgate

// GraceSec is how long a turn must have been open before it counts as "waiting for me": a
// turn that lasts less than this is usually answered at the keyboard before the person ever
// looks at the list.
const GraceSec = 300

// Ripe reports whether a turn that ended (asked) at askedAt has waited out the grace
// period at now. When it has not, recheckAt is the unix second it will, so a client can
// refetch exactly then.
func Ripe(now, askedAt int64) (ripe bool, recheckAt int64) {
	at := askedAt + GraceSec
	if now < at {
		return false, at
	}
	return true, 0
}
