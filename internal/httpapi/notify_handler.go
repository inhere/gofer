// notify_handler.go serves the console's notification settings (gofer-x9mb):
//
//	GET  /v1/notify/events  the event catalog (key, group, zh/en description, default flags)
//	POST /v1/notify/test    send ONE test message through the CURRENT config
//
// Writing the targets themselves stays on PUT /v1/config/server (the notification block is
// a patch there); these two routes only read the vocabulary and prove a target works. The
// catalog comes from internal/notify, so the console never carries a second event list.
package httpapi

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/desktop"
	"github.com/inhere/gofer/internal/notify"
)

// EventNotifyTest is the event type stamped on a test message. It is deliberately not a
// real trigger: a bot's X-Gofer-Event header should say "this was a test".
const EventNotifyTest = "notify.test"

// notifyTestTargetWebhook / notifyTestTargetDesktop are the accepted `target` values.
const (
	notifyTestTargetWebhook = "webhook"
	notifyTestTargetDesktop = "desktop"
)

// notifyDesktopChannelView describes the desktop channel's platform capability: the
// console cannot know which OS the server runs on, and "the switch is on but this build
// has no notifier" is a state the operator must see.
type notifyDesktopChannelView struct {
	Supported bool `json:"supported"`
	// MergeSecDefault is the coalescing window used when desktop.merge_sec is unset.
	MergeSecDefault int `json:"merge_sec_default"`
}

type notifyEventsView struct {
	Events  []notify.EventInfo       `json:"events"`
	Groups  []notify.EventGroup      `json:"groups"`
	Desktop notifyDesktopChannelView `json:"desktop"`
}

// handleNotifyEvents returns the event catalog the settings pickers render.
func (s *Server) handleNotifyEvents(c *rux.Context) {
	c.JSON(http.StatusOK, notifyEventsView{
		Events:  notify.EventCatalog(),
		Groups:  notify.EventGroups,
		Desktop: notifyDesktopChannelView{Supported: desktop.PlatformSupported(), MergeSecDefault: config.DefaultDesktopMergeSec},
	})
}

type notifyTestReq struct {
	// Target is "webhook" (with Index) or "desktop".
	Target string `json:"target"`
	Index  int    `json:"index"`
}

type notifyTestResp struct {
	OK     bool   `json:"ok"`
	Target string `json:"target"`
	Index  int    `json:"index,omitempty"`
	Kind   string `json:"kind,omitempty"`
	// Error is the delivery failure verbatim — for an IM bot that is its own business
	// error ("keywords not in content"), which is the whole point of the test button.
	Error string `json:"error,omitempty"`
	// Skipped reports that the desktop channel could not show anything for an
	// environment reason (no interactive session, no notifier installed).
	Skipped bool `json:"skipped,omitempty"`
}

// handleNotifyTest sends one test message through a configured target and reports what
// happened. A refusal is a 200 with ok:false + error: the operator is testing a target,
// and "the bot said no" is the answer, not a server fault. 400 is reserved for a request
// that names a target which does not exist.
func (s *Server) handleNotifyTest(c *rux.Context) {
	var req notifyTestReq
	if err := c.BindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	var cfg *config.Config
	if s.projects != nil {
		cfg = s.projects.Config()
	}
	var nconf *config.NotificationConfig
	link := ""
	if cfg != nil {
		nconf = cfg.Server.Notification
		link = strings.TrimRight(strings.TrimSpace(cfg.Server.WebBaseURL), "/") + "/settings/notifications"
	}
	msg := notify.Message{
		EventType: EventNotifyTest,
		Title:     "gofer 通知测试",
		Text:      "这是一条来自 gofer 的测试消息（设置 · 通知 → 发送测试）。",
		Link:      link,
		LinkLabel: "打开设置",
		At:        time.Now().Unix(),
	}

	switch strings.ToLower(strings.TrimSpace(req.Target)) {
	case notifyTestTargetDesktop:
		err := s.desktopNotifier()(desktop.Notification{
			EventType: EventNotifyTest, Title: msg.Title, Text: msg.Text, Link: msg.Link, At: msg.At,
		})
		if err != nil {
			resp := notifyTestResp{Target: notifyTestTargetDesktop, Error: err.Error()}
			var skip *desktop.SkipError
			resp.Skipped = errors.As(err, &skip)
			c.JSON(http.StatusOK, resp)
			return
		}
		c.JSON(http.StatusOK, notifyTestResp{OK: true, Target: notifyTestTargetDesktop})

	case notifyTestTargetWebhook:
		if nconf == nil || req.Index < 0 || req.Index >= len(nconf.Webhooks) {
			writeError(c, http.StatusBadRequest, "webhook not found",
				"target webhook needs an index of a configured server.notification.webhooks entry")
			return
		}
		w := nconf.Webhooks[req.Index]
		kind := notify.NormalizeKind(w.Kind)
		payload, err := notify.RenderMessageWithLimit(kind, msg, w.Keyword, w.EffectiveMaxTextRunes(nconf.EffectiveMaxTextRunes()))
		if err != nil {
			c.JSON(http.StatusOK, notifyTestResp{Target: notifyTestTargetWebhook, Index: req.Index, Kind: kind, Error: "render: " + err.Error()})
			return
		}
		// The 加签 secret is read here, at post time, like the delivery sweeper does: the
		// config only ever holds the env var NAME (SR403).
		secret := ""
		if w.SecretEnv != "" {
			secret = os.Getenv(w.SecretEnv)
		}
		target, hmacSecret := w.URL, secret
		if kind != notify.KindGeneric {
			hmacSecret = ""
			signedURL, signedBody, sErr := notify.ApplyProviderAuth(kind, w.URL, payload, secret, time.Now())
			if sErr != nil {
				c.JSON(http.StatusOK, notifyTestResp{Target: notifyTestTargetWebhook, Index: req.Index, Kind: kind, Error: "sign: " + sErr.Error()})
				return
			}
			target, payload = signedURL, signedBody
		}
		ctx, cancel := context.WithTimeout(c.Req.Context(), notify.DefaultPostTimeoutSeconds*time.Second)
		defer cancel()
		if err := s.postNotify(ctx, kind, target, EventNotifyTest, payload, hmacSecret, *nconf); err != nil {
			c.JSON(http.StatusOK, notifyTestResp{Target: notifyTestTargetWebhook, Index: req.Index, Kind: kind, Error: err.Error()})
			return
		}
		c.JSON(http.StatusOK, notifyTestResp{OK: true, Target: notifyTestTargetWebhook, Index: req.Index, Kind: kind})

	default:
		writeError(c, http.StatusBadRequest, "invalid target",
			`target must be "webhook" (with index) or "desktop"`)
	}
}

// desktopNotifier is the platform popup, overridable by a test so none pops.
func (s *Server) desktopNotifier() func(desktop.Notification) error {
	if s.notifyDesktop != nil {
		return s.notifyDesktop
	}
	return desktop.NewSystemNotifier().Show
}

// postNotify is the outbound webhook POST (validated + signed), overridable by a test.
func (s *Server) postNotify(ctx context.Context, kind, target, eventType string, body []byte, secretValue string, cfg config.NotificationConfig) error {
	if s.notifyPost != nil {
		return s.notifyPost(ctx, kind, target, eventType, body, secretValue, cfg)
	}
	return notify.PostWebhook(ctx, kind, target, eventType, body, secretValue, cfg)
}
