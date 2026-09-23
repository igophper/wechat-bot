package wechat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/mdp/qrterminal/v3"
)

const (
	defaultQRCodePath   = "/ilink/bot/get_bot_qrcode"
	defaultQRStatusPath = "/ilink/bot/get_qrcode_status"
	maxQRRedirects      = 5
)

// GetQRCode requests a new login QR code from the iLink platform.
func GetQRCode(ctx context.Context, opts ...LoginOption) (*QRCodeResult, error) {
	lo := resolveLoginOptions(opts)
	endpoint, err := loginEndpoint(lo.baseURL, defaultQRCodePath, url.Values{"bot_type": {"3"}})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(`{"local_token_list":[]}`))
	if err != nil {
		return nil, fmt.Errorf("create qrcode request: %w", transportError(err))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("AuthorizationType", "ilink_bot_token")
	req.Header.Set("X-WECHAT-UIN", GenerateUIN())
	setAppHeaders(req)

	var out QRCodeResult
	if err := lo.doJSON(req, defaultQRCodePath, &out); err != nil {
		return nil, err
	}
	if out.QRCode == "" {
		return nil, ErrEmptyQRCode
	}
	return &out, nil
}

// CheckQRStatus polls the iLink platform once for the current scan state of a QR code.
// Use WaitForQRConfirmation to follow redirects and handle verification codes.
func CheckQRStatus(ctx context.Context, qrcode string, opts ...LoginOption) (*QRStatusResult, error) {
	lo := resolveLoginOptions(opts)
	return lo.checkQRStatus(ctx, qrcode, "")
}

