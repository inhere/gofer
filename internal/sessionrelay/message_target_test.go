package sessionrelay

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
)

const (
	// peerTestUnix is a Linux / macOS messaging socket as Claude Code writes it.
	peerTestUnix = "/run/user/1000/cc-socks/4242.sock"
	// peerTestPipe is the Windows named pipe. It starts with two backslashes, so the
	// tests can tell a verbatim address from a %q-escaped one.
	peerTestPipe = `\\.\pipe\LOCAL\cc-msg-0123456789abcdef0123456789abcdef`
)

func registerPeerSession(t *testing.T, s *Service, sid, runner, name, socket string) {
	t.Helper()
	_, err := s.Register(RegisterInput{SessionID: sid, Agent: "claude", ProjectKey: "self", Runner: runner,
		PeerName: name, PeerMessaging: true, PeerSocket: socket, Event: EventSessionStart})
	assert.NoErr(t, err)
}

func peerTarget(t *testing.T, s *Service, sid string) messengerTarget {
	t.Helper()
	a, err := s.Session(sid)
	assert.NoErr(t, err)
	return s.messengerTargetFor(a)
}

// The delivery target (design 2026-11-05 「提示词」): an address only means something
// when the server accepted it, and the fallback to the name is only allowed while no
// OTHER live session of the same runner answers to that name.
func TestMessengerTargetFor(t *testing.T) {
	t.Run("no address keeps the name path", func(t *testing.T) {
		s := newSvc(t)
		registerPeerSession(t, s, "sid-a", "local", "proj-a", "")
		g := peerTarget(t, s, "sid-a")
		assert.Eq(t, "proj-a", g.Name)
		assert.Eq(t, "", g.Socket)
		assert.False(t, g.Fallback, "without an address there is nothing to fall back from")
	})

	t.Run("an address that no longer validates counts as none", func(t *testing.T) {
		s := newSvc(t)
		// A row written before the server validated (Register does not check), or one
		// whose value changed shape since: it must not reach a prompt.
		registerPeerSession(t, s, "sid-a", "local", "proj-a", "/var/run/docker.sock")
		g := peerTarget(t, s, "sid-a")
		assert.Eq(t, "", g.Socket)
		assert.False(t, g.Fallback)
	})

	t.Run("no namesake allows the fallback", func(t *testing.T) {
		s := newSvc(t)
		registerPeerSession(t, s, "sid-a", "local", "proj-a", peerTestUnix)
		g := peerTarget(t, s, "sid-a")
		assert.Eq(t, peerTestUnix, g.Socket)
		assert.True(t, g.Fallback, "nothing else answers to this name")
	})

	t.Run("a namesake on the same runner closes it", func(t *testing.T) {
		s := newSvc(t)
		// The stored labels may spell the built-in runner either way (G043): the
		// comparison must still see one runner, or the prompt would fall back into the
		// other session.
		registerPeerSession(t, s, "sid-a", "server", "proj-a", peerTestUnix)
		registerPeerSession(t, s, "sid-b", "local", "proj-a", "")
		assert.False(t, peerTarget(t, s, "sid-a").Fallback)
	})

	t.Run("a namesake on another runner does not", func(t *testing.T) {
		s := newSvc(t)
		registerPeerSession(t, s, "sid-a", "local", "proj-a", peerTestUnix)
		registerPeerSession(t, s, "sid-b", "builder", "proj-a", "")
		assert.True(t, peerTarget(t, s, "sid-a").Fallback)
	})

	t.Run("a namesake without a runner does not", func(t *testing.T) {
		s := newSvc(t)
		registerPeerSession(t, s, "sid-a", "local", "proj-a", peerTestUnix)
		registerPeerSession(t, s, "sid-b", "", "proj-a", "")
		assert.True(t, peerTarget(t, s, "sid-a").Fallback, "a row that never said where it runs takes no part (R2)")
	})

	t.Run("an ended namesake does not", func(t *testing.T) {
		s := newSvc(t)
		registerPeerSession(t, s, "sid-a", "local", "proj-a", peerTestUnix)
		registerPeerSession(t, s, "sid-b", "local", "proj-a", "")
		_, err := s.Heartbeat("sid-b", HeartbeatInput{Event: EventSessionEnd})
		assert.NoErr(t, err)
		assert.True(t, peerTarget(t, s, "sid-a").Fallback)
	})

	t.Run("a failed lookup keeps it closed", func(t *testing.T) {
		st, err := jobstore.Open(filepath.Join(t.TempDir(), "gofer.db"))
		assert.NoErr(t, err)
		s := NewService(st)
		registerPeerSession(t, s, "sid-a", "local", "proj-a", peerTestUnix)
		assert.NoErr(t, st.Close())

		buf := &lockedBuffer{}
		restore := swapSlog(buf)
		defer restore()

		g := s.messengerTargetFor(jobstore.AgentSession{SessionID: "sid-a", PeerName: "proj-a",
			PeerSocket: peerTestUnix, Runner: "local"})
		assert.True(t, g.Fallback == false, "a lookup that fails must not guess")
		logged := buf.String()
		assert.True(t, strings.Contains(logged, "sid-a"), "the warning names the session: %s", logged)
		assert.False(t, strings.Contains(logged, peerTestUnix), "and never the address: %s", logged)
	})
}

