package wechat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGetQRCodeRequestProtocol(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != defaultQRCodePath || r.URL.Query().Get("bot_type") != "3" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("AuthorizationType") != "ilink_bot_token" || r.Header.Get("X-WECHAT-UIN") == "" {
			t.Error("missing QR POST headers")
		}
		assertLoginHeaders(t, r)
		var body struct {
			Tokens []string `json:"local_token_list"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Tokens == nil || len(body.Tokens) != 0 {
			t.Errorf("expected an empty local_token_list array, got %+v, %v", body, err)
		}
		_, _ = io.WriteString(w, `{"qrcode":"qr","qrcode_img_content":"https://example.test/qr"}`)
	}))
	defer server.Close()
	qr, err := GetQRCode(context.Background(), WithLoginBaseURL(server.URL+"/"))
	if err != nil || qr.QRCode != "qr" {
		t.Fatalf("GetQRCode: %v, %v", qr, err)
	}
}

func assertLoginHeaders(t *testing.T, r *http.Request) {
	t.Helper()
	if r.Header.Get("iLink-App-Id") != "bot" || r.Header.Get("iLink-App-ClientVersion") == "" {
		t.Error("missing application headers")
	}
	if r.Header.Get("Authorization") != "" {
		t.Error("login must not send bot authorization")
	}
}

func TestCheckQRStatusEncodesToken(t *testing.T) {
	const token = "qr &=+?/中文#"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != defaultQRStatusPath || r.URL.Query().Get("qrcode") != token || len(r.URL.Query()) != 1 {
			t.Error("QR token changed or injected query parameters")
		}
		assertLoginHeaders(t, r)
		if r.Header.Get("AuthorizationType") != "" || r.Header.Get("X-WECHAT-UIN") != "" {
			t.Error("status GET must not send POST authorization headers")
		}
		_, _ = io.WriteString(w, `{"status":"scaned"}`)
	}))
	defer server.Close()
	status, err := CheckQRStatus(context.Background(), token, WithLoginBaseURL(server.URL))
	if err != nil || status.Status != QRStatusScanned {
		t.Fatalf("CheckQRStatus: %v, %v", status, err)
	}
}

func TestQRConfirmationTerminalStates(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   error
	}{
		{QRStatusExpired, ErrQRExpired},
		{QRStatusNeedVerifyCode, ErrVerificationRequired},
		{QRStatusVerifyCodeBlocked, ErrVerificationBlocked},
		{QRStatusBoundRedirect, ErrQRAlreadyBound},
		{QRStatusConfirmed, ErrQRConfirmedNoCreds},
	} {
		t.Run(tc.status, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(QRStatusResult{Status: tc.status})
			}))
			defer server.Close()
			_, err := WaitForQRConfirmation(context.Background(), "qr", time.Hour, time.Second, nil, WithLoginBaseURL(server.URL))
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestQRConfirmationPollsImmediately(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":"confirmed","bot_token":"token","ilink_bot_id":"bot"}`)
	}))
	defer server.Close()
	creds, err := WaitForQRConfirmation(context.Background(), "qr", time.Hour, time.Second, nil, WithLoginBaseURL(server.URL))
	if err != nil || creds.ILinkBotID != "bot" {
		t.Fatalf("first poll was delayed or failed: %v", err)
	}
}

func TestQRConfirmationRedirect(t *testing.T) {
	const token = "qr&value"
	destination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("qrcode") != token {
			t.Error("redirect lost QR token")
		}
		assertLoginHeaders(t, r)
		_, _ = io.WriteString(w, `{"status":"confirmed","bot_token":"token","ilink_bot_id":"bot"}`)
	}))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(QRStatusResult{Status: QRStatusRedirect, RedirectHost: strings.TrimPrefix(destination.URL, "https://")})
	}))
	defer source.Close()
	creds, err := WaitForQRConfirmation(context.Background(), token, time.Hour, time.Second, nil,
		WithLoginBaseURL(source.URL), WithLoginHTTPClient(destination.Client()))
	if err != nil || creds.ILinkBotID != "bot" {
		t.Fatalf("redirect did not complete: %v", err)
	}
}

func TestQRConfirmationRejectsMalformedRedirects(t *testing.T) {
	for _, host := range []string{"", "https://example.test", "user@example.test", "example.test/path", "example.test?secret=x", "example.test#fragment", "example.test?", "example.test:badport"} {
		t.Run(host, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(QRStatusResult{Status: QRStatusRedirect, RedirectHost: host})
			}))
			defer server.Close()
			_, err := WaitForQRConfirmation(context.Background(), "qr", time.Hour, time.Second, nil, WithLoginBaseURL(server.URL))
			if err == nil || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("expected immediate malformed-host error, got %v", err)
			}
		})
	}
}

