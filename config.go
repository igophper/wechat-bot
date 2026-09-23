package wechat

import (
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Config defines the configuration parameters for WeChat Bot Client.
type Config struct {
	// BotToken is the bearer token returned by iLink QR scan login (Required).
	BotToken string

	// ILinkBotID is the bot identifier (e.g. "4090de018d12@im.bot") (Required).
	ILinkBotID string

	// ILinkUserID is the operator's user id returned by iLink (Optional).
	ILinkUserID string

	// BaseURL is the API server base URL. Defaults to "https://ilinkai.weixin.qq.com".
	BaseURL string

	// CDNBaseURL is the media upload CDN base URL. Defaults to "https://novac2c.cdn.weixin.qq.com/c2c".
	CDNBaseURL string

	// HTTPClient defaults to a new client using http.DefaultTransport.
	// Its transport is borrowed and is not closed by Client.
	HTTPClient *http.Client

	// Storage manages get_updates_buf persistence. Defaults to MemoryBufStorage.
	Storage BufStorage

	// Logger is the structured logger. Defaults to slog.Default().
	Logger *slog.Logger

	// Timeouts
	SendTimeout     time.Duration
	MediaTimeout    time.Duration
	TypingTimeout   time.Duration
	LongPollTimeout time.Duration

	// HandlerConcurrency bounds concurrent user conversations per batch. Default: 1.
	// Each user's items are processed serially in their received order.
	HandlerConcurrency int

	// BackoffInitial and BackoffMax bound exponential polling retries.
	// Actual delays are jittered within the upper half of the computed
	// interval and never exceed BackoffMax.
	BackoffInitial time.Duration
	BackoffMax     time.Duration

	// MediaHosts pins the hosts that media uploads and downloads may contact,
	// checked on the server-supplied URL and again on every redirect hop. The
	// CDNBaseURL host is always allowed. While this is empty, any public host
	// is accepted and only private, loopback, and link-local addresses are
	// refused; set it to pin a known CDN or a self-hosted deployment.
	MediaHosts []string

	// MaxResponseBytes limits JSON responses. Default: 1 MiB.
	MaxResponseBytes int64
	// MaxMediaBytes limits plaintext uploads and downloads. Default: 32 MiB.
	MaxMediaBytes int64
	// MaxContextTokens bounds the LRU conversation-token cache. Default: 1024.
	MaxContextTokens int

	// EmptyBufExpiredThreshold is the number of consecutive empty-buf SessionExpired
	// responses before Start stops and signals reauthentication. Defaults to 20.
	EmptyBufExpiredThreshold int
}

// WithHandlerConcurrency limits concurrent conversations; each user stays ordered.
func WithHandlerConcurrency(n int) Option {
	return func(c *Config) { c.HandlerConcurrency = n }
}

// WithBackoff sets the initial and maximum delays between failed polls.
func WithBackoff(initial, maximum time.Duration) Option {
	return func(c *Config) { c.BackoffInitial, c.BackoffMax = initial, maximum }
}

// WithMediaHosts pins the hosts that media transfers may contact, including
// every redirect hop. The CDNBaseURL host is always allowed.
func WithMediaHosts(hosts ...string) Option {
	return func(c *Config) { c.MediaHosts = hosts }
}

// Option is a functional option for configuring Config or Client.
type Option func(*Config)

// WithBaseURL overrides the API base URL.
func WithBaseURL(baseURL string) Option {
	return func(c *Config) {
		c.BaseURL = baseURL
	}
}

// WithCDNBaseURL overrides the CDN base URL.
func WithCDNBaseURL(cdnBaseURL string) Option {
	return func(c *Config) {
		c.CDNBaseURL = cdnBaseURL
	}
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(client *http.Client) Option {
	return func(c *Config) {
		c.HTTPClient = client
	}
}

// WithStorage sets a custom sync cursor storage.
func WithStorage(storage BufStorage) Option {
	return func(c *Config) {
		c.Storage = storage
	}
}

// WithLogger sets a custom structured logger.
func WithLogger(logger *slog.Logger) Option {
	return func(c *Config) {
		c.Logger = logger
	}
}

// WithUserID sets the operator's iLink user ID.
func WithUserID(userID string) Option {
	return func(c *Config) {
		c.ILinkUserID = userID
	}
}

// WithSendTimeout sets the timeout for text message sending.
func WithSendTimeout(t time.Duration) Option {
	return func(c *Config) {
		c.SendTimeout = t
	}
}

// WithMediaTimeout sets the timeout for media upload and sending.
func WithMediaTimeout(t time.Duration) Option {
	return func(c *Config) {
		c.MediaTimeout = t
	}
}

// WithLongPollTimeout sets the long-polling timeout.
func WithLongPollTimeout(t time.Duration) Option {
	return func(c *Config) {
		c.LongPollTimeout = t
	}
}

// WithExpiredThreshold sets the threshold of consecutive empty-buf failures before declaring expiry.
func WithExpiredThreshold(threshold int) Option {
	return func(c *Config) {
		c.EmptyBufExpiredThreshold = threshold
	}
}

func (c *Config) setDefaults() {
	if c.BaseURL == "" {
		c.BaseURL = DefaultBaseURL
	}
	if c.CDNBaseURL == "" {
		c.CDNBaseURL = DefaultCDNBaseURL
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{}
	}
	if c.Storage == nil {
		c.Storage = NewMemoryBufStorage()
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.SendTimeout <= 0 {
		c.SendTimeout = DefaultSendTimeout
	}
	if c.MediaTimeout <= 0 {
		c.MediaTimeout = DefaultMediaTimeout
	}
	if c.TypingTimeout <= 0 {
		c.TypingTimeout = DefaultTypingTimeout
	}
	if c.LongPollTimeout <= 0 {
		c.LongPollTimeout = DefaultLongPollTimeout
	}
	if c.EmptyBufExpiredThreshold <= 0 {
		c.EmptyBufExpiredThreshold = DefaultEmptyBufExpiredThreshold
	}
	if c.HandlerConcurrency == 0 {
		c.HandlerConcurrency = 1
	}
	if c.BackoffInitial == 0 {
		c.BackoffInitial = DefaultBackoffInitial
	}
	if c.BackoffMax == 0 {
		c.BackoffMax = DefaultBackoffMax
	}
	if c.MaxResponseBytes == 0 {
		c.MaxResponseBytes = DefaultMaxResponseBytes
	}
	if c.MaxMediaBytes == 0 {
		c.MaxMediaBytes = DefaultMaxMediaBytes
	}
	if c.MaxContextTokens == 0 {
		c.MaxContextTokens = DefaultMaxContextTokens
	}
}

func (c *Config) validate() error {
	if c.HandlerConcurrency < 1 || c.MaxContextTokens < 1 || c.BackoffInitial <= 0 || c.BackoffMax < c.BackoffInitial {
		return fmt.Errorf("wechat: invalid concurrency, cache capacity, or backoff bounds")
	}
	if c.MaxResponseBytes < 1 || c.MaxResponseBytes == math.MaxInt64 || c.MaxMediaBytes < 1 || c.MaxMediaBytes > math.MaxInt64-32 {
		return fmt.Errorf("wechat: invalid response or media byte limit")
	}
	if c.LongPollTimeout > time.Duration(math.MaxInt64)-5*time.Second {
		return fmt.Errorf("wechat: long-poll timeout is too large")
	}
	for _, base := range []*string{&c.BaseURL, &c.CDNBaseURL} {
		u, err := url.Parse(*base)
		if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			return fmt.Errorf("wechat: base URLs require http(s), a host, and no credentials, query, or fragment")
		}
		*base = strings.TrimRight(u.String(), "/")
	}
	return nil
}
