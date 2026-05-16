package clawdbridge

import (
	"strings"
	"testing"
	"time"
)

func TestBrokerResolveAllow(t *testing.T) {
	b := NewBroker(time.Second)
	id, ch, err := b.CreateRequest("title", "detail")
	if err != nil {
		t.Fatalf("CreateRequest() error: %v", err)
	}
	if id == "" {
		t.Fatal("CreateRequest() returned empty id")
	}
	if !b.Resolve(id, DecisionAllow) {
		t.Fatal("Resolve() = false, want true")
	}

	result := <-ch
	if result.Decision != DecisionAllow {
		t.Fatalf("decision = %q, want allow", result.Decision)
	}
	if b.PendingCount() != 0 {
		t.Fatalf("PendingCount() = %d, want 0", b.PendingCount())
	}
}

func TestBrokerTimeout(t *testing.T) {
	b := NewBroker(15 * time.Millisecond)
	_, ch, err := b.CreateRequest("title", "detail")
	if err != nil {
		t.Fatalf("CreateRequest() error: %v", err)
	}

	select {
	case result := <-ch:
		if result.Decision != DecisionTimeout {
			t.Fatalf("decision = %q, want timeout", result.Decision)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for broker timeout")
	}
	if b.PendingCount() != 0 {
		t.Fatalf("PendingCount() = %d, want 0", b.PendingCount())
	}
}

func TestBrokerRepeatedResolveReturnsFalse(t *testing.T) {
	b := NewBroker(time.Second)
	id, ch, err := b.CreateRequest("title", "detail")
	if err != nil {
		t.Fatalf("CreateRequest() error: %v", err)
	}
	if !b.Resolve(id, DecisionDeny) {
		t.Fatal("first Resolve() = false, want true")
	}
	if b.Resolve(id, DecisionAllow) {
		t.Fatal("second Resolve() = true, want false")
	}
	if got := <-ch; got.Decision != DecisionDeny {
		t.Fatalf("decision = %q, want deny", got.Decision)
	}
	if _, ok := <-ch; ok {
		t.Fatal("result channel stayed open after terminal result")
	}
}

func TestBrokerCancelRemovesPending(t *testing.T) {
	b := NewBroker(time.Second)
	id, ch, err := b.CreateRequest("title", "detail")
	if err != nil {
		t.Fatalf("CreateRequest() error: %v", err)
	}
	if !b.Cancel(id) {
		t.Fatal("Cancel() = false, want true")
	}
	if got := <-ch; got.Decision != DecisionCanceled {
		t.Fatalf("decision = %q, want canceled", got.Decision)
	}
	if b.PendingCount() != 0 {
		t.Fatalf("PendingCount() = %d, want 0", b.PendingCount())
	}
}

func TestBrokerCancelAll(t *testing.T) {
	b := NewBroker(time.Second)
	_, ch1, err := b.CreateRequest("one", "")
	if err != nil {
		t.Fatalf("CreateRequest() error: %v", err)
	}
	_, ch2, err := b.CreateRequest("two", "")
	if err != nil {
		t.Fatalf("CreateRequest() error: %v", err)
	}
	if got := b.CancelAll(); got != 2 {
		t.Fatalf("CancelAll() = %d, want 2", got)
	}
	for i, ch := range []<-chan Result{ch1, ch2} {
		if got := <-ch; got.Decision != DecisionCanceled {
			t.Fatalf("channel %d decision = %q, want canceled", i, got.Decision)
		}
	}
	if b.PendingCount() != 0 {
		t.Fatalf("PendingCount() = %d, want 0", b.PendingCount())
	}
}

func TestBrokerTimeoutAfterResolveDoesNothing(t *testing.T) {
	b := NewBroker(10 * time.Millisecond)
	id, ch, err := b.CreateRequest("title", "detail")
	if err != nil {
		t.Fatalf("CreateRequest() error: %v", err)
	}
	if !b.Resolve(id, DecisionAllow) {
		t.Fatal("Resolve() = false, want true")
	}
	if got := <-ch; got.Decision != DecisionAllow {
		t.Fatalf("decision = %q, want allow", got.Decision)
	}
	time.Sleep(30 * time.Millisecond)
	if b.PendingCount() != 0 {
		t.Fatalf("PendingCount() = %d, want 0", b.PendingCount())
	}
	if _, ok := <-ch; ok {
		t.Fatal("channel had a second result")
	}
}

func TestCallbackDataLengthAndParse(t *testing.T) {
	b := NewBroker(time.Second)
	id, _, err := b.CreateRequest("title", "detail")
	if err != nil {
		t.Fatalf("CreateRequest() error: %v", err)
	}
	if len(id) > maxRequestIDHexChars {
		t.Fatalf("request id length = %d, want <= %d", len(id), maxRequestIDHexChars)
	}

	data, err := CallbackData(id, DecisionAllow)
	if err != nil {
		t.Fatalf("CallbackData() error: %v", err)
	}
	if len([]byte(data)) > maxCallbackDataBytes {
		t.Fatalf("callback length = %d, want <= %d", len([]byte(data)), maxCallbackDataBytes)
	}
	gotID, decision, ok := ParseCallbackData(data)
	if !ok {
		t.Fatal("ParseCallbackData() ok = false")
	}
	if gotID != id || decision != DecisionAllow {
		t.Fatalf("ParseCallbackData() = %q, %q; want %q, allow", gotID, decision, id)
	}
}

func TestCallbackDataRejectsUnknownDecision(t *testing.T) {
	if _, err := CallbackData("abc", Decision("always")); err == nil {
		t.Fatal("CallbackData() error = nil, want error")
	}
	_, _, ok := ParseCallbackData("clawdperm:abc:always")
	if ok {
		t.Fatal("ParseCallbackData() ok = true, want false")
	}
}

func TestHandleCallbackUnauthorizedDoesNotResolve(t *testing.T) {
	b := NewBroker(time.Second)
	id, ch, err := b.CreateRequest("title", "detail")
	if err != nil {
		t.Fatalf("CreateRequest() error: %v", err)
	}
	data, err := CallbackData(id, DecisionAllow)
	if err != nil {
		t.Fatalf("CallbackData() error: %v", err)
	}

	outcome := b.HandleCallback("", "123", data)
	if outcome.Status != CallbackUnauthorized {
		t.Fatalf("empty allowed status = %q, want unauthorized", outcome.Status)
	}
	if b.PendingCount() != 1 {
		t.Fatalf("PendingCount() = %d, want 1", b.PendingCount())
	}

	outcome = b.HandleCallback("123", "999", data)
	if outcome.Status != CallbackUnauthorized {
		t.Fatalf("status = %q, want unauthorized", outcome.Status)
	}
	if b.PendingCount() != 1 {
		t.Fatalf("PendingCount() = %d, want 1", b.PendingCount())
	}

	outcome = b.HandleCallback("123", "123", data)
	if outcome.Status != CallbackResolved || outcome.Decision != DecisionAllow {
		t.Fatalf("authorized callback outcome = %+v, want resolved allow", outcome)
	}
	if got := <-ch; got.Decision != DecisionAllow {
		t.Fatalf("decision = %q, want allow", got.Decision)
	}
}

func TestHandleCallbackMalformedDoesNotResolve(t *testing.T) {
	b := NewBroker(time.Second)
	_, _, err := b.CreateRequest("title", "detail")
	if err != nil {
		t.Fatalf("CreateRequest() error: %v", err)
	}

	for _, data := range []string{"", "perm:abc:allow", "clawdperm::allow", "clawdperm:abc:always", "clawdperm:abc:allow:extra"} {
		t.Run(strings.ReplaceAll(data, ":", "_"), func(t *testing.T) {
			outcome := b.HandleCallback("123", "123", data)
			if outcome.Status != CallbackMalformed {
				t.Fatalf("status = %q, want malformed", outcome.Status)
			}
			if b.PendingCount() != 1 {
				t.Fatalf("PendingCount() = %d, want 1", b.PendingCount())
			}
		})
	}
}

func TestHandleCallbackRepeatedClick(t *testing.T) {
	b := NewBroker(time.Second)
	id, _, err := b.CreateRequest("title", "detail")
	if err != nil {
		t.Fatalf("CreateRequest() error: %v", err)
	}
	data, err := CallbackData(id, DecisionDeny)
	if err != nil {
		t.Fatalf("CallbackData() error: %v", err)
	}
	if outcome := b.HandleCallback("123", "123", data); outcome.Status != CallbackResolved {
		t.Fatalf("first status = %q, want resolved", outcome.Status)
	}
	if outcome := b.HandleCallback("123", "123", data); outcome.Status != CallbackAlreadyHandled {
		t.Fatalf("second status = %q, want already_handled", outcome.Status)
	}
}

func TestBrokerConcurrentResolveCancelAndTimeout(t *testing.T) {
	b := NewBroker(time.Millisecond)
	done := make(chan struct{}, 100)
	for i := 0; i < 100; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			id, ch, err := b.CreateRequest("title", "detail")
			if err != nil {
				t.Errorf("CreateRequest() error: %v", err)
				return
			}
			switch i % 3 {
			case 0:
				b.Resolve(id, DecisionAllow)
			case 1:
				b.Cancel(id)
			default:
				<-ch
				return
			}
			select {
			case <-ch:
			case <-time.After(time.Second):
				t.Errorf("request %d did not resolve", i)
			}
		}(i)
	}
	for i := 0; i < 100; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for concurrent broker operations")
		}
	}
	if b.PendingCount() != 0 {
		t.Fatalf("PendingCount() = %d, want 0", b.PendingCount())
	}
}
