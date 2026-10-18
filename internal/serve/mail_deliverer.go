package serve

import (
	"path/filepath"
	"strings"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/core"
	"github.com/inhere/gofer/internal/httpapi"
	"github.com/inhere/gofer/internal/mail"
)

// startMailDeliverer assembles and starts the mailbox deliverer over core's mailbox (the very
// instance the HTTP server sends through) and the server's own session relay. It returns nil
// when the server has no mailbox or relay (no job store).
func startMailDeliverer(cr *core.Core, srv *httpapi.Server, cfg *config.Config) *mail.Deliverer {
	relay := srv.Relay()
	if cr == nil || cr.Mail == nil || relay == nil || cr.Jobs == nil {
		return nil
	}
	set := cfg.EffectiveMail()
	cr.Mail.SetDeliverBy(set.DeliverBy)
	dl := mail.NewDeliverer(mail.DelivererOptions{
		Svc: cr.Mail, Sender: relay, Jobs: cr.Jobs,
		IsSuga:   func(key string) bool { return isSugaAgent(cr.Agents, key) },
		Settings: mailSettings(set),
	})
	dl.Start()
	return dl
}

// applyMailSettings re-reads the `mail:` block on a config reload.
func applyMailSettings(svc *mail.Service, dl *mail.Deliverer, next *config.Config) {
	set := next.EffectiveMail()
	if svc != nil {
		svc.SetDeliverBy(set.DeliverBy)
	}
	if dl != nil {
		dl.Configure(mailSettings(set))
	}
}

func mailSettings(s config.MailSettings) mail.Settings {
	return mail.Settings{Tick: s.Tick, WakeIdle: s.WakeIdle, WakeInterval: s.WakeInterval, Lease: s.Lease}
}

// isSugaAgent reports whether a session's agent key is suga: the built-in key, or a
// cli-agent whose command is the suga binary (a renamed key keeps the same `suga send`).
func isSugaAgent(reg *agent.Registry, key string) bool {
	key = strings.TrimSpace(key)
	if key == "suga" {
		return true
	}
	if reg == nil {
		return false
	}
	ac, ok := reg.Get(key)
	if !ok || ac.Type != agent.TypeCLIAgent {
		return false
	}
	base := strings.TrimSuffix(strings.ToLower(filepath.Base(ac.Command)), ".exe")
	return base == "suga"
}
