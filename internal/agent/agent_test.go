package agent

import "testing"

func TestScreenStripsEscapesAndHandlesRedraws(t *testing.T) {
	var s screen
	s.Write([]byte("\x1b[1;32mtyler@devbox\x1b[0m $ ls\r\nREADME.md  go.mod\r\n"))
	if got, want := s.Snapshot(), "tyler@devbox $ ls\nREADME.md  go.mod"; got != want {
		t.Fatalf("snapshot = %q, want %q", got, want)
	}

	// A full-screen redraw (cotty desk) clears what came before.
	s.Write([]byte("\x1b[H\x1b[2JSYMBOL  BID\r\nTSLA    367.00\r\n"))
	if got, want := s.Snapshot(), "SYMBOL  BID\nTSLA    367.00"; got != want {
		t.Fatalf("after clear = %q, want %q", got, want)
	}
}

func TestScreenCarriageReturnBackspaceAndSplitWrites(t *testing.T) {
	var s screen
	s.Write([]byte("progress 10%\rprogress 99%\r\n"))
	s.Write([]byte("abx\bc\r\n"))
	// An escape sequence and a UTF-8 rune split across writes.
	s.Write([]byte("\x1b[3"))
	s.Write([]byte("1mred\x1b[0m \xe2\x9c"))
	s.Write([]byte("\x93 ok"))
	want := "progress 99%\nabc\nred ✓ ok"
	if got := s.Snapshot(); got != want {
		t.Fatalf("snapshot = %q, want %q", got, want)
	}
}

func TestScreenSkipsOSC(t *testing.T) {
	var s screen
	s.Write([]byte("\x1b]0;window title\x07hello\x1b]2;t\x1b\\ world"))
	if got := s.Snapshot(); got != "hello world" {
		t.Fatalf("snapshot = %q", got)
	}
}

func TestParseAction(t *testing.T) {
	cases := []struct {
		reply string
		want  action
	}{
		{`{"say":"hi","run":""}`, action{Say: "hi"}},
		{"Sure:\n```json\n{\"say\": \"adding\", \"run\": \"cotty desk -symbols NVDA\"}\n```",
			action{Say: "adding", Run: "cotty desk -symbols NVDA"}},
		{`{"say":"stop it","run":"","interrupt":true}`, action{Say: "stop it", Interrupt: true}},
		{"just text", action{Say: "just text"}},
		// A second line, or control keys, never reach the shell.
		{`{"say":"x","run":"ls\nrm -rf ~"}`, action{Say: "x", Run: "ls"}},
		{`{"say":"x","run":"echo hi\u001b[2J"}`, action{Say: "x", Run: "echo hi[2J"}},
	}
	for _, c := range cases {
		if got := parseAction(c.reply); got != c.want {
			t.Errorf("parseAction(%q) = %+v, want %+v", c.reply, got, c.want)
		}
	}
}
