package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/chenhg5/cc-connect/internal/clawdbridge"
	"github.com/chenhg5/cc-connect/platform/telegram"
)

const (
	configPathEnv   = "CLAWD_BRIDGE_CONFIG"
	botTokenFileEnv = "CLAWD_TG_BOT_TOKEN_FILE"
	clawdAppName    = "Clawd on Desk"
	readyTimeout    = 20 * time.Second
	stopTimeout     = 5 * time.Second
)

var (
	version   = "dev"
	commit    = "none"
	buildTime = "unknown"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, clawdbridge.RedactText(err.Error()))
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer, getenv func(string) string) error {
	fs := flag.NewFlagSet("cc-connect-clawd", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", defaultConfigPath(getenv), "path to clawd bridge TOML config")
	envFile := fs.String("env-file", defaultTokenEnvFilePath(getenv), "path to token env file")
	showVersion := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		fmt.Fprintf(stdout, "cc-connect-clawd %s\ncommit:  %s\nbuilt:   %s\n", version, commit, buildTime)
		return nil
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg, err := clawdbridge.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	if !cfg.Enabled {
		slog.Info("clawdbridge: disabled by config")
		return nil
	}
	if !cfg.TargetConfigured() {
		return errors.New("clawdbridge: target_session_key is required when enabled")
	}

	botToken, err := loadBotToken(*envFile, getenv)
	if err != nil {
		return err
	}

	platform, err := newTelegramPlatform(botToken, cfg)
	if err != nil {
		return err
	}
	replyCtx, err := platform.ReconstructReplyCtx(cfg.TargetSessionKey)
	if err != nil {
		return redactConfigError(fmt.Errorf("clawdbridge: reconstruct Telegram target: %w", err), cfg)
	}

	broker := clawdbridge.NewBroker(cfg.TTL())
	platform.SetClawdCallbackHandler(newClawdCallbackHandler(broker, cfg.AllowedTGUserID))

	readyCh, unavailableCh := platformReadyChannels(platform)
	if err := platform.Start(noopMessageHandler); err != nil {
		return err
	}
	started := false
	defer func() {
		if !started {
			_ = platform.Stop()
		}
	}()
	if err := waitForPlatformReady(readyCh, unavailableCh, readyTimeout); err != nil {
		return redactConfigError(err, cfg)
	}

	server, err := clawdbridge.NewServer(clawdbridge.ServerOptions{
		Config:          cfg,
		Broker:          broker,
		Sender:          clawdbridge.NewTelegramApprovalSender(platform, replyCtx),
		HandshakeWriter: stdout,
	})
	if err != nil {
		return redactConfigError(err, cfg)
	}
	if err := server.Start(); err != nil {
		return redactConfigError(err, cfg)
	}
	started = true

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	serverErr := server.Stop(shutdownCtx)
	platformErr := platform.Stop()
	if serverErr != nil {
		return redactConfigError(serverErr, cfg)
	}
	return redactConfigError(platformErr, cfg)
}

func noopMessageHandler(core.Platform, *core.Message) {}

func newTelegramPlatform(botToken string, cfg clawdbridge.Config) (*telegram.Platform, error) {
	p, err := telegram.New(map[string]any{
		"token":      botToken,
		"allow_from": cfg.AllowedTGUserID,
	})
	if err != nil {
		return nil, err
	}
	tg, ok := p.(*telegram.Platform)
	if !ok {
		return nil, fmt.Errorf("clawdbridge: unexpected Telegram platform type %T", p)
	}
	return tg, nil
}

func newClawdCallbackHandler(broker *clawdbridge.Broker, allowedUserID string) telegram.ClawdCallbackHandler {
	return func(_ context.Context, fromUserID, data string) telegram.ClawdCallbackResult {
		outcome := broker.HandleCallback(allowedUserID, fromUserID, data)
		switch outcome.Status {
		case clawdbridge.CallbackResolved:
			if outcome.Decision == clawdbridge.DecisionAllow {
				return telegram.ClawdCallbackResult{AnswerText: "Allowed", MessageSuffix: "Allowed", EditMessage: true}
			}
			return telegram.ClawdCallbackResult{AnswerText: "Denied", MessageSuffix: "Denied", EditMessage: true}
		case clawdbridge.CallbackUnauthorized:
			return telegram.ClawdCallbackResult{AnswerText: "Not authorized"}
		case clawdbridge.CallbackMalformed:
			return telegram.ClawdCallbackResult{AnswerText: "Invalid approval"}
		case clawdbridge.CallbackAlreadyHandled:
			return telegram.ClawdCallbackResult{AnswerText: "Already handled"}
		default:
			return telegram.ClawdCallbackResult{AnswerText: "Unknown approval"}
		}
	}
}

type sidecarLifecycle struct {
	ready       chan<- struct{}
	unavailable chan<- error
}

func (h sidecarLifecycle) OnPlatformReady(core.Platform) {
	select {
	case h.ready <- struct{}{}:
	default:
	}
}

func (h sidecarLifecycle) OnPlatformUnavailable(_ core.Platform, err error) {
	select {
	case h.unavailable <- err:
	default:
	}
}

func platformReadyChannels(p *telegram.Platform) (<-chan struct{}, <-chan error) {
	readyCh := make(chan struct{}, 1)
	unavailableCh := make(chan error, 1)
	p.SetLifecycleHandler(sidecarLifecycle{ready: readyCh, unavailable: unavailableCh})
	return readyCh, unavailableCh
}

func waitForPlatformReady(readyCh <-chan struct{}, unavailableCh <-chan error, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = readyTimeout
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	var lastUnavailable error
	var unavailableTimer *time.Timer
	var unavailableC <-chan time.Time
	defer func() {
		if unavailableTimer != nil {
			unavailableTimer.Stop()
		}
	}()

	for {
		select {
		case <-readyCh:
			return nil
		case err := <-unavailableCh:
			if err == nil {
				err = errors.New("unknown Telegram platform error")
			}
			lastUnavailable = err
			grace := unavailableGrace(timeout)
			if unavailableTimer == nil {
				unavailableTimer = time.NewTimer(grace)
				unavailableC = unavailableTimer.C
			} else {
				if !unavailableTimer.Stop() {
					select {
					case <-unavailableTimer.C:
					default:
					}
				}
				unavailableTimer.Reset(grace)
			}
		case <-unavailableC:
			return fmt.Errorf("clawdbridge: Telegram platform unavailable during startup: %w", lastUnavailable)
		case <-deadline.C:
			if lastUnavailable != nil {
				return fmt.Errorf("clawdbridge: Telegram platform did not become ready within %s: %w", timeout, lastUnavailable)
			}
			return fmt.Errorf("clawdbridge: Telegram platform did not become ready within %s", timeout)
		}
	}
}

func unavailableGrace(timeout time.Duration) time.Duration {
	grace := timeout / 2
	if grace <= 0 {
		return timeout
	}
	if grace > 3*time.Second {
		return 3 * time.Second
	}
	return grace
}

func redactConfigError(err error, cfg clawdbridge.Config) error {
	if err == nil {
		return nil
	}
	return errors.New(clawdbridge.RedactTextWithSecrets(err.Error(), cfg.RedactionSecrets()))
}

func loadBotToken(envFile string, getenv func(string) string) (string, error) {
	if token := strings.TrimSpace(getenv(clawdbridge.BotTokenEnv)); token != "" {
		return token, nil
	}
	values, err := readEnvFile(envFile)
	if err != nil {
		return "", err
	}
	if token := strings.TrimSpace(values[clawdbridge.BotTokenEnv]); token != "" {
		return token, nil
	}
	return "", fmt.Errorf("clawdbridge: %s is required", clawdbridge.BotTokenEnv)
}

func readEnvFile(path string) (map[string]string, error) {
	out := map[string]string{}
	if strings.TrimSpace(path) == "" {
		return out, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, fmt.Errorf("clawdbridge: read token env file: %w", err)
	}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		idx := strings.Index(line, "=")
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		value := strings.TrimSpace(line[idx+1:])
		if unquoted, err := strconv.Unquote(value); err == nil {
			value = unquoted
		} else if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
			value = value[1 : len(value)-1]
		}
		if key != "" {
			out[key] = value
		}
	}
	return out, nil
}

