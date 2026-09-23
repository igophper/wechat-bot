package wechat

import (
	"bytes"
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Version identifies this SDK's protocol client version. It is sent as
// channel_version and, encoded, as the iLink-App-ClientVersion header.
const Version = "0.1.0"

// clientVersionHeader encodes Version as 0x00MMNNPP in decimal, matching the
// reference plugin's iLink-App-ClientVersion header. Deriving it here keeps it
// from drifting when Version changes.
var clientVersionHeader = encodeClientVersion(Version)

// encodeClientVersion returns "0" for a version it cannot encode; the package
// test pins the value for the current Version so a bad constant is caught.
func encodeClientVersion(version string) string {
	fields := strings.Split(version, ".")
	if len(fields) != 3 {
		return "0"
	}
	var encoded uint64
	for _, field := range fields {
		part, err := strconv.ParseUint(field, 10, 8)
		if err != nil {
			return "0"
		}
		encoded = encoded<<8 | part
	}
	return strconv.FormatUint(encoded, 10)
}

// Client is a WeChat iLink client. Sending and callback registration are safe for
// concurrent use. Only one Start call may run at a time. Registered handlers are
// snapshotted at Start; replacements take effect the next time Start is called.
type Client struct {
	cfg       Config
	wechatUIN string

	ctxTokensMu sync.Mutex
	ctxTokens   map[string]*list.Element
	ctxTokenLRU *list.List

	// mediaPolicy vets server-supplied CDN URLs; mediaClient additionally
	// re-applies it to every redirect hop.
	mediaPolicy urlPolicy
	mediaClient *http.Client

	// jitter spreads out poll retries. Tests replace it to pin exact delays.
	jitter func(time.Duration) time.Duration

	handlersMu sync.RWMutex
	running    atomic.Bool
	onMessage  MessageHandler
	onExpired  ExpiredHandler
}

// NewClient initializes a new WeChat bot client with credentials and options.
func NewClient(cfg Config, opts ...Option) (*Client, error) {
	for _, opt := range opts {
		opt(&cfg)
	}
	cfg.setDefaults()

	if cfg.BotToken == "" || cfg.ILinkBotID == "" {
		return nil, ErrCredentialsRequired
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	policy := newURLPolicy(cfg.CDNBaseURL, cfg.MediaHosts)
	c := &Client{
		cfg:         cfg,
		wechatUIN:   GenerateUIN(),
		ctxTokens:   make(map[string]*list.Element),
		ctxTokenLRU: list.New(),
		mediaPolicy: policy,
		mediaClient: policy.redirectGuardedClient(cfg.HTTPClient),
		jitter:      equalJitter,
	}
	return c, nil
}

// BotID returns the bot account ID (e.g. "xxx@im.bot").
func (c *Client) BotID() string {
	return c.cfg.ILinkBotID
}

// BaseURL returns the configured base URL.
func (c *Client) BaseURL() string {
	return c.cfg.BaseURL
}

// Logger returns the configured logger.
func (c *Client) Logger() *slog.Logger {
	return c.cfg.Logger
}

type contextTokenEntry struct{ userID, token string }

// SetContextToken restores a conversation token, for example from application
// storage. Empty tokens remove entries. Tokens are sensitive; persist them securely.
// The least recently used entry is evicted when MaxContextTokens is exceeded.
func (c *Client) SetContextToken(chatID, token string) {
	if token == "" {
		c.ForgetContextToken(chatID)
		return
	}
	if chatID == "" || token == "" {
		return
	}
	c.ctxTokensMu.Lock()
	defer c.ctxTokensMu.Unlock()
	if entry := c.ctxTokens[chatID]; entry != nil {
		entry.Value = contextTokenEntry{chatID, token}
		c.ctxTokenLRU.MoveToFront(entry)
		return
	}
	c.ctxTokens[chatID] = c.ctxTokenLRU.PushFront(contextTokenEntry{chatID, token})
	if c.ctxTokenLRU.Len() > c.cfg.MaxContextTokens {
		oldest := c.ctxTokenLRU.Back()
		delete(c.ctxTokens, oldest.Value.(contextTokenEntry).userID)
		c.ctxTokenLRU.Remove(oldest)
	}
}

// ContextToken returns the cached conversation token, or an empty string if absent.
// A cache miss does not establish that the server permits sending without a token.
func (c *Client) ContextToken(chatID string) string {
	c.ctxTokensMu.Lock()
	defer c.ctxTokensMu.Unlock()
	if entry := c.ctxTokens[chatID]; entry != nil {
		c.ctxTokenLRU.MoveToFront(entry)
		return entry.Value.(contextTokenEntry).token
	}
	return ""
}

// ForgetContextToken removes a sensitive conversation token from the cache.
func (c *Client) ForgetContextToken(chatID string) {
	c.ctxTokensMu.Lock()
	defer c.ctxTokensMu.Unlock()
	if entry := c.ctxTokens[chatID]; entry != nil {
		delete(c.ctxTokens, chatID)
		c.ctxTokenLRU.Remove(entry)
	}
}

func (c *Client) saveContextToken(userID, token string) { c.SetContextToken(userID, token) }

type conversationContextKey struct{}
type conversationContext struct {
	client *Client
	userID string
	token  string
}

// Replies using the handler context retain their own token even if parallel
// conversations evict it from the bounded cache before the handler sends.
func (c *Client) contextTokenFor(ctx context.Context, userID string) string {
	if value, ok := ctx.Value(conversationContextKey{}).(conversationContext); ok && value.client == c && value.userID == userID {
		return value.token
	}
	return c.ContextToken(userID)
}

// doPost performs an authenticated JSON POST to the iLink server.
func (c *Client) doPost(ctx context.Context, path string, body, result any) (err error) {
	ctx, diagnostic := startHTTPDiagnostic(ctx, c.cfg.Logger, path)
	if diagnostic != nil {
		defer func() { diagnostic.finish(ctx, err == nil) }()
	}
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	url := c.cfg.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("AuthorizationType", "ilink_bot_token")
	req.Header.Set("Authorization", "Bearer "+c.cfg.BotToken)
	req.Header.Set("X-WECHAT-UIN", c.wechatUIN)
	setAppHeaders(req)

	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return transportError(err)
	}
	if diagnostic != nil {
		diagnostic.statusCode = resp.StatusCode
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return &HTTPError{Endpoint: path, StatusCode: resp.StatusCode}
	}

	if diagnostic != nil {
		diagnostic.readStarted = time.Now()
	}
	respBody, err := readLimitedBody(resp.Body, c.cfg.MaxResponseBytes)
	if diagnostic != nil {
		diagnostic.readDuration = time.Since(diagnostic.readStarted)
	}
	if err != nil {
		return fmt.Errorf("read response: %w", transportError(err))
	}

	if result != nil {
		if err := json.Unmarshal(respBody, result); err != nil {
			return &responseDecodeError{cause: err}
		}
	}
	return nil
}

func setAppHeaders(req *http.Request) {
	req.Header.Set("iLink-App-Id", "bot")
	req.Header.Set("iLink-App-ClientVersion", clientVersionHeader)
}

func readLimitedBody(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, ErrResponseTooLarge
	}
	return data, nil
}

// Strip request URLs from transport failures, preserving cancellation and timeout causes.
func transportError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return &transportFailure{cause: urlErr.Err}
	}
	return &transportFailure{cause: err}
}

type transportFailure struct{ cause error }

func (e *transportFailure) Error() string { return "wechat: HTTP transport failed" }
func (e *transportFailure) Unwrap() error { return e.cause }

type responseDecodeError struct{ cause error }

func (e *responseDecodeError) Error() string { return "wechat: invalid JSON response" }
func (e *responseDecodeError) Unwrap() error { return e.cause }
