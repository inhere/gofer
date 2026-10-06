package work

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// ErrPlanNotFound is returned by ToTodo when the chosen plan does not exist.
var ErrPlanNotFound = errors.New("plan not found")

// AlreadyTodoError is returned by ToTodo when the item already became a todo: the
// caller shows where it went instead of making a second one.
type AlreadyTodoError struct {
	TodoID string
	PlanID string
}

func (e *AlreadyTodoError) Error() string {
	return fmt.Sprintf("work item is already linked to todo %s (plan %s)", e.TodoID, e.PlanID)
}

// ToTodoInput picks the destination of the conversion.
type ToTodoInput struct {
	// PlanID is an existing plan; empty creates a new plan named NewPlanTitle (default:
	// the work item's title).
	PlanID       string
	NewPlanTitle string
	// By is the actor label for the plan owner / journal.
	By string
}

// ToTodoResult says what was created.
type ToTodoResult struct {
	TodoID      string `json:"todo_id"`
	PlanID      string `json:"plan_id"`
	PlanCreated bool   `json:"plan_created"`
}

func newID(prefix string, now time.Time) string {
	var b [4]byte
	suffix := fmt.Sprintf("%08x", now.UnixNano()&0xffffffff)
	if _, err := rand.Read(b[:]); err == nil {
		suffix = hex.EncodeToString(b[:])
	}
	return prefix + now.Format("20060102-150405") + "-" + suffix
}

// ExistingTodo returns the todo (and its plan) an item was already converted into.
func (s *Service) ExistingTodo(id string) (todoID, planID string, ok bool, err error) {
	links, err := s.store.ListWorkLinks(id)
	if err != nil {
		return "", "", false, err
	}
	for _, l := range links {
		if l.Kind != jobstore.WorkLinkTodo {
			continue
		}
		t, found, err := s.store.GetTodo(l.Ref)
		if err != nil {
			return "", "", false, err
		}
		if found {
			return t.TodoID, t.PlanID, true, nil
		}
	}
	return "", "", false, nil
}

// ToTodo turns a work item into a plan todo: title and description come from the item,
// the item records a todo link (plus a plan link) so the two can be followed both ways.
// Converting twice is refused with *AlreadyTodoError.
func (s *Service) ToTodo(id string, in ToTodoInput) (ToTodoResult, error) {
	id = strings.TrimSpace(id)
	w, ok, err := s.store.GetWorkItem(id)
	if err != nil {
		return ToTodoResult{}, err
	}
	if !ok {
		return ToTodoResult{}, jobstore.ErrWorkItemNotFound
	}
	if tid, pid, dup, err := s.ExistingTodo(w.ID); err != nil {
		return ToTodoResult{}, err
	} else if dup {
		return ToTodoResult{}, &AlreadyTodoError{TodoID: tid, PlanID: pid}
	}

	now := s.nowFn()
	res := ToTodoResult{PlanID: strings.TrimSpace(in.PlanID)}
	if res.PlanID != "" {
		if _, found, err := s.store.GetPlan(res.PlanID); err != nil {
			return ToTodoResult{}, err
		} else if !found {
			return ToTodoResult{}, fmt.Errorf("%w: %s", ErrPlanNotFound, res.PlanID)
		}
	} else {
		title := strings.TrimSpace(in.NewPlanTitle)
		if title == "" {
			title = w.Title
		}
		res.PlanID = newID("plan-", now)
		res.PlanCreated = true
		if err := s.store.InsertPlan(jobstore.Plan{
			PlanID: res.PlanID, Title: title, Status: jobstore.PlanOpen, Owner: in.By,
			ProjectKey: w.ProjectKey, CreatedAt: now.Unix(), UpdatedAt: now.Unix(),
		}); err != nil {
			return ToTodoResult{}, err
		}
	}

	res.TodoID = newID("todo-", now)
	if err := s.store.InsertTodo(jobstore.PlanTodo{
		TodoID: res.TodoID, PlanID: res.PlanID, Title: w.Title, Status: jobstore.TodoPending,
		Note: todoNote(w), Auto: true, CreatedAt: now.Unix(), UpdatedAt: now.Unix(),
	}); err != nil {
		return ToTodoResult{}, err
	}
	_ = s.store.TouchPlan(res.PlanID)
	if _, err := s.store.AddWorkLink(w.ID, jobstore.WorkLinkTodo, res.TodoID, in.By); err != nil {
		return ToTodoResult{}, err
	}
	if _, err := s.store.AddWorkLink(w.ID, jobstore.WorkLinkPlan, res.PlanID, in.By); err != nil {
		return ToTodoResult{}, err
	}
	return res, nil
}

// todoNote is the todo description built from the item: goal, then the next step.
func todoNote(w jobstore.WorkItem) string {
	var parts []string
	if g := strings.TrimSpace(w.Goal); g != "" {
		parts = append(parts, g)
	}
	if n := strings.TrimSpace(w.NextStep); n != "" {
		parts = append(parts, "下一步: "+n)
	}
	parts = append(parts, "来源工作项: "+w.ID)
	return strings.Join(parts, "\n\n")
}