func (lo loginOptions) checkQRStatus(ctx context.Context, qrcode, verifyCode string) (*QRStatusResult, error) {
	if qrcode == "" {
		return nil, ErrEmptyQRCode
	}
	query := url.Values{"qrcode": {qrcode}}
	if verifyCode != "" {
		query.Set("verify_code", verifyCode)
	}
	endpoint, err := loginEndpoint(lo.baseURL, defaultQRStatusPath, query)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create qr status request: %w", transportError(err))
	}
	setAppHeaders(req)

	var out QRStatusResult
	if err := lo.doJSON(req, defaultQRStatusPath, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func loginEndpoint(baseURL, path string, query url.Values) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", errors.New("wechat: invalid login base URL")
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawPath = ""
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func (lo loginOptions) doJSON(req *http.Request, endpoint string, out any) error {
	resp, err := lo.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("login request: %w", transportError(err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return &HTTPError{Endpoint: endpoint, StatusCode: resp.StatusCode}
	}
	body, err := readLimitedBody(resp.Body, DefaultMaxResponseBytes)
	if err != nil {
		return fmt.Errorf("read login response: %w", transportError(err))
	}
	if err := json.Unmarshal(body, out); err != nil {
		// A type error can embed an untrusted JSON value, so do not include it.
		return errors.New("wechat: invalid login response JSON")
	}
	return nil
}

// WaitForQRConfirmation polls immediately, then at interval until the QR code is
// confirmed, expired, or canceled. Temporary network and server errors are retried.
// Every poll, including one that carries a new verification code, waits out the
// interval first, so a fast callback cannot turn verification into a request
// flood. Verification requests require WithVerificationCodeHandler and stop after
// WithVerificationAttempts codes with ErrVerificationAttemptsExhausted.
// Unrecognized non-empty statuses are reported through onStatus and polled
// through, so a new server state does not break login outright. Status callbacks
// are synchronous and must return promptly. Server-directed redirects use HTTPS,
// are limited to five hops, and must satisfy WithLoginHosts.
func WaitForQRConfirmation(ctx context.Context, qrcode string, interval, timeout time.Duration, onStatus func(status string), opts ...LoginOption) (*Credentials, error) {
	if interval <= 0 {
		interval = 3 * time.Second
	}
	if timeout <= 0 {
		timeout = 3 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	lo := resolveLoginOptions(opts)
	// The policy is anchored to the operator-configured host so a redirect
	// cannot widen the trust boundary by becoming the new anchor.
	policy := newURLPolicy(lo.baseURL, lo.hosts)
	lastStatus, verifyCode := "", ""
	redirects := make(map[string]bool)
	verifyAttempts := 0

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		res, err := lo.checkQRStatus(ctx, qrcode, verifyCode)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if !retryQRRequest(err) {
				return nil, err
			}
		} else {
			if res.Status != lastStatus {
				lastStatus = res.Status
				if onStatus != nil {
					onStatus(res.Status)
				}
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			switch res.Status {
			case QRStatusConfirmed:
				if res.BotToken == "" || res.ILinkBotID == "" {
					return nil, ErrQRConfirmedNoCreds
				}
				return &Credentials{
					BotToken: res.BotToken, ILinkBotID: res.ILinkBotID,
					BaseURL: res.BaseURL, ILinkUserID: res.ILinkUserID,
				}, nil
			case QRStatusExpired:
				return nil, ErrQRExpired
			case QRStatusNeedVerifyCode:
				if lo.verificationCodeHandler == nil {
					return nil, ErrVerificationRequired
				}
				// Stop after the configured number of codes. This is a client
				// limit, so it is reported separately from a server block.
				if verifyAttempts >= lo.maxVerifyAttempts {
					return nil, ErrVerificationAttemptsExhausted
				}
				verifyAttempts++
				verifyCode, err = lo.verificationCodeHandler(ctx)
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				if err != nil {
					return nil, fmt.Errorf("get verification code: %w", err)
				}
				verifyCode = strings.TrimSpace(verifyCode)
				if verifyCode == "" {
					return nil, ErrVerificationRequired
				}
				// Fall through to the shared wait: a handler that returns
				// instantly must not turn this into an unthrottled retry loop.
			case QRStatusVerifyCodeBlocked:
				return nil, ErrVerificationBlocked
			case QRStatusBoundRedirect:
				return nil, ErrQRAlreadyBound
			case QRStatusRedirect:
				baseURL, err := qrRedirectURL(res.RedirectHost, policy)
				if err != nil {
					return nil, err
				}
				if len(redirects) >= maxQRRedirects || redirects[baseURL] {
					return nil, errors.New("wechat: qr redirect limit or loop detected")
				}
				redirects[baseURL] = true
				lo.baseURL = baseURL
				continue
			case QRStatusScanned:
				verifyCode = ""
			case QRStatusWait:
			default:
				// An empty status means the response was not a status at all.
				if res.Status == "" {
					return nil, errors.New("wechat: qr status response has no status")
				}
				// Treat any other unrecognized value as a state this client does
				// not know yet and keep polling at the normal interval. The value
				// already reached the caller through onStatus for diagnosis.
			}
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// qrRedirectURL validates a server-chosen login host. Beyond the shape check
// it applies the same policy as media URLs, so a redirect cannot aim the
// credential exchange at a private or loopback address.
func qrRedirectURL(host string, policy urlPolicy) (string, error) {
	u, err := url.Parse("https://" + host)
	if err != nil || u.Hostname() == "" || u.Host != host || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", errors.New("wechat: invalid qr redirect host")
	}
	if err := policy.check(u); err != nil {
		return "", err
	}
	return u.String(), nil
}

func retryQRRequest(err error) bool {
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode == http.StatusRequestTimeout || httpErr.StatusCode == http.StatusTooManyRequests || httpErr.StatusCode >= 500
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// LoginOption configures the QR code login helper.
type LoginOption func(*loginOptions)

type loginOptions struct {
	baseURL                 string
	httpClient              *http.Client
	verificationCodeHandler func(context.Context) (string, error)
	hosts                   []string
	maxVerifyAttempts       int
}

func defaultLoginOptions() loginOptions {
	return loginOptions{
		baseURL:           DefaultBaseURL,
		httpClient:        &http.Client{Timeout: 45 * time.Second},
		maxVerifyAttempts: DefaultVerificationAttempts,
	}
}

func resolveLoginOptions(opts []LoginOption) loginOptions {
	lo := defaultLoginOptions()
	for _, opt := range opts {
		opt(&lo)
	}
	return lo
}

// WithLoginBaseURL sets a custom base URL for QR login.
func WithLoginBaseURL(baseURL string) LoginOption {
	return func(o *loginOptions) {
		if baseURL != "" {
			o.baseURL = baseURL
		}
	}
}

// WithLoginHTTPClient sets a custom HTTP client for QR login.
func WithLoginHTTPClient(client *http.Client) LoginOption {
	return func(o *loginOptions) {
		if client != nil {
			o.httpClient = client
		}
	}
}

// WithVerificationCodeHandler supplies the code shown by WeChat when requested.
// It may be called again if a code is rejected. The handler must honor ctx
// cancellation and must not log or persist verification codes.
func WithVerificationCodeHandler(handler func(context.Context) (string, error)) LoginOption {
	return func(o *loginOptions) {
		o.verificationCodeHandler = handler
	}
}

// WithLoginHosts pins the hosts a server-directed login redirect may point to.
// The configured login base host is always allowed. While this is unset, any
// public host is accepted and private, loopback, and link-local addresses are
// refused.
func WithLoginHosts(hosts ...string) LoginOption {
	return func(o *loginOptions) {
		o.hosts = hosts
	}
}

// WithVerificationAttempts caps how many verification codes this client will
// submit before giving up with ErrVerificationAttemptsExhausted. Values below
// one restore the default.
func WithVerificationAttempts(n int) LoginOption {
	return func(o *loginOptions) {
		if n < 1 {
			n = DefaultVerificationAttempts
		}
		o.maxVerifyAttempts = n
	}
}

// PrintTerminalQR renders a QR code directly into the terminal using half-block Unicode characters.
func PrintTerminalQR(content string, out ...io.Writer) {
	w := io.Writer(os.Stdout)
	if len(out) > 0 && out[0] != nil {
		w = out[0]
	}
	config := qrterminal.Config{
		Level:      qrterminal.L,
		Writer:     w,
		HalfBlocks: true,
		QuietZone:  1,
	}
	qrterminal.GenerateWithConfig(content, config)
}

// PrintTerminal prints this QR code to the terminal stdout.
// It prioritizes QRCodeImgContent (which contains the scannable WeChat login URL),
// and falls back to QRCode if QRCodeImgContent is empty.
func (q *QRCodeResult) PrintTerminal(out ...io.Writer) {
	content := q.QRCodeImgContent
	if content == "" {
		content = q.QRCode
	}
	PrintTerminalQR(content, out...)
}
