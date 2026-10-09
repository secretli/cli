// Package sharetest is a fake Secretli server for tests: the slice of the
// API the client talks to, with the rules the real handlers enforce on
// parts, tokens and one-time secrets, tombstones for what is gone that last
// only until the secret would have expired, and the short-code transfer relay.
package sharetest

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const minPartBytes = 5 * 1024 * 1024

var expirations = []string{"5m", "10m", "15m", "1h", "4h", "12h", "1d", "3d", "7d"}

// Server is the fake, listening on a loopback port.
type Server struct {
	*httptest.Server

	mu       sync.Mutex
	partSize int64
	uploads  map[string]*upload
	secrets  map[string]*secret
	gone     map[string]*tombstone
	sessions map[string]string // session token -> public id
	relay    *relay
}

type uploadRequest struct {
	PublicID      string `json:"public_id"`
	MetadataToken string `json:"metadata_token"`
	BlobToken     string `json:"blob_token"`
	DeletionToken string `json:"deletion_token"`
	EncryptedMeta string `json:"encrypted_meta"`
	Expiration    string `json:"expiration"`
	BurnAfterRead bool   `json:"burn_after_read"`
	BlobSize      int64  `json:"blob_size"`
}

type upload struct {
	token   string
	req     uploadRequest
	parts   map[int][]byte
	offsets map[int]int64
}

type secret struct {
	req      uploadRequest
	blob     []byte
	expires  time.Time
	created  time.Time
	consumed bool
	opened   bool
}

// tombstone is what the server remembers of a secret that is gone, for whoever
// holds its metadata token, until the secret's own expiry.
type tombstone struct {
	metadataToken string
	outcome       string
	burnAfterRead bool
	expires       time.Time
}

// New starts a fake server whose upload sessions hand out this part size.
func New(partSize int64) *Server {
	s := &Server{
		partSize: partSize,
		uploads:  map[string]*upload{},
		secrets:  map[string]*secret{},
		gone:     map[string]*tombstone{},
		sessions: map[string]string{},
		relay:    newRelay(),
	}
	mux := http.NewServeMux()
	s.relay.register(mux)
	mux.HandleFunc("POST /api/v1/secrets/uploads", s.startUpload)
	mux.HandleFunc("PUT /api/v1/secrets/uploads/{sid}/parts/{n}", s.uploadPart)
	mux.HandleFunc("POST /api/v1/secrets/uploads/{sid}/complete", s.completeUpload)
	mux.HandleFunc("DELETE /api/v1/secrets/uploads/{sid}", s.abortUpload)
	mux.HandleFunc("GET /api/v1/secrets/{id}/meta", s.metadata)
	mux.HandleFunc("POST /api/v1/secrets/{id}/retrieval-session", s.startSession)
	mux.HandleFunc("GET /api/v1/secrets/{id}/blob", s.blob)
	mux.HandleFunc("DELETE /api/v1/secrets/{id}", s.deleteSecret)
	s.Server = httptest.NewServer(mux)
	return s
}

// PartsUploaded is how many parts the most recent upload session received.
func (s *Server) PartsUploaded() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, up := range s.uploads {
		n = len(up.parts)
	}
	return n
}

// PartSizes is the size of each part of the most recent upload session, in
// part order.
func (s *Server) PartSizes() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var sizes []int64
	for _, up := range s.uploads {
		sizes = make([]int64, len(up.parts))
		for n, data := range up.parts {
			if n >= 1 && n <= len(sizes) {
				sizes[n-1] = int64(len(data))
			}
		}
	}
	return sizes
}

// Secrets is how many live secrets the server holds.
func (s *Server) Secrets() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.secrets)
}

// Expire lets every secret reach its expiry, and with it the note of what
// became of the ones that are gone: from here on they are all unknown.
func (s *Server) Expire() {
	s.mu.Lock()
	defer s.mu.Unlock()
	past := time.Now().Add(-time.Minute)
	for _, sec := range s.secrets {
		sec.expires = past
	}
	for _, tomb := range s.gone {
		tomb.expires = past
	}
}

// live is the secret behind a public id, unless it is gone or its time is up.
func (s *Server) live(id string) (*secret, bool) {
	sec, ok := s.secrets[id]
	if !ok || time.Now().After(sec.expires) || (sec.req.BurnAfterRead && sec.consumed) {
		return nil, false
	}
	return sec, true
}

// notLive answers for a secret that is not live: 410 with what became of it
// to whoever holds its metadata token, until it would have expired, and 404
// for everything else, an expired secret and an unknown one alike.
func (s *Server) notLive(w http.ResponseWriter, id, metadataToken string) {
	if tomb, ok := s.gone[id]; ok && time.Now().Before(tomb.expires) && tomb.metadataToken == metadataToken {
		writeError(w, 410, "secret is gone", map[string]any{"outcome": tomb.outcome, "burn_after_read": tomb.burnAfterRead})
		return
	}
	writeError(w, 404, "secret not found", nil)
}

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string, details map[string]any) {
	body := map[string]any{"error": msg}
	if details != nil {
		body["details"] = details
	}
	writeJSON(w, status, body)
}

