package httpapi

import (
	"errors"
	"net/http"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/webpush"
)

type WebPushService interface {
	PublicKey() (string, error)
	Subscribe(callerID string, input webpush.SubscriptionInput, userAgent string) (jobstore.PushSubscription, error)
	ListSubscriptions(callerID string) ([]jobstore.PushSubscription, error)
	DeleteSubscription(callerID, endpoint string) (bool, error)
	QueueTest(callerID string) (int, error)
	Act(token, option string) (job.Interaction, error)
}

type deletePushSubscriptionReq struct {
	Endpoint string `json:"endpoint"`
}

type pushActionReq struct {
	Token  string `json:"token"`
	Option string `json:"option"`
}

type pushSubscriptionView struct {
	Endpoint  string `json:"endpoint"`
	UserAgent string `json:"user_agent"`
	CreatedAt int64  `json:"created_at"`
	LastOKAt  int64  `json:"last_ok_at"`
}

func (s *Server) handlePushPublicKey(c *rux.Context) {
	if !s.pushUserCaller(c) {
		return
	}
	if s.push == nil {
		writeError(c, http.StatusServiceUnavailable, "web push unavailable", "web push service is not wired")
		return
	}
	publicKey, err := s.push.PublicKey()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "load VAPID public key failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]string{"public_key": publicKey})
}

func (s *Server) handleListPushSubscriptions(c *rux.Context) {
	if !s.pushUserCaller(c) {
		return
	}
	if s.push == nil {
		writeError(c, http.StatusServiceUnavailable, "web push unavailable", "web push service is not wired")
		return
	}
	rows, err := s.push.ListSubscriptions(callerFromCtx(c))
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list push subscriptions failed", err.Error())
		return
	}
	views := make([]pushSubscriptionView, 0, len(rows))
	for _, row := range rows {
		views = append(views, pushSubscriptionView{
			Endpoint: row.Endpoint, UserAgent: row.UserAgent,
			CreatedAt: row.CreatedAt, LastOKAt: row.LastOKAt,
		})
	}
	c.JSON(http.StatusOK, map[string]any{"subscriptions": views})
}

func (s *Server) handleCreatePushSubscription(c *rux.Context) {
	if !s.pushUserCaller(c) {
		return
	}
	if s.push == nil {
		writeError(c, http.StatusServiceUnavailable, "web push unavailable", "web push service is not wired")
		return
	}
	var body webpush.SubscriptionInput
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	sub, err := s.push.Subscribe(callerFromCtx(c), body, c.Req.UserAgent())
	if err != nil {
		writeError(c, http.StatusBadRequest, "register push subscription failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, pushSubscriptionView{
		Endpoint: sub.Endpoint, UserAgent: sub.UserAgent,
		CreatedAt: sub.CreatedAt, LastOKAt: sub.LastOKAt,
	})
}

func (s *Server) handleDeletePushSubscription(c *rux.Context) {
	if !s.pushUserCaller(c) {
		return
	}
	if s.push == nil {
		writeError(c, http.StatusServiceUnavailable, "web push unavailable", "web push service is not wired")
		return
	}
	var body deletePushSubscriptionReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	deleted, err := s.push.DeleteSubscription(callerFromCtx(c), body.Endpoint)
	if err != nil {
		writeError(c, http.StatusBadRequest, "delete push subscription failed", err.Error())
		return
	}
	if !deleted {
		writeError(c, http.StatusNotFound, "push subscription not found", "no subscription for this caller and endpoint")
		return
	}
	c.JSON(http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) handleTestPush(c *rux.Context) {
	if !s.pushUserCaller(c) {
		return
	}
	if s.push == nil {
		writeError(c, http.StatusServiceUnavailable, "web push unavailable", "web push service is not wired")
		return
	}
	queued, err := s.push.QueueTest(callerFromCtx(c))
	if err != nil {
		if errors.Is(err, webpush.ErrNoSubscriptions) {
			writeError(c, http.StatusConflict, "no push subscription", err.Error())
			return
		}
		writeError(c, http.StatusInternalServerError, "queue test push failed", err.Error())
		return
	}
	c.JSON(http.StatusAccepted, map[string]int{"queued": queued})
}

// handlePushAction is intentionally outside the bearer-authenticated route
// group. Its one-time HMAC token is the sole credential.
func (s *Server) handlePushAction(c *rux.Context) {
	if s.push == nil {
		writeError(c, http.StatusServiceUnavailable, "web push unavailable", "web push service is not wired")
		return
	}
	var body pushActionReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	interaction, err := s.push.Act(body.Token, body.Option)
	if err != nil {
		switch {
		case errors.Is(err, webpush.ErrActionUnauthorized):
			writeError(c, http.StatusUnauthorized, "invalid push action token", "signature or token format is invalid")
		case errors.Is(err, webpush.ErrActionOption):
			writeError(c, http.StatusBadRequest, "push action option not allowed", err.Error())
		case errors.Is(err, webpush.ErrActionExpired), errors.Is(err, webpush.ErrActionUsed),
			errors.Is(err, job.ErrInteractionState), errors.Is(err, job.ErrJobTerminal),
			errors.Is(err, job.ErrUnknownInteraction), errors.Is(err, job.ErrUnknownJob):
			writeError(c, http.StatusConflict, "push action is no longer available", err.Error())
		default:
			writeError(c, http.StatusInternalServerError, "push action failed", err.Error())
		}
		return
	}
	c.JSON(http.StatusOK, interaction)
}

func (s *Server) pushUserCaller(c *rux.Context) bool {
	if callerKindFromCtx(c) == callerKindUser {
		return true
	}
	writeError(c, http.StatusForbidden, "push management requires a user caller", "worker and job credentials cannot manage browser push")
	return false
}
