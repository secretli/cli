package sharetest

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// pollWindow is how long a leg's GET waits before answering 204; the real
// server waits 25 s.
const pollWindow = 200 * time.Millisecond

const (
	shareBytes        = 32
	confirmationBytes = 32
	sealedBytes       = 552
	maxNameplate      = 999
)

// relay is the short-code transfer relay, with the server's rules: the
// sender picks the id and gets the lowest free nameplate, a nameplate is
// claimed once, each leg is written once (an identical retry succeeds), a
// delivery ends the transfer as done, a written leg is handed out even after
// the transfer closed, and a closed transfer answers 410 with its reason.
type relay struct {
	mu        sync.Mutex
	changed   chan struct{}
	transfers map[string]*transferState
}

type transferState struct {
	id            string
	nameplate     int
	senderToken   string
	receiverToken string
	offer         []byte
	answer        *answerLeg
	delivery      []byte
	closed        string
	expires       time.Time
}

type answerLeg struct {
	share, confirmation []byte
}

// TransferState is what a test can check about a transfer.
type TransferState struct {
	Nameplate int
	Claimed   bool
	Answered  bool
	Delivered bool
	// Closed is "", done, cancelled or mismatch.
	Closed string
}

func newRelay() *relay {
	return &relay{changed: make(chan struct{}), transfers: map[string]*transferState{}}
}

func (rl *relay) register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/transfers", rl.open)
	mux.HandleFunc("POST /api/v1/transfers/claim", rl.claim)
	mux.HandleFunc("POST /api/v1/transfers/{id}/answer", rl.postAnswer)
	mux.HandleFunc("GET /api/v1/transfers/{id}/answer", rl.awaitAnswer)
	mux.HandleFunc("POST /api/v1/transfers/{id}/delivery", rl.postDelivery)
	mux.HandleFunc("GET /api/v1/transfers/{id}/delivery", rl.awaitDelivery)
	mux.HandleFunc("DELETE /api/v1/transfers/{id}", rl.close)
}

// Transfers lists the transfers the relay has seen, oldest nameplate first.
func (s *Server) Transfers() []TransferState {
	s.relay.mu.Lock()
	defer s.relay.mu.Unlock()
	out := make([]TransferState, 0, len(s.relay.transfers))
	for n := 1; n <= maxNameplate; n++ {
		for _, t := range s.relay.transfers {
			if t.nameplate == n {
				out = append(out, TransferState{
					Nameplate: n, Claimed: t.receiverToken != "", Answered: t.answer != nil,
					Delivered: t.delivery != nil, Closed: t.closed,
				})
			}
		}
	}
	return out
}

func (rl *relay) notifyLocked() {
	close(rl.changed)
	rl.changed = make(chan struct{})
}

func decodeLeg(s string, size int) ([]byte, bool) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	return b, err == nil && len(b) == size
}

