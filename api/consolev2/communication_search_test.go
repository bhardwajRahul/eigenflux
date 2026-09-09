package consolev2

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCommunicationSearchPreviewIncludesFirstLiteralMatch(t *testing.T) {
	for _, test := range []struct {
		name, content, query, want string
	}{
		{"short", "hello NEEDLE", "needle", "hello NEEDLE"},
		{"late ASCII", strings.Repeat("a", 1500) + "NEEDLE" + strings.Repeat("b", 1500), "needle", "NEEDLE"},
		{"late Chinese", strings.Repeat("前", 1500) + "合同编号" + strings.Repeat("后", 1500), "合同编号", "合同编号"},
		{"literal", strings.Repeat("a", 1500) + "_%!" + strings.Repeat("b", 1500), "_%!", "_%!"},
		{"first match", strings.Repeat("a", 1500) + "Needle" + strings.Repeat("b", 1500) + "NEEDLE", "needle", "Needle"},
		{"rune offsets", strings.Repeat("İ", 1500) + "NEEDLE" + strings.Repeat("后", 1500), "needle", "NEEDLE"},
	} {
		t.Run(test.name, func(t *testing.T) {
			preview := communicationSearchPreview(test.content, test.query, 1000)
			if !strings.Contains(preview, test.want) || utf8.RuneCountInString(preview) > 1000 || !utf8.ValidString(preview) {
				t.Fatalf("preview lost literal match or exceeded budget: %q", preview)
			}
		})
	}
}

func TestCommunicationSearchCursorRoundTrip(t *testing.T) {
	want := communicationSearchCursor{Rank: 3, MatchedAt: 1720000000000, ConvID: 42}
	got, err := decodeCommunicationSearchCursor(encodeCommunicationSearchCursor(want))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("cursor mismatch: got %#v want %#v", got, want)
	}
}

func TestCommunicationSearchCursorRejectsInvalidValues(t *testing.T) {
	for _, raw := range []string{"not-base64", encodeCommunicationSearchCursor(communicationSearchCursor{Rank: 5, MatchedAt: 1, ConvID: 1})} {
		if _, err := decodeCommunicationSearchCursor(raw); err == nil {
			t.Fatalf("expected invalid cursor for %q", raw)
		}
	}
}