func TestQRConfirmationRejectsRedirectLoop(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(QRStatusResult{Status: QRStatusRedirect, RedirectHost: strings.TrimPrefix(server.URL, "https://")})
	}))
	defer server.Close()
	_, err := WaitForQRConfirmation(context.Background(), "qr", time.Hour, time.Second, nil,
		WithLoginBaseURL(server.URL), WithLoginHTTPClient(server.Client()))
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected redirect-loop error, got %v", err)
	}
}

func TestQRVerificationFlow(t *testing.T) {
	var polls atomic.Int32
	var callbackCount int
	var statuses []string
	const code = "1&2=+3"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := polls.Add(1)
		gotCode := r.URL.Query().Get("verify_code")
		if n == 2 && gotCode != "wrong" || n == 3 && gotCode != code || n >= 4 && gotCode != "" {
			t.Errorf("poll %d has wrong verification code", n)
		}
		switch n {
		case 1, 2:
			_, _ = io.WriteString(w, `{"status":"need_verifycode"}`)
		case 3:
			_, _ = io.WriteString(w, `{"status":"scaned"}`)
		case 4:
			_, _ = io.WriteString(w, `{"status":"wait"}`)
		default:
			_, _ = io.WriteString(w, `{"status":"confirmed","bot_token":"token","ilink_bot_id":"bot"}`)
		}
	}))
	defer server.Close()
	creds, err := WaitForQRConfirmation(context.Background(), "qr", time.Millisecond, time.Second,
		func(status string) { statuses = append(statuses, status) },
		WithLoginBaseURL(server.URL), WithVerificationCodeHandler(func(ctx context.Context) (string, error) {
			callbackCount++
			if callbackCount == 1 {
				return "wrong", nil
			}
			return "  " + code + "  ", nil
		}))
	if err != nil || creds.ILinkBotID != "bot" || callbackCount != 2 {
		t.Fatalf("verification flow: creds=%v err=%v calls=%d", creds, err, callbackCount)
	}
	if strings.Join(statuses, ",") != "need_verifycode,scaned,wait,confirmed" {
		t.Fatalf("unexpected status transitions: %v", statuses)
	}
}

func TestQRVerificationCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":"need_verifycode"}`)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := WaitForQRConfirmation(ctx, "qr", time.Hour, time.Second, nil,
		WithLoginBaseURL(server.URL), WithVerificationCodeHandler(func(ctx context.Context) (string, error) {
			cancel()
			<-ctx.Done()
			return "", ctx.Err()
		}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestQRHTTPErrorHandling(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var polls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if polls.Add(1) == 1 {
					w.WriteHeader(status)
					_, _ = io.WriteString(w, "secret-QR-or-code")
					return
				}
				_, _ = io.WriteString(w, `{"status":"confirmed","bot_token":"token","ilink_bot_id":"bot"}`)
			}))
			defer server.Close()
			_, err := WaitForQRConfirmation(context.Background(), "qr", time.Millisecond, time.Second, nil, WithLoginBaseURL(server.URL))
			if status == http.StatusUnauthorized {
				var httpErr *HTTPError
				if !errors.As(err, &httpErr) || httpErr.StatusCode != status || polls.Load() != 1 || strings.Contains(err.Error(), "secret") {
					t.Fatalf("unexpected permanent HTTP error: %v, polls=%d", err, polls.Load())
				}
			} else if err != nil || polls.Load() != 2 {
				t.Fatalf("temporary HTTP error was not retried: %v, polls=%d", err, polls.Load())
			}
		})
	}
}

func TestLoginRejectsOversizedAndInvalidResponses(t *testing.T) {
	for _, body := range []string{strings.Repeat("x", int(DefaultMaxResponseBytes)+1), `{"status":secret-value}`, `{}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, body)
		}))
		_, err := WaitForQRConfirmation(context.Background(), "qr", time.Hour, time.Second, nil, WithLoginBaseURL(server.URL))
		server.Close()
		if err == nil || errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "secret-value") {
			t.Fatalf("expected immediate response validation error, got %v", err)
		}
	}
}

func TestQRConfirmationCancelWhileWaiting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":"wait"}`)
	}))
	defer server.Close()
	_, err := WaitForQRConfirmation(ctx, "qr", time.Hour, time.Second, func(string) { cancel() }, WithLoginBaseURL(server.URL))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation during poll interval, got %v", err)
	}
}

type loginRoundTripper func(*http.Request) (*http.Response, error)

