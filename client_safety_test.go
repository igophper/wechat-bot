package wechat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func newSafetyTestClient(t *testing.T, opts ...Option) *Client {
	t.Helper()
	client, err := NewClient(Config{BotToken: "test-bot-token", ILinkBotID: "test-bot"}, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestContextTokenCacheEvictsLeastRecentlyUsed(t *testing.T) {
	client := newSafetyTestClient(t, func(cfg *Config) { cfg.MaxContextTokens = 2 })
	client.SetContextToken("alice", "alice-token")
	client.SetContextToken("bob", "bob-token")
	if got := client.ContextToken("alice"); got != "alice-token" {
		t.Fatalf("restored token=%q", got)
	}
	client.SetContextToken("charlie", "charlie-token")
	if got := client.ContextToken("bob"); got != "" {
		t.Fatalf("least recently read entry remains: %q", got)
	}
	client.SetContextToken("alice", "alice-updated")
	client.SetContextToken("dana", "dana-token")
	if got := client.ContextToken("charlie"); got != "" {
		t.Fatalf("updating alice did not refresh recency: %q", got)
	}
	if got := client.ContextToken("alice"); got != "alice-updated" {
		t.Fatalf("updated token=%q", got)
	}
	client.ForgetContextToken("alice")
	client.ForgetContextToken("missing")
	if got := client.ContextToken("alice"); got != "" {
		t.Fatalf("forgotten token=%q", got)
	}
	client.SetContextToken("dana", "")
	if got := client.ContextToken("dana"); got != "" {
		t.Fatalf("empty token did not remove entry: %q", got)
	}
	client.SetContextToken("", "ignored")
	if got := client.ContextToken(""); got != "" {
		t.Fatalf("stored empty user ID: %q", got)
	}
}

func TestRestoredContextTokenIsSent(t *testing.T) {
	var got wechatSendRequest
	client := newSafetyTestClient(t, WithHTTPClient(&http.Client{Transport: safetyRoundTripper(func(req *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(req.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		return safetyJSONResponse(`{"ret":0}`), nil
	})}))
	client.SetContextToken("user", "persisted-conversation-token")
	if err := client.SendText(context.Background(), "user", "hello"); err != nil {
		t.Fatal(err)
	}
	if got.Msg.ContextToken != "persisted-conversation-token" {
		t.Fatalf("sent context token=%q", got.Msg.ContextToken)
	}
}

func TestContextTokenCacheConcurrentAccess(t *testing.T) {
	const capacity = 8
	client := newSafetyTestClient(t, func(cfg *Config) { cfg.MaxContextTokens = capacity })
	var workers sync.WaitGroup
	for worker := 0; worker < 12; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := 0; i < 100; i++ {
				user := fmt.Sprintf("user-%d", (i+worker)%16)
				client.SetContextToken(user, fmt.Sprintf("token-%d-%d", worker, i))
				_ = client.ContextToken(user)
				if i%3 == 0 {
					client.ForgetContextToken(user)
				}
			}
		}()
	}
	workers.Wait()
	// A final deterministic sequence verifies the capacity after concurrent mutation.
	for i := 0; i <= capacity; i++ {
		client.SetContextToken(fmt.Sprintf("final-%d", i), "final-token")
	}
	if got := client.ContextToken("final-0"); got != "" {
		t.Fatalf("cache exceeded capacity: %q", got)
	}
	for i := 1; i <= capacity; i++ {
		if got := client.ContextToken(fmt.Sprintf("final-%d", i)); got != "final-token" {
			t.Fatalf("entry %d lost: %q", i, got)
		}
	}
}

func TestJSONResponseSizeBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		wantErr    bool
	}{
		{name: "exact limit", body: `{"ret":0}`},
		{name: "one byte over limit", body: `{"ret":0} `, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newSafetyTestClient(t, func(cfg *Config) { cfg.MaxResponseBytes = int64(len(`{"ret":0}`)) }, WithHTTPClient(&http.Client{Transport: safetyRoundTripper(func(_ *http.Request) (*http.Response, error) {
				return safetyJSONResponse(tc.body), nil
			})}))
			err := client.SendText(context.Background(), "user", "hello")
			if tc.wantErr && !errors.Is(err, ErrResponseTooLarge) {
				t.Fatalf("oversized response error=%v", err)
			}
			if !tc.wantErr && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestResponseReaderStopsAtLimitPlusOne(t *testing.T) {
	reader := &safetyCountingReader{reader: strings.NewReader(strings.Repeat("x", 1024))}
	if data, err := readLimitedBody(reader, 8); !errors.Is(err, ErrResponseTooLarge) || data != nil {
		t.Fatalf("data=%q err=%v", data, err)
	}
	if reader.count != 9 {
		t.Fatalf("read %d bytes instead of limit+1", reader.count)
	}
}

func TestHTTPAndTransportErrorsRedactSecrets(t *testing.T) {
	t.Run("HTTP body", func(t *testing.T) {
		reader := &safetyCountingReader{reader: strings.NewReader("secret response body")}
		client := newSafetyTestClient(t, WithHTTPClient(&http.Client{Transport: safetyRoundTripper(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(reader), Header: make(http.Header)}, nil
		})}))
		err := client.SendText(context.Background(), "user", "hello")
		var statusErr *HTTPError
		if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusUnauthorized || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe HTTP error=%v", err)
		}
		if reader.count != 0 {
			t.Fatalf("read %d error body bytes unnecessarily", reader.count)
		}
	})
	for _, cause := range []error{
		&url.Error{Op: "Get", URL: "https://cdn.example/?token=secret", Err: context.Canceled},
		fmt.Errorf("secret transport detail: %w", context.DeadlineExceeded),
	} {
		client := newSafetyTestClient(t, WithHTTPClient(&http.Client{Transport: safetyRoundTripper(func(_ *http.Request) (*http.Response, error) { return nil, cause })}))
		err := client.SendText(context.Background(), "user", "hello")
		if err == nil || strings.Contains(err.Error(), "secret") || (!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)) {
			t.Fatalf("unsafe transport error or lost cancellation: %v", err)
		}
	}
	t.Run("response body read error", func(t *testing.T) {
		cause := fmt.Errorf("secret response stream URL: %w", context.Canceled)
		client := newSafetyTestClient(t, WithHTTPClient(&http.Client{Transport: safetyRoundTripper(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(safetyErrorReader{err: cause}), Header: make(http.Header)}, nil
		})}))
		err := client.SendText(context.Background(), "user", "hello")
		if err == nil || strings.Contains(err.Error(), "secret") || !errors.Is(err, context.Canceled) {
			t.Fatalf("unsafe read error or lost cancellation: %v", err)
		}
	})
}