// A target without an address renders the instruction gofer has always sent, word for
// word — the name path must not change (acceptance "无地址行为不变").
func TestMessengerPromptWithoutAnAddressIsUnchanged(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.EN, i18n.ZhCN} {
		for _, fallback := range []bool{false, true} {
			got := messengerInstruction(lang, messengerTarget{Name: "proj-a", Fallback: fallback}, "prompt")
			want := i18n.T(lang, "sessionrelay.messenger.prompt", "name", fmt.Sprintf("%q", "proj-a"))
			assert.Eq(t, want, got, "%s prompt with no address", lang)
			assert.False(t, strings.Contains(got, "uds:"), "no address, no address sentence: %q", got)
		}
		wake := messengerInstruction(i18n.ZhCN, messengerTarget{Name: "peer-a"}, "wake_prompt")
		assert.Eq(t, i18n.T(i18n.ZhCN, "sessionrelay.messenger.wake_prompt", "name", `"peer-a"`), wake)
	}
}

// The address is written into the prompt verbatim: %q would double every backslash of a
// Windows pipe, and a messenger that sends a different string cannot reach the session.
// The sentence about a failed tool call differs by whether the name may be used as a
// fallback: with a namesake alive it must not be.
func TestMessengerAddressPromptIsVerbatim(t *testing.T) {
	fallbackSentence := map[i18n.Lang]string{i18n.EN: "once at most", i18n.ZhCN: "最多一次"}
	noFallbackSentence := map[i18n.Lang]string{i18n.EN: "do not send it to another target", i18n.ZhCN: "不要改发其他目标"}
	for _, lang := range []i18n.Lang{i18n.EN, i18n.ZhCN} {
		for _, base := range []string{"prompt", "wake_prompt"} {
			for _, fallback := range []bool{false, true} {
				target := messengerTarget{Name: "proj-4d", Socket: peerTestPipe, Fallback: fallback}
				got := messengerInstruction(lang, target, base)
				assert.True(t, strings.Contains(got, "uds:"+peerTestPipe),
					"%s %s must carry the address verbatim: %q", lang, base, got)
				assert.False(t, strings.Contains(got, `\\\\`),
					"%s %s must not carry a %q-escaped address: %q", lang, base, got)
				assert.Eq(t, 1, strings.Count(got, peerTestPipe), "%s %s names the address once", lang, base)
				assert.False(t, strings.Contains(got, "] "), "%s %s must not contain \"] \": %q", lang, base, got)
				if fallback {
					assert.True(t, strings.Contains(got, `"proj-4d"`), "%s %s keeps the quoted name", lang, base)
					assert.True(t, strings.Contains(got, fallbackSentence[lang]), "%s %s allows one fallback", lang, base)
					assert.True(t, strings.Contains(got, "success:false"), "%s %s names the failure that allows it", lang, base)
					continue
				}
				assert.False(t, strings.Contains(got, fallbackSentence[lang]), "%s %s must not allow a fallback", lang, base)
				assert.True(t, strings.Contains(got, noFallbackSentence[lang]), "%s %s says to report the failure", lang, base)
			}
		}
	}
}

