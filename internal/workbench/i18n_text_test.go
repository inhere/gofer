package workbench

import (
	"fmt"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/testutil/golden"
)

// attachTextOutputs renders the workbench's display text in lang. The zh-CN golden was
// recorded from the code BEFORE the text moved into the catalog (S3f).
func attachTextOutputs(lang i18n.Lang) string {
	var sb strings.Builder
	for i, in := range []RouteInput{
		{Pty: true}, {ACPAccepts: true}, {Agent: "suga"}, {}, {Agent: "claude", FilePath: true},
		{Agent: "claude", FilePath: true, Runner: "w-lab"}, {Agent: "claude", FilePath: true, Runner: "w-lab", Project: "p", Arg: true},
	} {
		in.Lang = lang
		r := RouteAttachments(in)
		sb.WriteString(fmt.Sprintf("route %d -> %s %q\n", i, r.Mode, r.Reason))
	}
	sb.WriteString(i18n.T(lang, "workbench.attach.ended") + "\n")
	sb.WriteString(ownDirReason(lang, "w-lab") + "\n" + ownDirReason(lang, "") + "\n")
	sb.WriteString(attachMarker(lang, 1, "/abs/x.png", "", "p") + "\n" + attachMarker(lang, 2, "", ".gofer/a/x.png", "alpha") + "\n")
	sb.WriteString(i18n.T(lang, "workbench.diff.captured_notice") + "\n")
	return sb.String()
}

func TestWorkbenchTextGolden(t *testing.T) {
	for _, lang := range i18n.Supported() {
		t.Run(string(lang), func(t *testing.T) { golden.Check(t, lang, "attach_text", attachTextOutputs(lang)) })
	}
}
