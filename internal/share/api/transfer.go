package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// OpenedTransfer is a short-code transfer the sender opened: the nameplate
// that goes into the code, and the sender's token for its legs.
type OpenedTransfer struct {
	Nameplate   int
	SenderToken string
	ExpiresAt   time.Time
}

// ClaimedTransfer is a transfer the receiver joined by its nameplate: its id,
// which is the session id, the receiver's token and the sender's offer.
type ClaimedTransfer struct {
	TransferID    string
	ReceiverToken string
	Offer         []byte
	ExpiresAt     time.Time
}

// TransferAnswer is the receiver's leg: its share and confirmation tag.
type TransferAnswer struct {
	Share        []byte
	Confirmation []byte
}

// OpenTransfer opens a transfer with the sender's offer. The sender picks
// the id, because its offer depends on it.
func (c *Client) OpenTransfer(ctx context.Context, transferID string, offer []byte) (*OpenedTransfer, error) {
	var body struct {
		Nameplate   int    `json:"nameplate"`
		SenderToken string `json:"sender_token"`
		ExpiresAt   string `json:"expires_at"`
	}
	payload, err := json.Marshal(map[string]string{"transfer_id": transferID, "offer": encodeValue(offer)})
	if err != nil {
		return nil, fmt.Errorf("encode transfer: %w", err)
	}
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/transfers", jsonHeaders(""), payload, &body); err != nil {
		return nil, err
	}
	expires, err := time.Parse(time.RFC3339, body.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("transfer expires_at: %w", err)
	}
	return &OpenedTransfer{Nameplate: body.Nameplate, SenderToken: body.SenderToken, ExpiresAt: expires}, nil
}

// ClaimTransfer joins the transfer under a nameplate. A nameplate can be
// claimed once: 404 means there is no such transfer, 409 that it was taken.
func (c *Client) ClaimTransfer(ctx context.Context, nameplate int) (*ClaimedTransfer, error) {
	var body struct {
		TransferID    string `json:"transfer_id"`
		ReceiverToken string `json:"receiver_token"`
		Offer         string `json:"offer"`
		ExpiresAt     string `json:"expires_at"`
	}
	payload, err := json.Marshal(map[string]int{"nameplate": nameplate})
	if err != nil {
		return nil, fmt.Errorf("encode claim: %w", err)
	}
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/transfers/claim", jsonHeaders(""), payload, &body); err != nil {
		return nil, err
	}
	offer, err := base64.RawURLEncoding.DecodeString(body.Offer)
	if err != nil {
		return nil, fmt.Errorf("transfer offer: %w", err)
	}
	expires, err := time.Parse(time.RFC3339, body.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("transfer expires_at: %w", err)
	}
	return &ClaimedTransfer{TransferID: body.TransferID, ReceiverToken: body.ReceiverToken, Offer: offer, ExpiresAt: expires}, nil
}

// PostTransferAnswer stores the receiver's answer. Repeating it is safe.
func (c *Client) PostTransferAnswer(ctx context.Context, transferID, token string, answer TransferAnswer) error {
	payload, err := json.Marshal(map[string]string{"share": encodeValue(answer.Share), "confirmation": encodeValue(answer.Confirmation)})
	if err != nil {
		return fmt.Errorf("encode answer: %w", err)
	}
	return c.doJSON(ctx, http.MethodPost, transferPath(transferID, "/answer"), jsonHeaders(token), payload, nil)
}

// AwaitTransferAnswer waits up to the server's long-poll window for the
// answer, and returns nil when the window passed without one.
func (c *Client) AwaitTransferAnswer(ctx context.Context, transferID, token string) (*TransferAnswer, error) {
	var body struct {
		Share        string `json:"share"`
		Confirmation string `json:"confirmation"`
	}
	ok, err := c.longPoll(ctx, transferPath(transferID, "/answer"), token, &body)
	if err != nil || !ok {
		return nil, err
	}
	share, err := base64.RawURLEncoding.DecodeString(body.Share)
	if err != nil {
		return nil, fmt.Errorf("answer share: %w", err)
	}
	confirmation, err := base64.RawURLEncoding.DecodeString(body.Confirmation)
	if err != nil {
		return nil, fmt.Errorf("answer confirmation: %w", err)
	}
	return &TransferAnswer{Share: share, Confirmation: confirmation}, nil
}

// PostTransferDelivery stores the sealed link, which ends the transfer.
// Repeating it is safe.
func (c *Client) PostTransferDelivery(ctx context.Context, transferID, token string, sealed []byte) error {
	payload, err := json.Marshal(map[string]string{"sealed": encodeValue(sealed)})
	if err != nil {
		return fmt.Errorf("encode delivery: %w", err)
	}
	return c.doJSON(ctx, http.MethodPost, transferPath(transferID, "/delivery"), jsonHeaders(token), payload, nil)
}

// AwaitTransferDelivery waits up to the server's long-poll window for the
// sealed link, and returns nil when the window passed without it.
func (c *Client) AwaitTransferDelivery(ctx context.Context, transferID, token string) ([]byte, error) {
	var body struct {
		Sealed string `json:"sealed"`
	}
	ok, err := c.longPoll(ctx, transferPath(transferID, "/delivery"), token, &body)
	if err != nil || !ok {
		return nil, err
	}
	sealed, err := base64.RawURLEncoding.DecodeString(body.Sealed)
	if err != nil {
		return nil, fmt.Errorf("sealed link: %w", err)
	}
	return sealed, nil
}

// CloseTransfer ends a transfer early, with the reason cancelled or mismatch.
func (c *Client) CloseTransfer(ctx context.Context, transferID, token, reason string) error {
	path := transferPath(transferID, "") + "?reason=" + url.QueryEscape(reason)
	return c.doJSON(ctx, http.MethodDelete, path, jsonHeaders(token), nil, nil)
}

// longPoll is one GET that the server holds until the leg exists or its
// window passes; 204 is the window passing, and ok is false then.
func (c *Client) longPoll(ctx context.Context, path, token string, out any) (bool, error) {
	resp, err := c.do(ctx, http.MethodGet, path, jsonHeaders(token), nil, 0)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, resp.Body)
		return false, nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return false, &Error{Status: resp.StatusCode, Message: "unreadable answer: " + err.Error(), RequestID: resp.Header.Get(headerRequestID)}
	}
	return true, nil
}

func transferPath(transferID, leg string) string {
	return "/api/v1/transfers/" + url.PathEscape(transferID) + leg
}

func jsonHeaders(token string) http.Header {
	h := http.Header{"Content-Type": {"application/json"}}
	if token != "" {
		h.Set("Authorization", "Bearer "+token)
	}
	return h
}

func encodeValue(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}
