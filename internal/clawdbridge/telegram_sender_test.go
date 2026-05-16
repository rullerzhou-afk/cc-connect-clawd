package clawdbridge

import (
	"context"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

type fakePlainTextButtonSender struct {
	replyCtx any
	content  string
	buttons  [][]core.ButtonOption
}

func (f *fakePlainTextButtonSender) SendPlainTextWithButtons(_ context.Context, replyCtx any, content string, buttons [][]core.ButtonOption) error {
	f.replyCtx = replyCtx
	f.content = content
	f.buttons = buttons
	return nil
}

func TestTelegramApprovalSenderUsesPlainTextButtons(t *testing.T) {
	fake := &fakePlainTextButtonSender{}
	replyCtx := struct{ chat string }{"target"}
	sender := NewTelegramApprovalSender(fake, replyCtx)

	msg := ApprovalMessage{
		Title:             "run <cmd> & keep _literal_ *stars* `ticks`",
		Detail:            "detail > summary",
		AllowCallbackData: "clawdperm:abc:allow",
		DenyCallbackData:  "clawdperm:abc:deny",
	}
	if err := sender.SendApproval(context.Background(), msg); err != nil {
		t.Fatalf("SendApproval() error: %v", err)
	}
	if fake.replyCtx != replyCtx {
		t.Fatalf("replyCtx = %#v, want %#v", fake.replyCtx, replyCtx)
	}
	wantContent := "Clawd approval request\n\nrun <cmd> & keep _literal_ *stars* `ticks`\n\ndetail > summary"
	if fake.content != wantContent {
		t.Fatalf("content = %q, want %q", fake.content, wantContent)
	}
	if len(fake.buttons) != 1 || len(fake.buttons[0]) != 2 {
		t.Fatalf("buttons = %#v, want one row with two buttons", fake.buttons)
	}
	if fake.buttons[0][0].Data != "clawdperm:abc:allow" || fake.buttons[0][1].Data != "clawdperm:abc:deny" {
		t.Fatalf("button data = %#v", fake.buttons)
	}
}