func (s *Server) startUpload(w http.ResponseWriter, r *http.Request) {
	var req uploadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.BlobSize <= 0 {
		writeError(w, 400, "invalid request body", nil)
		return
	}
	if !slices.Contains(expirations, req.Expiration) {
		writeError(w, 400, "invalid expiration", nil)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, token := randomToken(), randomToken()
	s.uploads[id] = &upload{token: token, req: req, parts: map[int][]byte{}, offsets: map[int]int64{}}
	writeJSON(w, 201, map[string]any{
		"session_id": id, "upload_token": token, "public_id": req.PublicID, "part_size": s.partSize,
		"blob_size": req.BlobSize, "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	})
}

func (s *Server) authUpload(w http.ResponseWriter, r *http.Request) *upload {
	s.mu.Lock()
	defer s.mu.Unlock()
	up, ok := s.uploads[r.PathValue("sid")]
	if !ok || r.Header.Get("Authorization") != "Bearer "+up.token {
		writeError(w, 403, "invalid upload token", nil)
		return nil
	}
	return up
}

func (s *Server) uploadPart(w http.ResponseWriter, r *http.Request) {
	up := s.authUpload(w, r)
	if up == nil {
		return
	}
	n, _ := strconv.Atoi(r.PathValue("n"))
	offset, _ := strconv.ParseInt(r.Header.Get("X-Part-Offset"), 10, 64)
	size, _ := strconv.ParseInt(r.Header.Get("X-Part-Size"), 10, 64)
	data, _ := io.ReadAll(r.Body)
	if n <= 0 || int64(len(data)) != size || size > s.partSize+1024*1024 || r.Header.Get("X-Part-SHA256") != sha256Hex(data) {
		writeError(w, 400, "bad part", nil)
		return
	}
	if offset+size > up.req.BlobSize || (offset+size < up.req.BlobSize && size < minPartBytes) {
		writeError(w, 400, "part placement", nil)
		return
	}
	s.mu.Lock()
	up.parts[n] = data
	up.offsets[n] = offset
	s.mu.Unlock()
	writeJSON(w, 200, map[string]any{"part_number": n, "offset": offset, "size": size})
}

func (s *Server) completeUpload(w http.ResponseWriter, r *http.Request) {
	up := s.authUpload(w, r)
	if up == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var blob []byte
	for n := 1; ; n++ {
		part, ok := up.parts[n]
		if !ok {
			break
		}
		if up.offsets[n] != int64(len(blob)) {
			writeError(w, 400, fmt.Sprintf("upload part %d has invalid offset", n), nil)
			return
		}
		blob = append(blob, part...)
	}
	if int64(len(blob)) != up.req.BlobSize {
		writeError(w, 400, "upload is missing parts", nil)
		return
	}
	now := time.Now()
	s.secrets[up.req.PublicID] = &secret{req: up.req, blob: blob, expires: now.Add(24 * time.Hour), created: now}
	writeJSON(w, 201, map[string]string{"expires_at": now.Add(24 * time.Hour).UTC().Format(time.RFC3339)})
}

func (s *Server) abortUpload(w http.ResponseWriter, r *http.Request) {
	if up := s.authUpload(w, r); up != nil {
		s.mu.Lock()
		delete(s.uploads, r.PathValue("sid"))
		s.mu.Unlock()
		w.WriteHeader(204)
	}
}

func (s *Server) metadata(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	sec, ok := s.live(id)
	if !ok {
		s.notLive(w, id, r.Header.Get("X-Metadata-Token"))
		return
	}
	if r.Header.Get("X-Metadata-Token") != sec.req.MetadataToken {
		writeError(w, 403, "invalid token", nil)
		return
	}
	writeJSON(w, 200, map[string]any{
		"encrypted_meta": sec.req.EncryptedMeta, "blob_size": len(sec.blob), "burn_after_read": sec.req.BurnAfterRead,
		"expires_at": sec.expires.UTC().Format(time.RFC3339), "created_at": sec.created.UTC().Format(time.RFC3339),
		"opened": sec.opened,
	})
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	sec, ok := s.live(id)
	if !ok {
		writeError(w, 404, "secret not found", nil)
		return
	}
	if r.Header.Get("X-Blob-Token") != sec.req.BlobToken {
		writeError(w, 403, "invalid blob token", nil)
		return
	}
	// The owner's own look at a reusable secret is not an opening. A one-time
	// secret is opened by whoever comes first, the owner included.
	byOwner := r.Header.Get("X-Deletion-Token") != "" && r.Header.Get("X-Deletion-Token") == sec.req.DeletionToken
	now := time.Now()
	if sec.req.BurnAfterRead {
		sec.consumed = true
		s.gone[id] = &tombstone{metadataToken: sec.req.MetadataToken, outcome: "opened", burnAfterRead: true, expires: sec.expires}
	} else if !byOwner {
		sec.opened = true
	}
	token := randomToken()
	s.sessions[token] = id
	writeJSON(w, 201, map[string]any{
		"session_token": token, "blob_size": len(sec.blob), "expires_at": now.Add(15 * time.Minute).UTC().Format(time.RFC3339), "burn_after_read": sec.req.BurnAfterRead,
	})
}

func (s *Server) blob(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	sec, ok := s.secrets[id]
	if !ok || s.sessions[token] != id {
		writeError(w, 403, "invalid retrieval session", nil)
		return
	}
	var start, end int64
	if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil || start < 0 || end >= int64(len(sec.blob)) || end < start {
		w.WriteHeader(416)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(sec.blob)))
	w.WriteHeader(206)
	_, _ = w.Write(sec.blob[start : end+1])
}

func (s *Server) deleteSecret(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	sec, ok := s.live(id)
	if !ok {
		s.notLive(w, id, r.Header.Get("X-Metadata-Token"))
		return
	}
	if r.Header.Get("X-Metadata-Token") != sec.req.MetadataToken || r.Header.Get("X-Deletion-Token") != sec.req.DeletionToken {
		writeError(w, 403, "invalid token", nil)
		return
	}
	delete(s.secrets, id)
	s.gone[id] = &tombstone{metadataToken: sec.req.MetadataToken, outcome: "deleted", burnAfterRead: sec.req.BurnAfterRead, expires: sec.expires}
	w.WriteHeader(204)
}
