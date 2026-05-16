package clawdbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeSender struct {
	mu   sync.Mutex
	msgs []ApprovalMessage
	send func(context.Context, ApprovalMessage) error
}

func (f *fakeSender) SendApproval(ctx context.Context, msg ApprovalMessage) error {
	f.mu.Lock()
	f.msgs = append(f.msgs, msg)
	f.mu.Unlock()
	if f.send != nil {
		return f.send(ctx, msg)
	}
	return nil
}

func (f *fakeSender) lastMessage() ApprovalMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.msgs) == 0 {
		return ApprovalMessage{}
	}
	return f.msgs[len(f.msgs)-1]
}

func TestServerAuthFailures(t *testing.T) {
	srv := newTestServer(t, nil, &fakeSender{})
	req := httptest.NewRequest(http.MethodPost, approvalRequestPath, strings.NewReader(`{"title":"approve"}`))
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("missing bearer status = %d, want 401", rr.Code)
	}

	req = httptest.NewRequest(http.MethodPost, approvalRequestPath, strings.NewReader(`{"title":"approve"}`))
	req.Header.Set("Authorization", "Bearer wrong")
	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("wrong bearer status = %d, want 401", rr.Code)
	}
}

func TestServerAcceptsCaseInsensitiveBearerScheme(t *testing.T) {
	broker := NewBroker(time.Second)
	sender := &fakeSender{send: func(_ context.Context, msg ApprovalMessage) error {
		go broker.Resolve(msg.ID, DecisionAllow)
		return nil
	}}
	srv := newTestServer(t, broker, sender)
	req := httptest.NewRequest(http.MethodPost, approvalRequestPath, strings.NewReader(`{"title":"approve"}`))
	req.Header.Set("Authorization", "bearer test-token")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
}

func TestServerMethodNotAllowed(t *testing.T) {
	srv := newTestServer(t, nil, &fakeSender{})
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, approvalRequestPath, nil)
			req.Header.Set("Authorization", "Bearer test-token")
			rr := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want 405", rr.Code)
			}
		})
	}
}

