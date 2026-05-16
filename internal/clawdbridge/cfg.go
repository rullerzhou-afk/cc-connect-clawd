package clawdbridge

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	BotTokenEnv       = "CLAWD_TG_BOT_TOKEN"
	DefaultListenAddr = "127.0.0.1:0"
	DefaultTTLSeconds = 90
	DefaultTTL        = DefaultTTLSeconds * time.Second
)

type Config struct {
	Enabled          bool   `toml:"enabled"`
	AllowedTGUserID  string `toml:"allowed_tg_user_id"`
	TargetSessionKey string `toml:"target_session_key"`
	TargetChatID     string `toml:"target_chat_id"`
	TTLSeconds       int    `toml:"ttl_seconds"`
	ListenAddr       string `toml:"listen_addr"`
}

func DefaultConfig() Config {
	return Config{
		Enabled:    true,
		TTLSeconds: DefaultTTLSeconds,
		ListenAddr: DefaultListenAddr,
	}
}

func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()
	if strings.TrimSpace(path) == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("clawdbridge: read config: %w", err)
	}
	meta, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return Config{}, fmt.Errorf("clawdbridge: parse config failed")
	}
	for _, key := range meta.Undecoded() {
		for _, part := range key {
			if strings.EqualFold(part, "bot_token") {
				return Config{}, fmt.Errorf("clawdbridge: bot_token must not be stored in config; use %s", BotTokenEnv)
			}
		}
	}
	if err := cfg.NormalizeAndValidate(); err != nil {
		return Config{}, fmt.Errorf("%s", RedactTextWithSecrets(err.Error(), cfg.RedactionSecrets()))
	}
	return cfg, nil
}

func (c *Config) NormalizeAndValidate() error {
	c.AllowedTGUserID = strings.TrimSpace(c.AllowedTGUserID)
	c.TargetSessionKey = strings.TrimSpace(c.TargetSessionKey)
	c.TargetChatID = strings.TrimSpace(c.TargetChatID)
	c.ListenAddr = strings.TrimSpace(c.ListenAddr)

	if c.ListenAddr == "" {
		c.ListenAddr = DefaultListenAddr
	}
	if c.TTLSeconds == 0 {
		c.TTLSeconds = DefaultTTLSeconds
	}
	if c.TTLSeconds < 0 {
		return fmt.Errorf("clawdbridge: ttl_seconds must be positive")
	}
	if c.TargetSessionKey == "" && c.TargetChatID != "" {
		c.TargetSessionKey = "telegram:" + c.TargetChatID
	}
	if c.TargetSessionKey != "" {
		if err := validateTelegramSessionKey(c.TargetSessionKey); err != nil {
			return err
		}
	}
	if c.AllowedTGUserID != "" {
		if err := validateTelegramUserID(c.AllowedTGUserID); err != nil {
			return err
		}
	}
	if c.Enabled && c.TargetConfigured() && c.AllowedTGUserID == "" {
		return fmt.Errorf("clawdbridge: allowed_tg_user_id is required when approval target is configured")
	}
	if err := validateListenAddr(c.ListenAddr); err != nil {
		return err
	}
	return nil
}

func (c Config) TTL() time.Duration {
	if c.TTLSeconds <= 0 {
		return DefaultTTL
	}
	return time.Duration(c.TTLSeconds) * time.Second
}

func (c Config) TargetConfigured() bool {
	return strings.TrimSpace(c.TargetSessionKey) != ""
}

// RedactionSecrets intentionally over-redacts exact chat/user/session values.
// False positives are preferable to leaking Telegram identifiers in approval text or logs.
func (c Config) RedactionSecrets() []string {
	var out []string
	seen := map[string]bool{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		out = append(out, value)
	}

	add(c.AllowedTGUserID)
	add(c.TargetChatID)
	add(c.TargetSessionKey)
	if parts := strings.Split(c.TargetSessionKey, ":"); len(parts) > 1 && parts[0] == "telegram" {
		for _, part := range parts[1:] {
			add(part)
		}
	}
	return out
}

func validateListenAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("clawdbridge: invalid listen_addr")
	}
	if port == "" {
		return fmt.Errorf("clawdbridge: listen_addr port is required")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.Equal(net.ParseIP("127.0.0.1")) {
		return fmt.Errorf("clawdbridge: listen_addr must bind 127.0.0.1")
	}
	return nil
}

func validateTelegramUserID(userID string) error {
	n, err := strconv.ParseInt(userID, 10, 64)
	if err != nil || n <= 0 {
		return fmt.Errorf("clawdbridge: allowed_tg_user_id must be a positive numeric Telegram user id")
	}
	return nil
}

func validateTelegramSessionKey(sessionKey string) error {
	parts := strings.Split(sessionKey, ":")
	if len(parts) < 2 || len(parts) > 4 || parts[0] != "telegram" {
		return fmt.Errorf("clawdbridge: target_session_key must be telegram:{chat_id}[:thread_id][:user_id]")
	}
	chatID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return fmt.Errorf("clawdbridge: target_session_key has invalid Telegram chat id")
	}
	if chatID == 0 {
		return fmt.Errorf("clawdbridge: target_session_key chat id must not be zero")
	}
	for _, part := range parts[2:] {
		if part == "" {
			return fmt.Errorf("clawdbridge: target_session_key contains an empty numeric part")
		}
		if _, err := strconv.ParseInt(part, 10, 64); err != nil {
			return fmt.Errorf("clawdbridge: target_session_key contains a non-numeric part")
		}
	}
	return nil
}
