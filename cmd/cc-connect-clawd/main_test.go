package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/internal/clawdbridge"
)

func TestRunVersionDoesNotRequireConfigOrToken(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := run([]string{"--version"}, &stdout, &stderr, func(string) string { return "" }); err != nil {
		t.Fatalf("run(--version) error: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "cc-connect-clawd ") || !strings.Contains(out, "commit:") || !strings.Contains(out, "built:") {
		t.Fatalf("version output = %q", out)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestLoadBotTokenPrefersEnvironment(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), "token.env")
	fileToken := "123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi"
	envToken := "987654321:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi"
	if err := os.WriteFile(envPath, []byte(clawdbridge.BotTokenEnv+"="+fileToken+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}
	token, err := loadBotToken(envPath, func(key string) string {
		if key == clawdbridge.BotTokenEnv {
			return envToken
		}
		return ""
	})
	if err != nil {
		t.Fatalf("loadBotToken() error: %v", err)
	}
	if token != envToken {
		t.Fatalf("token = %q, want environment token", token)
	}
}

func TestLoadBotTokenReadsEnvFile(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), "token.env")
	want := "123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi"
	content := "# comment\nexport " + clawdbridge.BotTokenEnv + "=\"" + want + "\"\n"
	if err := os.WriteFile(envPath, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}
	token, err := loadBotToken(envPath, func(string) string { return "" })
	if err != nil {
		t.Fatalf("loadBotToken() error: %v", err)
	}
	if token != want {
		t.Fatalf("token = %q, want file token", token)
	}
}

func TestLoadBotTokenRejectsInvalidFormatWithoutEchoingToken(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), "token.env")
	badToken := "123:bad-token"
	if err := os.WriteFile(envPath, []byte(clawdbridge.BotTokenEnv+"="+badToken+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}
	_, err := loadBotToken(envPath, func(string) string { return "" })
	if err == nil {
		t.Fatal("loadBotToken() error = nil, want invalid token error")
	}
	if strings.Contains(err.Error(), badToken) {
		t.Fatalf("error leaked token value: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "format is invalid") {
		t.Fatalf("error = %q, want invalid format", err.Error())
	}
}

func TestLoadBotTokenRequiresToken(t *testing.T) {
	_, err := loadBotToken(filepath.Join(t.TempDir(), "missing.env"), func(string) string { return "" })
	if err == nil {
		t.Fatal("loadBotToken() error = nil, want missing token error")
	}
}

func TestRedactingWriterMasksTokenAndKnownSecrets(t *testing.T) {
	var buf bytes.Buffer
	writer := newRedactingWriter(&buf)
	writer.SetSecrets([]string{"telegram:123456789", "987654321"})
	input := "bot=123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi chat=telegram:123456789 user=987654321\n"
	n, err := writer.Write([]byte(input))
	if err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	if n != len(input) {
		t.Fatalf("Write() n = %d, want %d", n, len(input))
	}
	out := buf.String()
	for _, secret := range []string{"123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi", "telegram:123456789", "987654321"} {
		if strings.Contains(out, secret) {
			t.Fatalf("redacted output still contains %q: %q", secret, out)
		}
	}
}

func TestDefaultUserDataDirMatchesClawdProductName(t *testing.T) {
	getenv := func(key string) string {
		switch key {
		case "APPDATA":
			return `C:\Users\me\AppData\Roaming`
		case "USERPROFILE":
			return `C:\Users\me`
		case "HOME":
			return "/Users/me"
		case "XDG_CONFIG_HOME":
			return "/home/me/.config"
		default:
			return ""
		}
	}
	cases := []struct {
		goos string
		want string
	}{
		{goos: "windows", want: filepath.Join(`C:\Users\me\AppData\Roaming`, clawdAppName)},
		{goos: "darwin", want: filepath.Join("/Users/me", "Library", "Application Support", clawdAppName)},
		{goos: "linux", want: filepath.Join("/home/me/.config", clawdAppName)},
	}
	for _, tt := range cases {
		t.Run(tt.goos, func(t *testing.T) {
			if got := defaultUserDataDirForGOOS(tt.goos, getenv); got != tt.want {
				t.Fatalf("defaultUserDataDirForGOOS() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDefaultUserDataDirFallbacks(t *testing.T) {
	getenv := func(key string) string {
		switch key {
		case "USERPROFILE":
			return `C:\Users\me`
		case "HOME":
			return "/home/me"
		default:
			return ""
		}
	}
	if got := defaultUserDataDirForGOOS("windows", getenv); got != filepath.Join(`C:\Users\me`, "AppData", "Roaming", clawdAppName) {
		t.Fatalf("windows fallback = %q", got)
	}
	if got := defaultUserDataDirForGOOS("linux", getenv); got != filepath.Join("/home/me", ".config", clawdAppName) {
		t.Fatalf("linux fallback = %q", got)
	}
}

func TestDefaultConfigAndTokenPaths(t *testing.T) {
	getenv := func(key string) string {
		if runtime.GOOS == "windows" && key == "APPDATA" {
			return `C:\Users\me\AppData\Roaming`
		}
		if runtime.GOOS != "windows" && key == "HOME" {
			return "/home/me"
		}
		return ""
	}
	if got := defaultConfigPath(getenv); !strings.Contains(got, clawdAppName) || !strings.HasSuffix(got, filepath.Join("cc-connect-clawd", "clawd-bridge.toml")) {
		t.Fatalf("defaultConfigPath() = %q", got)
	}
	if got := defaultTokenEnvFilePath(getenv); !strings.Contains(got, clawdAppName) || !strings.HasSuffix(got, "telegram-approval.env") {
		t.Fatalf("defaultTokenEnvFilePath() = %q", got)
	}
}

func TestWaitForPlatformReadyIgnoresTransientUnavailable(t *testing.T) {
	readyCh := make(chan struct{}, 1)
	unavailableCh := make(chan error, 1)
	unavailableCh <- errors.New("temporary network failure")
	readyCh <- struct{}{}

	if err := waitForPlatformReady(readyCh, unavailableCh, 50*time.Millisecond); err != nil {
		t.Fatalf("waitForPlatformReady() error: %v", err)
	}
}

func TestWaitForPlatformReadyReturnsUnavailableAfterGrace(t *testing.T) {
	readyCh := make(chan struct{})
	unavailableCh := make(chan error, 1)
	unavailableCh <- errors.New("temporary network failure")

	start := time.Now()
	err := waitForPlatformReady(readyCh, unavailableCh, 40*time.Millisecond)
	if err == nil {
		t.Fatal("waitForPlatformReady() error = nil, want unavailable error")
	}
	if elapsed := time.Since(start); elapsed < 15*time.Millisecond {
		t.Fatalf("waitForPlatformReady() returned too quickly after unavailable: %v", elapsed)
	}
	if !strings.Contains(err.Error(), "temporary network failure") {
		t.Fatalf("error = %q, want unavailable cause", err.Error())
	}
}

func TestWaitForPlatformReadyTimesOut(t *testing.T) {
	err := waitForPlatformReady(make(chan struct{}), make(chan error), 10*time.Millisecond)
	if err == nil {
		t.Fatal("waitForPlatformReady() error = nil, want timeout")
	}
	if !strings.Contains(err.Error(), "did not become ready") {
		t.Fatalf("error = %q, want timeout", err.Error())
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
