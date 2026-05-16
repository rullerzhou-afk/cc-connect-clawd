package clawdbridge

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const approvalRequestPath = "/approval/request"

type ApprovalSender interface {
	SendApproval(ctx context.Context, msg ApprovalMessage) error
}

type ApprovalMessage struct {
	ID                string
	Title             string
	Detail            string
	AllowCallbackData string
	DenyCallbackData  string
}

type ApprovalRequest struct {
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
}

type ApprovalResponse struct {
	Decision Decision `json:"decision"`
}

type Server struct {
	cfg              Config
	broker           *Broker
	sender           ApprovalSender
	token            string
	handshakeWriter  io.Writer
	redactionSecrets []string

	httpServer *http.Server
	listener   net.Listener
}

type ServerOptions struct {
	Config          Config
	Broker          *Broker
	Sender          ApprovalSender
	Token           string
	HandshakeWriter io.Writer
}

func NewServer(opts ServerOptions) (*Server, error) {
	cfg := opts.Config
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = DefaultListenAddr
	}
	if cfg.TTLSeconds == 0 {
		cfg.TTLSeconds = DefaultTTLSeconds
	}
	if err := cfg.NormalizeAndValidate(); err != nil {
		return nil, err
	}

	broker := opts.Broker
	if broker == nil {
		broker = NewBroker(cfg.TTL())
	}

	token := strings.TrimSpace(opts.Token)
	if token == "" {
		generated, err := GenerateBearerToken()
		if err != nil {
			return nil, err
		}
		token = generated
	}

	return &Server{
		cfg:              cfg,
		broker:           broker,
		sender:           opts.Sender,
		token:            token,
		handshakeWriter:  opts.HandshakeWriter,
		redactionSecrets: cfg.RedactionSecrets(),
	}, nil
}

func GenerateBearerToken() (string, error) {
	return randomHex(32)
}

func (s *Server) Start() error {
	if s == nil {
		return errors.New("clawdbridge: nil server")
	}
	if s.httpServer != nil {
		return errors.New("clawdbridge: server already started")
	}
	ln, err := net.Listen("tcp", s.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("clawdbridge: listen: %w", err)
	}
	host, _, err := net.SplitHostPort(ln.Addr().String())
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).Equal(net.ParseIP("127.0.0.1")) {
		_ = ln.Close()
		return fmt.Errorf("clawdbridge: listener did not bind 127.0.0.1")
	}

	s.httpServer = &http.Server{
		Handler:           s.buildMux(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	s.listener = ln

	go func() {
		err := s.httpServer.Serve(ln)
		if err != nil && err != http.ErrServerClosed {
			// Phase 1 keeps runtime logging outside this package so secrets do not
			// accidentally leak through default loggers.
		}
	}()

	if s.handshakeWriter != nil {
		_, err = fmt.Fprintf(s.handshakeWriter, "SIDECAR_LISTEN=%s SIDECAR_TOKEN=%s\n", s.Addr(), s.token)
		if err != nil {
			_ = s.Stop(context.Background())
			return fmt.Errorf("clawdbridge: write handshake: %w", err)
		}
	}
	return nil
}

func (s *Server) Stop(ctx context.Context) error {
	if s == nil || s.httpServer == nil {
		return nil
	}
	s.broker.CancelAll()
	if ctx == nil {
		ctx = context.Background()
	}
	return s.httpServer.Shutdown(ctx)
}

func (s *Server) Addr() string {
	if s == nil || s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

func (s *Server) TokenForTest() string {
	if s == nil {
		return ""
	}
	return s.token
}

func (s *Server) Handler() http.Handler {
	return s.buildMux()
}

// buildMux is the single route factory for both production and tests. Add future
// middleware here, not only in Start, so Handler-based tests cover the same path.
func (s *Server) buildMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(approvalRequestPath, s.handleApprovalRequest)
	return mux
}

func (s *Server) handleApprovalRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if !s.authenticate(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req ApprovalRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" {
		http.Error(w, "title is required", http.StatusBadRequest)
		return
	}
	if !s.cfg.TargetConfigured() || s.sender == nil {
		http.Error(w, "telegram target is not configured", http.StatusServiceUnavailable)
		return
	}

	title, detail := SanitizeApprovalTextWithSecrets(req.Title, req.Detail, s.redactionSecrets)
	id, resultCh, err := s.broker.CreateRequest(title, detail)
	if err != nil {
		http.Error(w, "create request failed", http.StatusInternalServerError)
		return
	}
	allowData, _ := CallbackData(id, DecisionAllow)
	denyData, _ := CallbackData(id, DecisionDeny)

	msg := ApprovalMessage{
		ID:                id,
		Title:             title,
		Detail:            detail,
		AllowCallbackData: allowData,
		DenyCallbackData:  denyData,
	}
	if err := s.sender.SendApproval(r.Context(), msg); err != nil {
		s.broker.Cancel(id)
		http.Error(w, "telegram send failed", http.StatusInternalServerError)
		return
	}

	select {
	case result := <-resultCh:
		decision := result.Decision
		if decision == DecisionCanceled {
			decision = DecisionTimeout
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ApprovalResponse{Decision: decision})
	case <-r.Context().Done():
		s.broker.Cancel(id)
		return
	}
}

func (s *Server) authenticate(r *http.Request) bool {
	if s == nil || s.token == "" {
		return false
	}
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(strings.ToLower(auth), strings.ToLower(prefix)) {
		return false
	}
	got := strings.TrimSpace(auth[len(prefix):])
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}
