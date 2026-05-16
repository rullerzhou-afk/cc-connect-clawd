package clawdbridge

import (
	"context"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

type PlainTextButtonSender interface {
	SendPlainTextWithButtons(ctx context.Context, replyCtx any, content string, buttons [][]core.ButtonOption) error
}

type TelegramApprovalSender struct {
	sender   PlainTextButtonSender
	replyCtx any
}

func NewTelegramApprovalSender(sender PlainTextButtonSender, replyCtx any) *TelegramApprovalSender {
	return &TelegramApprovalSender{
		sender:   sender,
		replyCtx: replyCtx,
	}
}

func (s *TelegramApprovalSender) SendApproval(ctx context.Context, msg ApprovalMessage) error {
	content := FormatApprovalMessage(msg)
	buttons := [][]core.ButtonOption{{
		{Text: "Allow", Data: msg.AllowCallbackData},
		{Text: "Deny", Data: msg.DenyCallbackData},
	}}
	return s.sender.SendPlainTextWithButtons(ctx, s.replyCtx, content, buttons)
}

func FormatApprovalMessage(msg ApprovalMessage) string {
	title := strings.TrimSpace(msg.Title)
	detail := strings.TrimSpace(msg.Detail)

	var b strings.Builder
	b.WriteString("Clawd approval request")
	if title != "" {
		b.WriteString("\n\n")
		b.WriteString(title)
	}
	if detail != "" {
		b.WriteString("\n\n")
		b.WriteString(detail)
	}
	return b.String()
}