func (rl *relay) open(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TransferID string `json:"transfer_id"`
		Offer      string `json:"offer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", nil)
		return
	}
	offer, ok := decodeLeg(req.Offer, shareBytes)
	if _, idOK := decodeLeg(req.TransferID, 32); !idOK || !ok {
		writeError(w, http.StatusBadRequest, "transfer_id and offer must be 32 bytes of unpadded base64url", nil)
		return
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if _, exists := rl.transfers[req.TransferID]; exists {
		writeError(w, http.StatusConflict, "transfer_id already used", nil)
		return
	}
	used := map[int]bool{}
	for _, t := range rl.transfers {
		if t.closed == "" {
			used[t.nameplate] = true
		}
	}
	nameplate := 1
	for used[nameplate] {
		nameplate++
	}
	if nameplate > maxNameplate {
		writeError(w, http.StatusServiceUnavailable, "too many active transfers, try again shortly", nil)
		return
	}
	t := &transferState{id: req.TransferID, nameplate: nameplate, senderToken: randomToken(), offer: offer, expires: time.Now().Add(10 * time.Minute)}
	rl.transfers[t.id] = t
	writeJSON(w, http.StatusCreated, map[string]any{
		"nameplate": t.nameplate, "sender_token": t.senderToken, "expires_at": t.expires.UTC().Format(time.RFC3339),
	})
}

func (rl *relay) claim(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Nameplate int `json:"nameplate"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Nameplate < 1 || req.Nameplate > maxNameplate {
		writeError(w, http.StatusBadRequest, "invalid nameplate", nil)
		return
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	for _, t := range rl.transfers {
		if t.nameplate != req.Nameplate || t.closed != "" {
			continue
		}
		if t.receiverToken != "" {
			writeError(w, http.StatusConflict, "transfer already claimed", nil)
			return
		}
		t.receiverToken = randomToken()
		writeJSON(w, http.StatusOK, map[string]any{
			"transfer_id": t.id, "receiver_token": t.receiverToken,
			"offer": base64.RawURLEncoding.EncodeToString(t.offer), "expires_at": t.expires.UTC().Format(time.RFC3339),
		})
		return
	}
	writeError(w, http.StatusNotFound, "no active transfer with this nameplate", nil)
}

// auth finds the transfer and checks the bearer token for one side
// ("sender" or "receiver"), or for either with side "". It holds the lock
// when it returns a transfer.
func (rl *relay) auth(w http.ResponseWriter, r *http.Request, side string) *transferState {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	rl.mu.Lock()
	t := rl.transfers[r.PathValue("id")]
	if t == nil {
		rl.mu.Unlock()
		writeError(w, http.StatusNotFound, "transfer not found", nil)
		return nil
	}
	isSender := token != "" && token == t.senderToken
	isReceiver := token != "" && token == t.receiverToken
	if (side == "sender" && !isSender) || (side == "receiver" && !isReceiver) || (side == "" && !isSender && !isReceiver) {
		rl.mu.Unlock()
		writeError(w, http.StatusForbidden, "invalid transfer token", nil)
		return nil
	}
	return t
}

func writeGone(w http.ResponseWriter, reason string) {
	writeError(w, http.StatusGone, "transfer has ended", map[string]any{"reason": reason})
}

func (rl *relay) postAnswer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Share        string `json:"share"`
		Confirmation string `json:"confirmation"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", nil)
		return
	}
	share, shareOK := decodeLeg(req.Share, shareBytes)
	confirmation, confirmationOK := decodeLeg(req.Confirmation, confirmationBytes)
	if !shareOK || !confirmationOK {
		writeError(w, http.StatusBadRequest, "share and confirmation must be 32 bytes of unpadded base64url", nil)
		return
	}
	t := rl.auth(w, r, "receiver")
	if t == nil {
		return
	}
	defer rl.mu.Unlock()
	switch {
	case t.answer != nil && bytes.Equal(t.answer.share, share) && bytes.Equal(t.answer.confirmation, confirmation):
	case t.answer != nil:
		writeError(w, http.StatusConflict, "answer already posted", nil)
		return
	case t.closed != "":
		writeGone(w, t.closed)
		return
	default:
		t.answer = &answerLeg{share: share, confirmation: confirmation}
		rl.notifyLocked()
	}
	w.WriteHeader(http.StatusNoContent)
}

func (rl *relay) postDelivery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Sealed string `json:"sealed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", nil)
		return
	}
	sealed, ok := decodeLeg(req.Sealed, sealedBytes)
	if !ok {
		writeError(w, http.StatusBadRequest, "sealed must be 552 bytes of unpadded base64url", nil)
		return
	}
	t := rl.auth(w, r, "sender")
	if t == nil {
		return
	}
	defer rl.mu.Unlock()
	switch {
	case t.delivery != nil && bytes.Equal(t.delivery, sealed):
	case t.delivery != nil:
		writeError(w, http.StatusConflict, "delivery already posted", nil)
		return
	case t.closed != "":
		writeGone(w, t.closed)
		return
	case t.answer == nil:
		writeError(w, http.StatusConflict, "no answer yet", nil)
		return
	default:
		t.delivery, t.closed = sealed, "done"
		rl.notifyLocked()
	}
	w.WriteHeader(http.StatusNoContent)
}

func (rl *relay) awaitAnswer(w http.ResponseWriter, r *http.Request) {
	rl.await(w, r, "sender", func(t *transferState) (any, bool) {
		if t.answer == nil {
			return nil, false
		}
		return map[string]string{
			"share":        base64.RawURLEncoding.EncodeToString(t.answer.share),
			"confirmation": base64.RawURLEncoding.EncodeToString(t.answer.confirmation),
		}, true
	})
}

func (rl *relay) awaitDelivery(w http.ResponseWriter, r *http.Request) {
	rl.await(w, r, "receiver", func(t *transferState) (any, bool) {
		if t.delivery == nil {
			return nil, false
		}
		return map[string]string{"sealed": base64.RawURLEncoding.EncodeToString(t.delivery)}, true
	})
}

// await holds the GET until the leg exists, the transfer closes, or the
// window passes.
func (rl *relay) await(w http.ResponseWriter, r *http.Request, side string, leg func(*transferState) (any, bool)) {
	t := rl.auth(w, r, side)
	if t == nil {
		return
	}
	rl.mu.Unlock()
	deadline := time.After(pollWindow)
	for {
		rl.mu.Lock()
		body, ok := leg(t)
		closed, changed := t.closed, rl.changed
		rl.mu.Unlock()
		switch {
		case ok:
			writeJSON(w, http.StatusOK, body)
			return
		case closed != "":
			writeGone(w, closed)
			return
		}
		select {
		case <-changed:
		case <-deadline:
			w.WriteHeader(http.StatusNoContent)
			return
		case <-r.Context().Done():
			return
		}
	}
}

func (rl *relay) close(w http.ResponseWriter, r *http.Request) {
	reason := r.URL.Query().Get("reason")
	if reason == "" {
		reason = "cancelled"
	}
	if reason != "cancelled" && reason != "mismatch" {
		writeError(w, http.StatusBadRequest, "invalid reason", nil)
		return
	}
	t := rl.auth(w, r, "")
	if t == nil {
		return
	}
	defer rl.mu.Unlock()
	if t.closed == "" {
		t.closed = reason
		rl.notifyLocked()
	}
	w.WriteHeader(http.StatusNoContent)
}