func defaultConfigPath(getenv func(string) string) string {
	if value := strings.TrimSpace(getenv(configPathEnv)); value != "" {
		return value
	}
	if dir := defaultUserDataDir(getenv); dir != "" {
		return filepath.Join(dir, "cc-connect-clawd", "clawd-bridge.toml")
	}
	return ""
}

func defaultTokenEnvFilePath(getenv func(string) string) string {
	if value := strings.TrimSpace(getenv(botTokenFileEnv)); value != "" {
		return value
	}
	if dir := defaultUserDataDir(getenv); dir != "" {
		return filepath.Join(dir, "telegram-approval.env")
	}
	return ""
}

func defaultUserDataDir(getenv func(string) string) string {
	return defaultUserDataDirForGOOS(runtime.GOOS, getenv)
}

func defaultUserDataDirForGOOS(goos string, getenv func(string) string) string {
	switch goos {
	case "windows":
		if dir := strings.TrimSpace(getenv("APPDATA")); dir != "" {
			return filepath.Join(dir, clawdAppName)
		}
		if home := strings.TrimSpace(getenv("USERPROFILE")); home != "" {
			return filepath.Join(home, "AppData", "Roaming", clawdAppName)
		}
	case "darwin":
		if home := strings.TrimSpace(getenv("HOME")); home != "" {
			return filepath.Join(home, "Library", "Application Support", clawdAppName)
		}
	default:
		if dir := strings.TrimSpace(getenv("XDG_CONFIG_HOME")); dir != "" {
			return filepath.Join(dir, clawdAppName)
		}
		if home := strings.TrimSpace(getenv("HOME")); home != "" {
			return filepath.Join(home, ".config", clawdAppName)
		}
	}
	return ""
}
