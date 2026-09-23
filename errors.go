package wechat

import (
	"errors"
	"fmt"
)

var (
	// ErrCredentialsRequired is returned when BotToken or AccountID is missing.
	ErrCredentialsRequired = errors.New("wechat: botToken and accountID (ILinkBotID) are required")

	// ErrSessionExpired is returned when the session token rotated on the server.
	ErrSessionExpired = errors.New("wechat: session expired")

	// ErrTokenRevoked means the configured recovery attempts were exhausted.
	// The server has not necessarily confirmed permanent revocation.
	ErrTokenRevoked = errors.New("wechat: session recovery exhausted; reauthentication may be required")

	// ErrEmptyQRCode is returned when iLink returns an empty qrcode token.
	ErrEmptyQRCode = errors.New("wechat: ilink returned empty qrcode")

	// ErrNoMediaInfo is returned when media payload is missing required decryption info.
	ErrNoMediaInfo = errors.New("wechat: no media info available")

	// ErrQRConfirmedNoCreds is returned when QR was confirmed but response had no credentials.
	ErrQRConfirmedNoCreds = errors.New("wechat: qr confirmed without credentials")

	// ErrInvalidBlockSize is returned when ciphertext length is not a multiple of AES block size.
	ErrInvalidBlockSize = errors.New("wechat: ciphertext not aligned to block size")

	// ErrAlreadyStarted means Start is already running for this client.
	ErrAlreadyStarted = errors.New("wechat: listener already started")
	// ErrNoMessageHandler means Start has no registered message processor.
	ErrNoMessageHandler = errors.New("wechat: register a message handler before starting")
	// ErrHandlerPanic means a callback panicked; the current batch was not committed.
	ErrHandlerPanic = errors.New("wechat: callback panicked")
	// ErrResponseTooLarge means a response exceeded the configured byte limit.
	ErrResponseTooLarge = errors.New("wechat: response exceeds byte limit")
	// ErrMediaTooLarge means media exceeded the configured plaintext size limit.
	ErrMediaTooLarge = errors.New("wechat: media exceeds byte limit")
	// ErrQRExpired means an unconfirmed QR code expired.
	ErrQRExpired = errors.New("wechat: QR code expired; request a new code")
	// ErrVerificationRequired means a verification-code callback must be supplied.
	ErrVerificationRequired = errors.New("wechat: QR login requires a verification code")
	// ErrVerificationBlocked means the server blocked further verification attempts.
	ErrVerificationBlocked = errors.New("wechat: QR verification blocked; request a new code")
	// ErrVerificationAttemptsExhausted means this client stopped after its own
	// attempt limit. Unlike ErrVerificationBlocked, the server has not rejected
	// the account, so a fresh QR code may still succeed.
	ErrVerificationAttemptsExhausted = errors.New("wechat: verification code attempts exhausted")
	// ErrBlockedURL means a server-supplied URL, or a redirect it issued, was
	// refused by the client's URL policy.
	ErrBlockedURL = errors.New("wechat: server-supplied URL refused by policy")
	// ErrEmptyMessage means there was nothing to send. Empty files are allowed
	// through SendFile; text, image, and video sends report this instead of
	// silently succeeding without delivering anything.
	ErrEmptyMessage = errors.New("wechat: refusing to send empty content")
	// ErrQRAlreadyBound means the server reports an existing binding.
	ErrQRAlreadyBound = errors.New("wechat: bot already bound; use its saved credentials or log in again")
)

// APIError represents an error returned by the iLink server.
type APIError struct {
	Endpoint string
	Ret      int
	ErrCode  int
	ErrMsg   string
}

// Error returns status fields without echoing potentially sensitive server text.
// Callers may inspect ErrMsg explicitly when diagnosing a failure.
func (e *APIError) Error() string {
	if e.ErrCode != 0 {
		return fmt.Sprintf("wechat api %s error: ret=%d errcode=%d", e.Endpoint, e.Ret, e.ErrCode)
	}
	return fmt.Sprintf("wechat api %s error: ret=%d", e.Endpoint, e.Ret)
}

// HTTPError is an unsuccessful HTTP status. Response bodies and URL tokens are omitted.
type HTTPError struct {
	Endpoint   string
	StatusCode int
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("wechat %s: HTTP %d", e.Endpoint, e.StatusCode)
}

// HandlerPanicError reports a recovered callback panic without exposing its value.
// Stack is available for explicit diagnostics; Error does not include callback data.
type HandlerPanicError struct{ Stack []byte }

func (e *HandlerPanicError) Error() string { return ErrHandlerPanic.Error() }

// Unwrap allows errors.Is(err, ErrHandlerPanic).
func (e *HandlerPanicError) Unwrap() error { return ErrHandlerPanic }
