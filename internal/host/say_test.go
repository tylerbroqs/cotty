package host

import (
	"testing"
	"time"
)

func TestAnswersGoToOldestAsk(t *testing.T) {
	var a answers
	if a.deliver("nobody asked") {
		t.Fatal("deliver with no pending ask should report false")
	}

	first, second := make(chan string), make(chan string)
	go func() { s, _ := a.wait(); first <- s }()
	waitPending(t, &a, 1)
	go func() { s, _ := a.wait(); second <- s }()
	waitPending(t, &a, 2)

	if !a.deliver("one") || !a.deliver("two") {
		t.Fatal("deliver should reach the pending asks")
	}
	if got := <-first; got != "one" {
		t.Errorf("first ask got %q, want %q", got, "one")
	}
	if got := <-second; got != "two" {
		t.Errorf("second ask got %q, want %q", got, "two")
	}
}

func waitPending(t *testing.T, a *answers, n int) {
	t.Helper()
	for i := 0; i < 200; i++ {
		a.mu.Lock()
		got := len(a.waiters)
		a.mu.Unlock()
		if got == n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected %d pending asks", n)
}

func TestSayLimiter(t *testing.T) {
	var l sayLimiter
	for i := 0; i < sayBurst; i++ {
		if !l.allow("bob") {
			t.Fatalf("message %d should pass", i)
		}
	}
	if l.allow("bob") {
		t.Error("burst over the limit should be dropped")
	}
	if !l.allow("agent") {
		t.Error("limits are per sender")
	}
}
