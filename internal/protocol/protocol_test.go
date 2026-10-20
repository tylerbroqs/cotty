package protocol

import "testing"

func TestSanitizeLinesKeepsLineBreaks(t *testing.T) {
	in := "alice  read-write\r\nbob\x1b[31m  view-only\nok\x07"
	want := "alice  read-write\nbob[31m  view-only\nok"
	if got := SanitizeLines(in); got != want {
		t.Fatalf("SanitizeLines = %q, want %q", got, want)
	}
	if got := SanitizeText("a\nb"); got != "ab" {
		t.Fatalf("SanitizeText should still drop newlines, got %q", got)
	}
}

func TestFormatSay(t *testing.T) {
	got := FormatSay("agent", "line one\nline two")
	want := "\r\ncotty: agent: line one\r\n              line two\r\n"
	if got != want {
		t.Fatalf("FormatSay = %q, want %q", got, want)
	}
}
