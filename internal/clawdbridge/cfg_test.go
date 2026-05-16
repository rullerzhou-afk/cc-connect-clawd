package clawdbridge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if !cfg.Enabled {
		t.Fatal("Enabled = false, want true")
	}
	if cfg.ListenAddr != DefaultListenAddr {
		t.Fatalf("ListenAddr = %q, want %q", cfg.ListenAddr, DefaultListenAddr)
	}
	if cfg.TTL() != DefaultTTL {
		t.Fatalf("TTL() = %v, want %v", cfg.TTL(), DefaultTTL)
	}
}

func TestLoadConfigNormalizesTargetChatID(t *testing.T) {
	path := writeConfig(t, `
enabled = true
target_chat_id = "123456789"
allowed_tg_user_id = "123456789"
ttl_seconds = 12
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if cfg.TargetSessionKey != "telegram:123456789" {
		t.Fatalf("TargetSessionKey = %q, want telegram:123456789", cfg.TargetSessionKey)
	}
	if cfg.AllowedTGUserID != "123456789" {
		t.Fatalf("AllowedTGUserID = %q, want 123456789", cfg.AllowedTGUserID)
	}
	if cfg.TTL() != 12*time.Second {
		t.Fatalf("TTL() = %v, want 12s", cfg.TTL())
	}
}

func TestLoadConfigRejectsMissingAllowedUserWhenTargetConfigured(t *testing.T) {
	path := writeConfig(t, `
enabled = true
target_session_key = "telegram:123456789"
`)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("LoadConfig() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "allowed_tg_user_id") {
		t.Fatalf("error = %q, want allowed_tg_user_id", err.Error())
	}
}

func TestLoadConfigRejectsBotToken(t *testing.T) {
	path := writeConfig(t, `
bot_token = "do-not-store-this"
target_session_key = "telegram:123"
`)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("LoadConfig() error = nil, want error")
	}
	if !strings.Contains(err.Error(), BotTokenEnv) {
		t.Fatalf("error = %q, want mention %s", err.Error(), BotTokenEnv)
	}
}

func TestLoadConfigRejectsNestedBotToken(t *testing.T) {
	path := writeConfig(t, `
[secrets]
bot_token = "do-not-store-this"
`)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("LoadConfig() error = nil, want error")
	}
	if !strings.Contains(err.Error(), BotTokenEnv) {
		t.Fatalf("error = %q, want mention %s", err.Error(), BotTokenEnv)
	}
}

func TestLoadConfigParseErrorDoesNotEchoRawValues(t *testing.T) {
	path := writeConfig(t, `
enabled = true
target_session_key = "telegram:123456789
allowed_tg_user_id = "123456789"
`)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("LoadConfig() error = nil, want parse error")
	}
	for _, leaked := range []string{"telegram:123456789", "123456789"} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("error leaked config value %q: %q", leaked, err.Error())
		}
	}
	if !strings.Contains(err.Error(), "parse config") {
		t.Fatalf("error = %q, want parse config", err.Error())
	}
}

func TestLoadConfigValidationErrorDoesNotEchoChatID(t *testing.T) {
	path := writeConfig(t, `
enabled = true
target_session_key = "telegram:123456789:thread"
allowed_tg_user_id = "123456789"
`)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("LoadConfig() error = nil, want validation error")
	}
	if strings.Contains(err.Error(), "123456789") {
		t.Fatalf("error leaked chat id: %q", err.Error())
	}
}

func TestConfigRejectsNonLocalListenAddr(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ListenAddr = "0.0.0.0:0"
	err := cfg.NormalizeAndValidate()
	if err == nil {
		t.Fatal("NormalizeAndValidate() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("error = %q, want 127.0.0.1", err.Error())
	}
}

func TestConfigRejectsInvalidTargetSessionKey(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TargetSessionKey = "slack:123"
	err := cfg.NormalizeAndValidate()
	if err == nil {
		t.Fatal("NormalizeAndValidate() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "telegram:") {
		t.Fatalf("error = %q, want telegram:", err.Error())
	}
}

func TestConfigAcceptsTelegramSessionKeyVariants(t *testing.T) {
	for _, key := range []string{"telegram:123", "telegram:-1001234567890:42", "telegram:123:42:789"} {
		t.Run(key, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.TargetSessionKey = key
			cfg.AllowedTGUserID = "789"
			if err := cfg.NormalizeAndValidate(); err != nil {
				t.Fatalf("NormalizeAndValidate() error: %v", err)
			}
		})
	}
}

func TestConfigRejectsNonNumericTelegramSessionKeyParts(t *testing.T) {
	for _, key := range []string{"telegram:", "telegram:0", "telegram:abc", "telegram:123:thread", "telegram:123:42:user", "telegram:123:42:789:extra"} {
		t.Run(key, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.TargetSessionKey = key
			cfg.AllowedTGUserID = "789"
			err := cfg.NormalizeAndValidate()
			if err == nil {
				t.Fatal("NormalizeAndValidate() error = nil, want error")
			}
		})
	}
}

func TestConfigRejectsInvalidAllowedUserID(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TargetSessionKey = "telegram:123"
	cfg.AllowedTGUserID = "not-a-number"
	err := cfg.NormalizeAndValidate()
	if err == nil {
		t.Fatal("NormalizeAndValidate() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "allowed_tg_user_id") {
		t.Fatalf("error = %q, want allowed_tg_user_id", err.Error())
	}
}

func TestConfigTargetConfigured(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.TargetConfigured() {
		t.Fatal("TargetConfigured() = true, want false")
	}
	cfg.TargetSessionKey = "telegram:123"
	if !cfg.TargetConfigured() {
		t.Fatal("TargetConfigured() = false, want true")
	}
}

func TestConfigRedactionSecrets(t *testing.T) {
	cfg := Config{
		AllowedTGUserID:  "987654321",
		TargetSessionKey: "telegram:-1001234567890:42:987654321",
		TargetChatID:     "-1001234567890",
	}
	secrets := cfg.RedactionSecrets()
	for _, want := range []string{"987654321", "-1001234567890", "42", "telegram:-1001234567890:42:987654321"} {
		found := false
		for _, got := range secrets {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("RedactionSecrets() missing %q in %#v", want, secrets)
		}
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "clawd-bridge.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
