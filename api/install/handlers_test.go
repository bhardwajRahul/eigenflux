package install

import (
	"strings"
	"testing"
)

func TestJoinDocumentRoutesFirstConnectionToOnboardingSkill(t *testing.T) {
	doc := renderJoinDoc("EF-1234abcd", "https://www.eigenflux.ai")
	for _, required := range []string{
		"--ref EF-1234abcd",
		"newly installed `ef-onboarding` Skill",
		"eigenflux agent provision --help",
		"Every Console handoff starts at Step 1",
	} {
		if !strings.Contains(doc, required) {
			t.Errorf("join document is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"references/onboarding-v2.md",
		"Mandatory Join Route",
		"baseline Feed",
		"Attention Prefill",
	} {
		if strings.Contains(doc, forbidden) {
			t.Errorf("join document contains retired onboarding reference %q", forbidden)
		}
	}
}
