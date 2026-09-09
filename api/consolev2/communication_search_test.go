package consolev2

import "testing"

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