func TestInvalidJSONResponseDoesNotEchoServerValue(t *testing.T) {
	client := newSafetyTestClient(t, WithHTTPClient(&http.Client{Transport: safetyRoundTripper(func(_ *http.Request) (*http.Response, error) {
		return safetyJSONResponse(`{"ret":98765432123456789012345678901234567890}`), nil
	})}))
	err := client.SendText(context.Background(), "user", "hello")
	var decodeErr *json.UnmarshalTypeError
	if err == nil || strings.Contains(err.Error(), "987654321") || !errors.As(err, &decodeErr) {
		t.Fatalf("unsafe decode error or lost cause: %v", err)
	}
}

func TestClientRejectsInvalidBaseURLs(t *testing.T) {
	for _, value := range []string{"ftp://example.com", "/relative", "https://user:secret@example.com", "https://example.com?token=secret", "https://example.com?", "https://example.com#secret", "https://", "https://example.com/%zz"} {
		for _, field := range []string{"api", "cdn"} {
			t.Run(field+" "+value, func(t *testing.T) {
				cfg := Config{BotToken: "token", ILinkBotID: "bot"}
				if field == "api" {
					cfg.BaseURL = value
				} else {
					cfg.CDNBaseURL = value
				}
				_, err := NewClient(cfg)
				if err == nil || strings.Contains(err.Error(), "secret") {
					t.Fatalf("invalid URL error=%v", err)
				}
			})
		}
	}
	client := newSafetyTestClient(t, WithBaseURL("http://localhost:8080/api/#"), WithCDNBaseURL("https://cdn.example/c2c/"))
	if client.BaseURL() != "http://localhost:8080/api" {
		t.Fatalf("trailing slash was not normalized: %q", client.BaseURL())
	}
}

func TestClientRejectsInvalidResourceBounds(t *testing.T) {
	for name, option := range map[string]Option{
		"negative response limit": func(c *Config) { c.MaxResponseBytes = -1 },
		"overflow response limit": func(c *Config) { c.MaxResponseBytes = math.MaxInt64 },
		"negative media limit":    func(c *Config) { c.MaxMediaBytes = -1 },
		"overflow media limit":    func(c *Config) { c.MaxMediaBytes = math.MaxInt64 },
		"negative cache capacity": func(c *Config) { c.MaxContextTokens = -1 },
		"negative concurrency":    WithHandlerConcurrency(-1),
		"inverted backoff":        WithBackoff(2, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewClient(Config{BotToken: "token", ILinkBotID: "bot"}, option); err == nil {
				t.Fatal("accepted invalid bounds")
			}
		})
	}
}

type safetyRoundTripper func(*http.Request) (*http.Response, error)

func (fn safetyRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }
func safetyJSONResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

type safetyCountingReader struct {
	reader io.Reader
	count  int
}

func (r *safetyCountingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.count += n
	return n, err
}

type safetyErrorReader struct{ err error }

func (r safetyErrorReader) Read(_ []byte) (int, error) { return 0, r.err }
