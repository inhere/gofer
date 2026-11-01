package settings

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/testutil/golden"
)

// descriptionOutputs lists every setting's description in lang. The zh-CN golden was
// recorded from the registry BEFORE the descriptions moved into the catalog.
func descriptionOutputs(lang i18n.Lang) string {
	var sb strings.Builder
	for _, d := range registry {
		sb.WriteString(d.Key + "\t" + d.DescriptionIn(lang) + "\n")
	}
	return sb.String()
}

func TestSettingDescriptionsGolden(t *testing.T) {
	for _, lang := range i18n.Supported() {
		t.Run(string(lang), func(t *testing.T) { golden.Check(t, lang, "descriptions", descriptionOutputs(lang)) })
	}
}

// TestListFollowsLanguage: the listing carries the description in the language asked for;
// List() is the default-language form.
func TestListFollowsLanguage(t *testing.T) {
	svc := NewService(nil)
	en, err := svc.ListIn(i18n.EN)
	if err != nil {
		t.Fatal(err)
	}
	zh, err := svc.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(en) != len(registry) || len(zh) != len(registry) {
		t.Fatalf("listing sizes %d / %d, registry %d", len(en), len(zh), len(registry))
	}
	for i, d := range registry {
		if en[i].Description != d.DescriptionIn(i18n.EN) || zh[i].Description != d.DescriptionIn(i18n.Default) {
			t.Errorf("%s: descriptions %q / %q", d.Key, en[i].Description, zh[i].Description)
		}
	}
}