func (f loginRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type loginTemporaryError struct{}

func (loginTemporaryError) Error() string   { return "temporary network failure" }
func (loginTemporaryError) Timeout() bool   { return true }
func (loginTemporaryError) Temporary() bool { return true }

func TestQRTransportErrors(t *testing.T) {
	for _, temporary := range []bool{false, true} {
		var polls int
		permanentErr := errors.New("permanent transport failure")
		client := &http.Client{Transport: loginRoundTripper(func(r *http.Request) (*http.Response, error) {
			polls++
			if !temporary {
				return nil, permanentErr
			}
			if polls == 1 {
				return nil, loginTemporaryError{}
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"status":"confirmed","bot_token":"token","ilink_bot_id":"bot"}`)),
				Header:     make(http.Header),
				Request:    r,
			}, nil
		})}
		_, err := WaitForQRConfirmation(context.Background(), "private-qr-token", time.Millisecond, time.Second, nil,
			WithLoginBaseURL("https://example.invalid"), WithLoginHTTPClient(client))
		if temporary {
			if err != nil || polls != 2 {
				t.Fatalf("temporary failure not retried: %v, polls=%d", err, polls)
			}
		} else if !errors.Is(err, permanentErr) || polls != 1 || strings.Contains(err.Error(), "private-qr-token") {
			t.Fatalf("permanent failure lost or disclosed QR token: %v, polls=%d", err, polls)
		}
	}
}

func TestQRVerificationHandlerErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":"need_verifycode"}`)
	}))
	defer server.Close()
	want := errors.New("input unavailable")
	_, err := WaitForQRConfirmation(context.Background(), "qr", time.Hour, time.Second, nil,
		WithLoginBaseURL(server.URL), WithVerificationCodeHandler(func(context.Context) (string, error) {
			return "", want
		}))
	if !errors.Is(err, want) {
		t.Fatalf("handler error lost: %v", err)
	}
	_, err = WaitForQRConfirmation(context.Background(), "qr", time.Hour, time.Second, nil,
		WithLoginBaseURL(server.URL), WithVerificationCodeHandler(func(context.Context) (string, error) {
			return "  ", nil
		}))
	if !errors.Is(err, ErrVerificationRequired) {
		t.Fatalf("empty code must not retry indefinitely: %v", err)
	}
}

// An unrecognized non-empty status may be a server state this SDK predates.
// Login keeps polling at the normal interval instead of failing outright, and
// reports the value through onStatus so it stays diagnosable.
func TestQRUnknownStatusKeepsPolling(t *testing.T) {
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if polls.Add(1) < 3 {
			_, _ = io.WriteString(w, `{"status":"brand_new_server_state"}`)
			return
		}
		_, _ = io.WriteString(w, `{"status":"confirmed","bot_token":"token","ilink_bot_id":"bot"}`)
	}))
	defer server.Close()

	var seen []string
	creds, err := WaitForQRConfirmation(context.Background(), "qr", time.Millisecond, 2*time.Second,
		func(status string) { seen = append(seen, status) }, WithLoginBaseURL(server.URL))
	if err != nil || creds.ILinkBotID != "bot" {
		t.Fatalf("unknown status did not recover: creds=%v err=%v", creds, err)
	}
	if strings.Join(seen, ",") != "brand_new_server_state,confirmed" {
		t.Fatalf("unknown status not reported to caller: %v", seen)
	}
}

// A fast verification callback must not turn polling into a request flood:
// every poll, including one carrying a new code, waits out the interval.
func TestQRVerificationIsThrottledAndBounded(t *testing.T) {
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		polls.Add(1)
		_, _ = io.WriteString(w, `{"status":"need_verifycode"}`)
	}))
	defer server.Close()

	calls := 0
	start := time.Now()
	_, err := WaitForQRConfirmation(context.Background(), "qr", 20*time.Millisecond, 5*time.Second, nil,
		WithLoginBaseURL(server.URL), WithVerificationCodeHandler(func(context.Context) (string, error) {
			calls++
			return "123456", nil
		}))
	if !errors.Is(err, ErrVerificationAttemptsExhausted) {
		t.Fatalf("expected exhausted attempts, got %v", err)
	}
	if calls != DefaultVerificationAttempts {
		t.Fatalf("callback ran %d times, want %d", calls, DefaultVerificationAttempts)
	}
	// One poll per attempt plus the poll that detects exhaustion.
	if got := polls.Load(); got > int32(DefaultVerificationAttempts)+1 {
		t.Fatalf("verification polled %d times: interval was not applied", got)
	}
	if elapsed := time.Since(start); elapsed < time.Duration(DefaultVerificationAttempts)*20*time.Millisecond {
		t.Fatalf("polls were not throttled: finished in %v", elapsed)
	}
}

// WithVerificationAttempts overrides the default limit.
func TestQRVerificationAttemptLimitIsConfigurable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":"need_verifycode"}`)
	}))
	defer server.Close()
	calls := 0
	_, err := WaitForQRConfirmation(context.Background(), "qr", time.Millisecond, 2*time.Second, nil,
		WithLoginBaseURL(server.URL), WithVerificationAttempts(1),
		WithVerificationCodeHandler(func(context.Context) (string, error) { calls++; return "1", nil }))
	if !errors.Is(err, ErrVerificationAttemptsExhausted) || calls != 1 {
		t.Fatalf("attempt limit not honored: calls=%d err=%v", calls, err)
	}
	// A client-side limit must stay distinguishable from a server-side block.
	if errors.Is(err, ErrVerificationBlocked) {
		t.Fatal("client attempt limit must not report a server block")
	}
}