// The web message keeps reaching the target intact: the instruction may only add text in
// front of it, and the text is cut after the FIRST "] " (the web prefix, see
// httpapi's messengerOriginalMessage).
func TestMessengerPromptKeepsTheForwardedTextWhole(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.EN, i18n.ZhCN} {
		for _, target := range []messengerTarget{
			{Name: "proj-4d"},
			{Name: "proj-4d", Socket: peerTestPipe},
			{Name: "proj-4d", Socket: peerTestUnix, Fallback: true},
		} {
			p := messengerPrompt(lang, target, "alice", "the original text")
			marker := strings.Index(p, "] ")
			assert.True(t, marker >= 0, "%s prompt carries the web prefix: %q", lang, p)
			assert.Eq(t, "the original text", p[marker+2:])
			assert.True(t, strings.HasPrefix(p, messengerInstruction(lang, target, "prompt")), "%s", lang)
		}
	}
}

// messageTargetFake implements both messenger seams of the relay: it records the command
// each path was given and answers the resident one with a canned tool outcome.
type messageTargetFake struct {
	submitted int
	commands  [][]string
	resident  ResidentResult
}

func (f *messageTargetFake) SubmitMessenger(_, _, _ string, command []string, _, _ string) (string, error) {
	f.submitted++
	f.commands = append(f.commands, append([]string(nil), command...))
	return "job-1", nil
}

func (*messageTargetFake) MessengerJob(string) (bool, string, int, string, string, error) {
	return true, "done", 0, "已发送", "已发送", nil
}

func (f *messageTargetFake) SendMessengerResident(_ context.Context, _, _, _ string, command []string) (ResidentResult, error) {
	f.commands = append(f.commands, append([]string(nil), command...))
	return f.resident, nil
}

// promptArg picks the -p value out of a recorded messenger command.
func promptArg(t *testing.T, command []string) string {
	t.Helper()
	for i, part := range command {
		if part == "-p" && i+1 < len(command) {
			return command[i+1]
		}
	}
	t.Fatalf("command has no -p prompt: %v", command)
	return ""
}

// A resident turn whose SendMessage failed is a failed message, not 「已送达」, and it
// never turns into a one-shot job: a second attempt could hand the text over twice while
// the first one landed (acceptance 4 / 8).
func TestResidentMessengerFailureFailsTheMessage(t *testing.T) {
	cases := map[string]string{
		"tool reason": "2 agents are named proj-a",
		"no reason":   i18n.T(i18n.Default, "sessionrelay.reason.messenger_no_send"),
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			s := newSvc(t)
			reason := ""
			if name == "tool reason" {
				reason = want
			}
			f := &messageTargetFake{resident: ResidentResult{Sent: false, Reason: reason}}
			s.SetMessenger(f)
			registerPeerSession(t, s, "sid-a", "server", "proj-a", peerTestUnix)

			m, err := s.SendMessage(context.Background(), "sid-a", "hi", "alice", "alice")
			assert.True(t, err != nil, "a tool failure is reported to the caller")
			assert.Eq(t, jobstore.SessionMessageFailed, m.Status)
			assert.Eq(t, want, m.Error)
			assert.Eq(t, 0, f.submitted, "the resident path must not fall back to a one-shot job")
		})
	}
}

