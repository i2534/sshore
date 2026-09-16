package namepattern

import "testing"

func TestMatchSubstringCaseInsensitive(t *testing.T) {
	if !Match("APP", "logs/app.log", "app.log") {
		t.Fatal("substring match should be case-insensitive")
	}
	if Match("nope", "logs/app.log", "app.log") {
		t.Fatal("must not match unrelated name")
	}
	if !Match("", "anything", "anything") {
		t.Fatal("empty pattern matches everything")
	}
}

func TestMatchGlob(t *testing.T) {
	if !Match("*.log", "logs/a.log", "a.log") {
		t.Fatal("glob on basename should match")
	}
	if !Match("logs/*", "logs/a.txt", "a.txt") {
		t.Fatal("glob on relative path should match")
	}
	if Match("*.log", "logs/a.txt", "a.txt") {
		t.Fatal("glob should not match txt")
	}
}

func TestMatchBadGlobFallsBackToSubstring(t *testing.T) {
	// "[abc" 是 ErrBadPattern，必须回退子串而不是 panic 或全不匹配。
	if !Match("[abc", "x/[abc", "[abc") {
		t.Fatal("bad glob should fall back to substring")
	}
}
