package config

import (
	"fmt"
	"slices"
)

// retiredEventRenames maps a notify event key that no longer exists to the one that
// replaced it. Configs written before the rename still list the old key in their
// events filters; it is read as the new key so nothing goes silent, and the load
// says so.
var retiredEventRenames = map[string]string{
	"session.waiting": "session.awaiting_reply",
}

// renameRetiredEvents rewrites the retired keys in events to their replacements,
// dropping duplicates (a list naming both keys keeps one). It returns the list and
// the retired keys it found, in list order; an untouched list is returned as is.
func renameRetiredEvents(events []string) ([]string, []string) {
	var found []string
	for _, e := range events {
		if _, ok := retiredEventRenames[e]; ok {
			found = append(found, e)
		}
	}
	if len(found) == 0 {
		return events, nil
	}
	out := make([]string, 0, len(events))
	for _, e := range events {
		if nw, ok := retiredEventRenames[e]; ok {
			e = nw
		}
		if !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	return out, found
}

// migrateRetiredEvents applies renameRetiredEvents to every events filter of the
// notification channels and event hooks, and returns one load warning per list that
// used a retired key (label names the file holding the server section).
func migrateRetiredEvents(cfg *Config, label string) []string {
	if cfg == nil {
		return nil
	}
	var out []string
	warn := func(where string, found []string) {
		for _, old := range found {
			out = append(out, fmt.Sprintf("%s: %s lists the retired event %q; it is read as %q, please rename it in the config",
				label, where, old, retiredEventRenames[old]))
		}
	}
	if n := cfg.Server.Notification; n != nil {
		if d := n.Desktop; d != nil {
			var found []string
			d.Events, found = renameRetiredEvents(d.Events)
			warn("server.notification.desktop.events", found)
		}
		for i := range n.Webhooks {
			var found []string
			n.Webhooks[i].Events, found = renameRetiredEvents(n.Webhooks[i].Events)
			warn(fmt.Sprintf("server.notification.webhooks[%d].events", i), found)
		}
	}
	for i := range cfg.Server.EventHooks {
		var found []string
		cfg.Server.EventHooks[i].Events, found = renameRetiredEvents(cfg.Server.EventHooks[i].Events)
		warn(fmt.Sprintf("server.event_hooks[%d].events", i), found)
	}
	return out
}
