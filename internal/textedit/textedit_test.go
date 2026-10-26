package textedit

import (
	"regexp"
	"testing"
)

func TestReplaceFirstTouchesOnlyTheFirstMatch(t *testing.T) {
	re := regexp.MustCompile(`(?m)^(\s*listen:\s*)"[^"]*"`)
	in := "public:\n  listen: \":443\" # kept\nadmin:\n  listen: \":8443\"\n"
	got, ok := ReplaceFirst(re, in, `${1}"127.0.0.1:8081"`)
	if want := "public:\n  listen: \"127.0.0.1:8081\" # kept\nadmin:\n  listen: \":8443\"\n"; !ok || got != want {
		t.Fatalf("%v %q", ok, got)
	}
	if got, ok := ReplaceFirst(re, "listen: 443\n", `x`); ok || got != "listen: 443\n" {
		t.Fatalf("no match: %v %q", ok, got)
	}
}
