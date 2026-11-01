package authz

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/testutil/golden"
)

// catalogText renders every label and description the catalog shows, in lang. The zh-CN
// golden was recorded from the code BEFORE the text moved into the catalog (S3f); it is
// also the text of the generated docs/reference/permissions.md.
func catalogText(lang i18n.Lang) string {
	var sb strings.Builder
	for _, p := range Permissions() {
		sb.WriteString(p.Name + "\t" + p.DescriptionIn(lang) + "\n")
	}
	for _, r := range []Risk{RiskLow, RiskMedium, RiskHigh, RiskDangerous, RiskRoot, "x"} {
		sb.WriteString("risk " + string(r) + "\t" + r.LabelIn(lang) + "\n")
	}
	for _, r := range []Source{SourceSeed, SourceConfig, SourceRoot, "x"} {
		sb.WriteString("source " + string(r) + "\t" + r.LabelIn(lang) + "\n")
	}
	for _, r := range []Scope{ScopeNo, ScopeYes, ScopePhase2, "x"} {
		sb.WriteString("scope " + string(r) + "\t" + r.LabelIn(lang) + "\n")
	}
	for _, r := range []SubjectKind{SubjectUsers, SubjectCaller, SubjectSession, "x"} {
		sb.WriteString("subject " + string(r) + "\t" + r.LabelIn(lang) + "\n")
	}
	return sb.String()
}

func TestCatalogTextGolden(t *testing.T) {
	for _, lang := range i18n.Supported() {
		t.Run(string(lang), func(t *testing.T) { golden.Check(t, lang, "catalog", catalogText(lang)) })
	}
}

// TestLabelWithoutLanguageIsChinese: Label() is the zh-CN text the generated reference uses.
func TestLabelWithoutLanguageIsChinese(t *testing.T) {
	if RiskDangerous.Label() != "危险" || SourceRoot.Label() != "仅根" || ScopePhase2.Label() != "第二期" || SubjectSession.Label() != "会话" {
		t.Errorf("Label() = %q %q %q %q", RiskDangerous.Label(), SourceRoot.Label(), ScopePhase2.Label(), SubjectSession.Label())
	}
}
