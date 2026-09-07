package consolev2

import (
	"testing"
	"time"
)

func TestMaskHomeActivityName(t *testing.T) {
	for input, want := range map[string]string{"Atlas": "A***", "星河": "星***", "": "***"} {
		if got := maskHomeActivityName(input); got != want {
			t.Fatalf("mask(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestTruncateHomeActivityContent(t *testing.T) {
	got, truncated := truncateHomeActivityContent("一二三四", 3)
	if got != "一二三" || !truncated {
		t.Fatalf("truncate = %q, %v", got, truncated)
	}
}

func TestHomeActivityWindowStartUsesRollingDay(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC).UnixMilli()
	want := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC).UnixMilli()
	if got := homeActivityWindowStart(now); got != want {
		t.Fatalf("window start = %d, want %d", got, want)
	}
}

func TestMaskHomeActivityEnglishName(t *testing.T) {
	for _, tc := range []struct{ name, englishName, want string }{
		{"刘明", "Liu Ming", "L***"},
		{"小刘", "James", "J***"},
		{"刘明", "", "***"},
		{"James", "", "J***"},
		{"", "", "***"},
		{"刘明", " 刘明 ", "***"},
		{"刘明", " Émile ", "É***"},
	} {
		if got := maskHomeActivityEnglishName(tc.name, tc.englishName); got != tc.want {
			t.Fatalf("English mask = %q, want %q", got, tc.want)
		}
	}
}