// Both messenger paths are handed the session's address, not just its name (the prompt
// the processing path uses is the same function, so one case per path proves the wiring).
func TestMessengerPathsUseTheSessionAddress(t *testing.T) {
	t.Run("resident", func(t *testing.T) {
		s := newSvc(t)
		f := &messageTargetFake{resident: ResidentResult{Sent: true}}
		s.SetMessenger(f)
		registerPeerSession(t, s, "sid-a", "server", "proj-a", peerTestUnix)
		m, err := s.SendMessage(context.Background(), "sid-a", "hi", "alice", "alice")
		assert.NoErr(t, err)
		assert.Eq(t, jobstore.SessionMessageDelivered, m.Status)
		assert.True(t, strings.Contains(promptArg(t, f.commands[0]), "uds:"+peerTestUnix), "%v", f.commands[0])
	})

	t.Run("one-shot", func(t *testing.T) {
		s := newSvc(t)
		f := &messageTargetFake{}
		s.SetMessenger(f)
		registerPeerSession(t, s, "sid-w", "builder", "proj-a", peerTestUnix)
		m, err := s.SendMessage(context.Background(), "sid-w", "hi", "alice", "alice")
		assert.NoErr(t, err)
		assert.Eq(t, jobstore.SessionMessageDelivered, m.Status)
		assert.Eq(t, 1, f.submitted)
		assert.True(t, strings.Contains(promptArg(t, f.commands[0]), "uds:"+peerTestUnix), "%v", f.commands[0])
	})
}

// The wake path is the third prompt site: a wake to a session with an address names it
// too, and the wake text stays the whole message.
func TestWakeMessengerPromptUsesTheAddress(t *testing.T) {
	for _, target := range []messengerTarget{
		{Name: "peer-a", Socket: peerTestUnix},
		{Name: "peer-a", Socket: peerTestPipe, Fallback: true},
	} {
		p := wakeMessengerPrompt(i18n.ZhCN, target, "<gofer-mail-wake/>")
		assert.True(t, strings.Contains(p, "uds:"+target.Socket), "%q", p)
		assert.True(t, strings.HasSuffix(p, "\n<gofer-mail-wake/>"), "%q", p)
		assert.True(t, strings.Contains(p, "完整消息"), "%q", p)
	}
	assert.Eq(t, wakeMessengerPrompt(i18n.ZhCN, messengerTarget{Name: "peer-a"}, "<gofer-mail-wake/>"),
		messengerInstruction(i18n.ZhCN, messengerTarget{Name: "peer-a"}, "wake_prompt")+"\n<gofer-mail-wake/>")
}

// With a namesake alive the name is the one target that delivers into the wrong session,
// so the prompt does not hand it to the model; a fallback prompt must name it, and says
// the first call goes to the address.
func TestAddressPromptNamesTheSessionOnlyWithFallback(t *testing.T) {
	first := map[i18n.Lang]string{i18n.ZhCN: "第一次调用", i18n.EN: "your first call"}
	for _, lang := range []i18n.Lang{i18n.EN, i18n.ZhCN} {
		for _, base := range []string{"prompt", "wake_prompt"} {
			strict := messengerInstruction(lang, messengerTarget{Name: "proj-4d", Socket: peerTestPipe}, base)
			assert.False(t, strings.Contains(strict, "proj-4d"), "%s %s must not carry the name: %q", lang, base, strict)
			assert.True(t, strings.Contains(strict, "uds:"+peerTestPipe), "%s %s keeps the backslashes: %q", lang, base, strict)

			fallback := messengerInstruction(lang, messengerTarget{Name: "proj-4d", Socket: peerTestPipe, Fallback: true}, base)
			assert.True(t, strings.Contains(fallback, `"proj-4d"`), "%s %s names the fallback: %q", lang, base, fallback)
			assert.True(t, strings.Contains(fallback, first[lang]), "%s %s puts the address first: %q", lang, base, fallback)
			assert.True(t, strings.Contains(fallback, "uds:"+peerTestPipe), "%s %s keeps the backslashes: %q", lang, base, fallback)
		}
	}
}

func TestMessengerSaidSentAcceptsAnExplainedReceipt(t *testing.T) {
	for _, out := range []string{"已发送。消息已投递到收件箱，对方尚未读取。", "**已发送**。已投递", "Sent. It is in the inbox."} {
		assert.True(t, messengerSaidSent(out), "%q is a receipt", out)
	}
	for _, out := range []string{"已发送失败。", "已发送，但对方拒收。", "not sent. the socket is stale", "send failed: sent."} {
		assert.False(t, messengerSaidSent(out), "%q is not a receipt", out)
	}
}
