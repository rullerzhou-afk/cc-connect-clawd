package clawdbridge

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	DecisionAllow    Decision = "allow"
	DecisionDeny     Decision = "deny"
	DecisionTimeout  Decision = "timeout"
	DecisionCanceled Decision = "canceled"

	callbackPrefix = "clawdperm"

	defaultRequestIDBytes = 12
	maxRequestIDHexChars  = 32
	maxCallbackDataBytes  = 64
)

type Decision string

type Result struct {
	Decision Decision
}

type Broker struct {
	mu      sync.Mutex
	ttl     time.Duration
	pending map[string]*pendingRequest
}

type pendingRequest struct {
	ch    chan Result
	timer *time.Timer
}

func NewBroker(ttl time.Duration) *Broker {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Broker{
		ttl:     ttl,
		pending: make(map[string]*pendingRequest),
	}
}

func (b *Broker) CreateRequest(title, detail string) (string, <-chan Result, error) {
	if b == nil {
		return "", nil, errors.New("clawdbridge: nil broker")
	}
	id, err := b.newRequestID()
	if err != nil {
		return "", nil, err
	}
	if _, err := CallbackData(id, DecisionAllow); err != nil {
		return "", nil, err
	}
	if _, err := CallbackData(id, DecisionDeny); err != nil {
		return "", nil, err
	}

	ch := make(chan Result, 1)
	req := &pendingRequest{ch: ch}

	b.mu.Lock()
	req.timer = time.AfterFunc(b.ttl, func() {
		b.finish(id, DecisionTimeout)
	})
	b.pending[id] = req
	b.mu.Unlock()

	return id, ch, nil
}

func (b *Broker) Resolve(id string, decision Decision) bool {
	if !isTerminalUserDecision(decision) {
		return false
	}
	return b.finish(id, decision)
}

func (b *Broker) Cancel(id string) bool {
	return b.finish(id, DecisionCanceled)
}

func (b *Broker) CancelAll() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	pending := b.pending
	b.pending = make(map[string]*pendingRequest)
	b.mu.Unlock()

	count := 0
	for _, req := range pending {
		if req.timer != nil {
			req.timer.Stop()
		}
		req.ch <- Result{Decision: DecisionCanceled}
		close(req.ch)
		count++
	}
	return count
}

func (b *Broker) PendingCount() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pending)
}

func (b *Broker) finish(id string, decision Decision) bool {
	if b == nil || id == "" {
		return false
	}

	b.mu.Lock()
	req, ok := b.pending[id]
	if ok {
		delete(b.pending, id)
	}
	b.mu.Unlock()

	if !ok {
		return false
	}
	if req.timer != nil {
		req.timer.Stop()
	}
	req.ch <- Result{Decision: decision}
	close(req.ch)
	return true
}

func (b *Broker) newRequestID() (string, error) {
	for i := 0; i < 8; i++ {
		id, err := randomHex(defaultRequestIDBytes)
		if err != nil {
			return "", err
		}
		if len(id) > maxRequestIDHexChars {
			return "", fmt.Errorf("clawdbridge: request id length %d exceeds %d", len(id), maxRequestIDHexChars)
		}
		b.mu.Lock()
		_, exists := b.pending[id]
		b.mu.Unlock()
		if !exists {
			return id, nil
		}
	}
	return "", errors.New("clawdbridge: could not allocate unique request id")
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("clawdbridge: generate random id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func CallbackData(id string, decision Decision) (string, error) {
	if id == "" {
		return "", errors.New("clawdbridge: callback id is required")
	}
	if !isTerminalUserDecision(decision) {
		return "", fmt.Errorf("clawdbridge: invalid callback decision %q", decision)
	}
	data := fmt.Sprintf("%s:%s:%s", callbackPrefix, id, decision)
	if len([]byte(data)) > maxCallbackDataBytes {
		return "", fmt.Errorf("clawdbridge: callback data length %d exceeds %d", len([]byte(data)), maxCallbackDataBytes)
	}
	return data, nil
}

func ParseCallbackData(data string) (string, Decision, bool) {
	parts := strings.Split(data, ":")
	if len(parts) != 3 || parts[0] != callbackPrefix || parts[1] == "" {
		return "", "", false
	}
	decision := Decision(parts[2])
	if !isTerminalUserDecision(decision) {
		return parts[1], decision, false
	}
	return parts[1], decision, true
}

type CallbackStatus string

const (
	CallbackResolved       CallbackStatus = "resolved"
	CallbackUnauthorized   CallbackStatus = "unauthorized"
	CallbackMalformed      CallbackStatus = "malformed"
	CallbackAlreadyHandled CallbackStatus = "already_handled"
)

type CallbackOutcome struct {
	Status   CallbackStatus
	Decision Decision
}

func (b *Broker) HandleCallback(allowedUserID, fromUserID, data string) CallbackOutcome {
	allowedUserID = strings.TrimSpace(allowedUserID)
	if allowedUserID == "" || strings.TrimSpace(fromUserID) != allowedUserID {
		return CallbackOutcome{Status: CallbackUnauthorized}
	}
	id, decision, ok := ParseCallbackData(data)
	if !ok {
		return CallbackOutcome{Status: CallbackMalformed}
	}
	if !b.Resolve(id, decision) {
		return CallbackOutcome{Status: CallbackAlreadyHandled, Decision: decision}
	}
	return CallbackOutcome{Status: CallbackResolved, Decision: decision}
}

func isTerminalUserDecision(decision Decision) bool {
	return decision == DecisionAllow || decision == DecisionDeny
}