func TestServerMalformedRequest(t *testing.T) {
	srv := newTestServer(t, nil, &fakeSender{})
	rr := postApproval(t, srv, `{`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("malformed JSON status = %d, want 400", rr.Code)
	}

	rr = postApproval(t, srv, `{"detail":"missing title"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("missing title status = %d, want 400", rr.Code)
	}
}

func TestServerRequestTooLarge(t *testing.T) {
	srv := newTestServer(t, nil, &fakeSender{})
	rr := postApproval(t, srv, `{"title":"`+strings.Repeat("x", 70*1024)+`"}`)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rr.Code)
	}
}

func TestServerTargetMissing(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TargetSessionKey = ""
	cfg.AllowedTGUserID = "123"
	srv, err := NewServer(ServerOptions{
		Config: cfg,
		Broker: NewBroker(time.Second),
		Sender: &fakeSender{},
		Token:  "test-token",
	})
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}
	rr := postApproval(t, srv, `{"title":"approve"}`)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
}

func TestServerSendFailureCancelsBrokerEntry(t *testing.T) {
	broker := NewBroker(time.Second)
	sender := &fakeSender{send: func(context.Context, ApprovalMessage) error {
		return errors.New("send failed")
	}}
	srv := newTestServer(t, broker, sender)

	rr := postApproval(t, srv, `{"title":"approve"}`)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	if broker.PendingCount() != 0 {
		t.Fatalf("PendingCount() = %d, want 0", broker.PendingCount())
	}
}

func TestServerReturnsAllowDecision(t *testing.T) {
	broker := NewBroker(time.Second)
	sender := &fakeSender{send: func(_ context.Context, msg ApprovalMessage) error {
		go broker.Resolve(msg.ID, DecisionAllow)
		return nil
	}}
	srv := newTestServer(t, broker, sender)

	rr := postApproval(t, srv, `{"title":"approve","detail":"safe detail"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	var resp ApprovalResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Decision != DecisionAllow {
		t.Fatalf("decision = %q, want allow", resp.Decision)
	}
	msg := sender.lastMessage()
	if msg.ID == "" || msg.AllowCallbackData == "" || msg.DenyCallbackData == "" {
		t.Fatalf("sender message missing id/callbacks: %+v", msg)
	}
}

func TestServerReturnsTimeoutDecision(t *testing.T) {
	broker := NewBroker(10 * time.Millisecond)
	srv := newTestServer(t, broker, &fakeSender{})

	rr := postApproval(t, srv, `{"title":"approve"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	var resp ApprovalResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Decision != DecisionTimeout {
		t.Fatalf("decision = %q, want timeout", resp.Decision)
	}
}

func TestServerMapsCanceledResultToTimeout(t *testing.T) {
	broker := NewBroker(time.Second)
	sender := &fakeSender{send: func(_ context.Context, msg ApprovalMessage) error {
		go broker.Cancel(msg.ID)
		return nil
	}}
	srv := newTestServer(t, broker, sender)

	rr := postApproval(t, srv, `{"title":"approve"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	var resp ApprovalResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Decision != DecisionTimeout {
		t.Fatalf("decision = %q, want timeout", resp.Decision)
	}
}

func TestServerSanitizesBeforeSending(t *testing.T) {
	broker := NewBroker(time.Second)
	sender := &fakeSender{send: func(_ context.Context, msg ApprovalMessage) error {
		go broker.Resolve(msg.ID, DecisionDeny)
		return nil
	}}
	srv := newTestServer(t, broker, sender)

	body := `{"title":"token=123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi","detail":"Authorization: Bearer abcdefghijklmnopqrstuvwxyz123456"}`
	rr := postApproval(t, srv, body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	msg := sender.lastMessage()
	if strings.Contains(msg.Title, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") || strings.Contains(msg.Detail, "abcdefghijklmnopqrstuvwxyz") {
		t.Fatalf("message was not sanitized: %+v", msg)
	}
}

func TestServerSanitizesConfiguredSecretsBeforeSending(t *testing.T) {
	broker := NewBroker(time.Second)
	sender := &fakeSender{send: func(_ context.Context, msg ApprovalMessage) error {
		go broker.Resolve(msg.ID, DecisionAllow)
		return nil
	}}
	cfg := DefaultConfig()
	cfg.TargetSessionKey = "telegram:123456789"
	cfg.AllowedTGUserID = "987654321"
	srv, err := NewServer(ServerOptions{
		Config: cfg,
		Broker: broker,
		Sender: sender,
		Token:  "test-token",
	})
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}

	rr := postApproval(t, srv, `{"title":"chat telegram:123456789","detail":"user 987654321"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	msg := sender.lastMessage()
	if strings.Contains(msg.Title, "123456789") || strings.Contains(msg.Detail, "987654321") {
		t.Fatalf("message leaked configured secrets: %+v", msg)
	}
}

func TestServerClientCancelRemovesBrokerEntry(t *testing.T) {
	broker := NewBroker(time.Second)
	sent := make(chan struct{})
	var once sync.Once
	sender := &fakeSender{send: func(_ context.Context, msg ApprovalMessage) error {
		once.Do(func() { close(sent) })
		return nil
	}}
	srv := newTestServer(t, broker, sender)
	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, httpSrv.URL+approvalRequestPath, strings.NewReader(`{"title":"approve"}`))
	if err != nil {
		t.Fatalf("NewRequestWithContext() error: %v", err)
	}
	req.Header.Set("Authorization", "Bearer test-token")
	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
	}()

	select {
	case <-sent:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for sender")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for canceled client")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if broker.PendingCount() == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("PendingCount() = %d, want 0", broker.PendingCount())
}

func TestServerStartPrintsSingleHandshakeLineAndBindsLocalhost(t *testing.T) {
	var handshake bytes.Buffer
	srv, err := NewServer(ServerOptions{
		Config: Config{
			ListenAddr:       DefaultListenAddr,
			TTLSeconds:       1,
			TargetSessionKey: "telegram:123",
			AllowedTGUserID:  "123",
		},
		Sender:          &fakeSender{},
		Token:           "handshake-token",
		HandshakeWriter: &handshake,
	})
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer srv.Stop(context.Background())

	lines := strings.Split(strings.TrimSuffix(handshake.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("handshake lines = %d, want 1: %q", len(lines), handshake.String())
	}
	line := lines[0]
	if !strings.HasPrefix(line, "SIDECAR_LISTEN=127.0.0.1:") || !strings.Contains(line, " SIDECAR_TOKEN=handshake-token") {
		t.Fatalf("bad handshake line: %q", line)
	}
	if !strings.HasPrefix(srv.Addr(), "127.0.0.1:") {
		t.Fatalf("Addr() = %q, want 127.0.0.1:<port>", srv.Addr())
	}
}

func newTestServer(t *testing.T, broker *Broker, sender ApprovalSender) *Server {
	t.Helper()
	if broker == nil {
		broker = NewBroker(time.Second)
	}
	cfg := DefaultConfig()
	cfg.TargetSessionKey = "telegram:123"
	cfg.AllowedTGUserID = "123"
	srv, err := NewServer(ServerOptions{
		Config: cfg,
		Broker: broker,
		Sender: sender,
		Token:  "test-token",
	})
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}
	return srv
}

func postApproval(t *testing.T, srv *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, approvalRequestPath, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	return rr
}
