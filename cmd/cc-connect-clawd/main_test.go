package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/internal/clawdbridge"
)

func TestLoadBotTokenPrefersEnvironment(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), "token.env")
	if err := os.WriteFile(envPath, []byte(clawdbridge.BotTokenEnv+"=from-file\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}
	token, err := loadBotToken(envPath, func(key string) string {
		if key == clawdbridge.BotTokenEnv {
			return "from-env"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("loadBotToken() error: %v", err)
	}
	if token != "from-env" {
		t.Fatalf("token = %q, want environment token", token)
	}
}

func TestLoadBotTokenReadsEnvFile(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), "token.env")
	content := "# comment\nexport " + clawdbridge.BotTokenEnv + "=\"from-file\"\n"
	if err := os.WriteFile(envPath, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}
	token, err := loadBotToken(envPath, func(string) string { return "" })
	if err != nil {
		t.Fatalf("loadBotToken() error: %v", err)
	}
	if token != "from-file" {
		t.Fatalf("token = %q, want file token", token)
	}
}

func TestLoadBotTokenRequiresToken(t *testing.T) {
	_, err := loadBotToken(filepath.Join(t.TempDir(), "missing.env"), func(string) string { return "" })
	if err == nil {
		t.Fatal("loadBotToken() error = nil, want missing token error")
	}
}

func TestNewClawdCallbackHandlerResolvesAllow(t *testing.T) {
	broker := clawdbridge.NewBroker(time.Minute)
	id, ch, err := broker.CreateRequest("title", "detail")
	if err != nil {
		t.Fatalf("CreateRequest() error: %v", err)
	}
	data, err := clawdbridge.CallbackData(id, clawdbridge.DecisionAllow)
	if err != nil {
		t.Fatalf("CallbackData() error: %v", err)
	}
	handler := newClawdCallbackHandler(broker, "123")

	result := handler(context.Background(), "123", data)
	if result.AnswerText != "Allowed" || result.MessageSuffix != "Allowed" || !result.EditMessage {
		t.Fatalf("result = %#v", result)
	}
	if got := <-ch; got.Decision != clawdbridge.DecisionAllow {
		t.Fatalf("decision = %q, want allow", got.Decision)
	}

	result = handler(context.Background(), "123", data)
	if result.AnswerText != "Already handled" || result.EditMessage {
		t.Fatalf("second result = %#v", result)
	}
}

func TestNewClawdCallbackHandlerRejectsUnauthorized(t *testing.T) {
	broker := clawdbridge.NewBroker(time.Minute)
	id, ch, err := broker.CreateRequest("title", "detail")
	if err != nil {
		t.Fatalf("CreateRequest() error: %v", err)
	}
	data, err := clawdbridge.CallbackData(id, clawdbridge.DecisionDeny)
	if err != nil {
		t.Fatalf("CallbackData() error: %v", err)
	}
	handler := newClawdCallbackHandler(broker, "123")

	result := handler(context.Background(), "999", data)
	if result.AnswerText != "Not authorized" || result.EditMessage {
		t.Fatalf("result = %#v", result)
	}
	select {
	case got := <-ch:
		t.Fatalf("unauthorized callback resolved request: %#v", got)
	default:
	}
}
