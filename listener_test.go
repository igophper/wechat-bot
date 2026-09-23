package wechat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type pollTransport func(*http.Request) (*http.Response, error)

func (f pollTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func pollJSON(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func newPollClient(t *testing.T, store BufStorage, transport pollTransport, opts ...Option) *Client {
	t.Helper()
	c, err := NewClient(Config{BotToken: "test-token", ILinkBotID: "test-bot", Storage: store,
		HTTPClient: &http.Client{Transport: transport}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, opts...)
	if err != nil {
		t.Fatal(err)
	}
	// Pin the jitter so backoff timing assertions stay exact; equalJitter has
	// its own bounds test.
	c.jitter = func(d time.Duration) time.Duration { return d }
	c.OnMessage(func(context.Context, *InboundMessage) error { return nil })
	return c
}

const onePollMessage = `{"ret":0,"get_updates_buf":"next","msgs":[{"message_id":1,"message_type":1,"message_state":2,"from_user_id":"alice","context_token":"conversation","item_list":[{"type":1,"text_item":{"text":"hello"}}]}]}`

func TestListenerErrorsBackoffAndCursor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		store := NewMemoryBufStorage()
		var starts []time.Duration
		var cursors []string
		start := time.Now()
		calls, handled := 0, 0
		c := newPollClient(t, store, func(r *http.Request) (*http.Response, error) {
			starts = append(starts, time.Since(start))
			var req wechatGetUpdatesRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			cursors = append(cursors, req.GetUpdatesBuf)
			calls++
			switch calls {
			case 1:
				return pollJSON(`{"ret":-1,"get_updates_buf":"bad"}`), nil
			case 2:
				return pollJSON(`{"errcode":-2,"get_updates_buf":"bad"}`), nil
			case 3:
				return nil, errors.New("temporary network failure")
			case 4:
				return pollJSON(onePollMessage), nil
			case 5:
				return pollJSON(`{"ret":-1,"errcode":-2}`), nil
			default:
				cancel()
				return nil, ctx.Err()
			}
		})
		c.OnMessage(func(context.Context, *InboundMessage) error { handled++; return nil })
		if err := c.Start(ctx); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		want := []time.Duration{0, 3 * time.Second, 9 * time.Second, 21 * time.Second, 21 * time.Second, 24 * time.Second}
		if !reflect.DeepEqual(starts, want) {
			t.Fatalf("retry delays: got %v want %v", starts, want)
		}
		if !reflect.DeepEqual(cursors, []string{"", "", "", "", "next", "next"}) {
			t.Fatalf("cursors: %v", cursors)
		}
		if handled != 1 {
			t.Fatalf("handled=%d", handled)
		}
	})
}

func TestListenerSessionExpiryFields(t *testing.T) {
	for _, body := range []string{`{"ret":-14}`, `{"errcode":-14}`} {
		t.Run(body, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := NewMemoryBufStorage()
				if err := store.SaveBuf(context.Background(), "test-bot", "stale"); err != nil {
					t.Fatal(err)
				}
				var cursors []string
				c := newPollClient(t, store, func(r *http.Request) (*http.Response, error) {
					var req wechatGetUpdatesRequest
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Fatal(err)
					}
					cursors = append(cursors, req.GetUpdatesBuf)
					return pollJSON(body), nil
				}, WithExpiredThreshold(1))
				expired := 0
				c.OnExpired(func(string) { expired++ })
				if err := c.Start(context.Background()); !errors.Is(err, ErrTokenRevoked) {
					t.Fatal(err)
				}
				if expired != 1 || !reflect.DeepEqual(cursors, []string{"stale", ""}) {
					t.Fatalf("expired=%d cursors=%v", expired, cursors)
				}
			})
		})
	}
}

func TestListenerRecoveryResetsExpiryCount(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		c := newPollClient(t, nil, func(*http.Request) (*http.Response, error) {
			calls++
			if calls == 2 {
				return pollJSON(`{"ret":0}`), nil
			}
			return pollJSON(`{"ret":-14}`), nil
		}, WithExpiredThreshold(2))
		if err := c.Start(context.Background()); !errors.Is(err, ErrTokenRevoked) {
			t.Fatal(err)
		}
		if calls != 4 {
			t.Fatalf("calls=%d: success must reset expiry counter", calls)
		}
	})
}

type failingCursorStore struct {
	BufStorage
	loadErr, saveErr, clearErr error
}

func (s failingCursorStore) LoadBuf(ctx context.Context, id string) (string, error) {
	if s.loadErr != nil {
		return "", s.loadErr
	}
	return s.BufStorage.LoadBuf(ctx, id)
}
func (s failingCursorStore) SaveBuf(ctx context.Context, id, buf string) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	return s.BufStorage.SaveBuf(ctx, id, buf)
}
func (s failingCursorStore) ClearBuf(ctx context.Context, id string) error {
	if s.clearErr != nil {
		return s.clearErr
	}
	return s.BufStorage.ClearBuf(ctx, id)
}

func TestListenerFailuresDoNotCommit(t *testing.T) {
	failure := errors.New("business processing failed")
	for _, mode := range []string{"handler", "panic", "cancel", "save"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store := NewMemoryBufStorage()
			if err := store.SaveBuf(ctx, "test-bot", "previous"); err != nil {
				t.Fatal(err)
			}
			var persistence BufStorage = store
			if mode == "save" {
				persistence = failingCursorStore{BufStorage: store, saveErr: failure}
			}
			c := newPollClient(t, persistence, func(*http.Request) (*http.Response, error) { return pollJSON(onePollMessage), nil })
			c.OnMessage(func(context.Context, *InboundMessage) error {
				switch mode {
				case "handler":
					return failure
				case "panic":
					panic("secret panic value")
				case "cancel":
					cancel()
				}
				return nil
			})
			err := c.Start(ctx)
			want := failure
			if mode == "panic" {
				want = ErrHandlerPanic
				var p *HandlerPanicError
				if !errors.As(err, &p) || len(p.Stack) == 0 {
					t.Fatalf("missing panic diagnostics: %v", err)
				}
			}
			if mode == "cancel" {
				want = context.Canceled
			}
			if !errors.Is(err, want) {
				t.Fatalf("got %v want %v", err, want)
			}
			cursor, _ := store.LoadBuf(context.Background(), "test-bot")
			if cursor != "previous" {
				t.Fatalf("failed batch committed cursor %q", cursor)
			}
			if strings.Contains(err.Error(), "secret panic value") {
				t.Fatal("panic data exposed")
			}
		})
	}
}

func TestListenerStorageFailuresSurface(t *testing.T) {
	failure := errors.New("disk unavailable")
	for _, mode := range []string{"load", "reset", "expire"} {
		t.Run(mode, func(t *testing.T) {
			store := NewMemoryBufStorage()
			if mode == "reset" {
				if err := store.SaveBuf(context.Background(), "test-bot", "stale"); err != nil {
					t.Fatal(err)
				}
			}
			fs := failingCursorStore{BufStorage: store, clearErr: failure}
			if mode == "load" {
				fs.loadErr = failure
			}
			c := newPollClient(t, fs, func(*http.Request) (*http.Response, error) {
				if mode == "load" {
					t.Fatal("polled despite failed cursor load")
				}
				return pollJSON(`{"ret":-14}`), nil
			}, WithExpiredThreshold(1))
			if err := c.Start(context.Background()); !errors.Is(err, failure) {
				t.Fatal(err)
			}
		})
	}
}

func TestListenerWaitsForHandlersAndSnapshotsRegistration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		store := NewMemoryBufStorage()
		release := make(chan struct{})
		done := make(chan error, 1)
		calls := 0
		c := newPollClient(t, store, func(r *http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return pollJSON(onePollMessage), nil
			}
			<-r.Context().Done()
			return nil, r.Context().Err()
		})
		handled, replaced := 0, 0
		c.OnMessage(func(context.Context, *InboundMessage) error { handled++; <-release; return nil })
		go func() { done <- c.Start(ctx) }()
		synctest.Wait()
		if handled != 1 {
			t.Fatal("handler did not start")
		}
		if err := c.Start(ctx); !errors.Is(err, ErrAlreadyStarted) {
			t.Fatalf("duplicate Start: %v", err)
		}
		if cursor, _ := store.LoadBuf(ctx, "test-bot"); cursor != "" {
			t.Fatal("cursor committed before handler returned")
		}
		c.OnMessage(func(context.Context, *InboundMessage) error { replaced++; return nil })
		close(release)
		synctest.Wait()
		if cursor, _ := store.LoadBuf(ctx, "test-bot"); cursor != "next" {
			t.Fatalf("cursor=%q", cursor)
		}
		cancel()
		synctest.Wait()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if replaced != 0 {
			t.Fatal("registration changed an active listener")
		}
		c.OnMessage(nil)
		if err := c.Start(context.Background()); !errors.Is(err, ErrNoMessageHandler) {
			t.Fatalf("Start state did not reset: %v", err)
		}
	})
}

func TestListenerCancelWaitsAndPreservesCursor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		store := NewMemoryBufStorage()
		release := make(chan struct{})
		finished := make(chan error, 1)
		c := newPollClient(t, store, func(*http.Request) (*http.Response, error) { return pollJSON(onePollMessage), nil })
		c.OnMessage(func(context.Context, *InboundMessage) error { <-release; return nil })
		go func() { finished <- c.Start(ctx) }()
		synctest.Wait()
		cancel()
		synctest.Wait()
		select {
		case err := <-finished:
			t.Fatalf("returned with handler running: %v", err)
		default:
		}
		close(release)
		synctest.Wait()
		if err := <-finished; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if cursor, _ := store.LoadBuf(context.Background(), "test-bot"); cursor != "" {
			t.Fatal("canceled batch committed")
		}
	})
}

func TestListenerCancellationDuringBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		c := newPollClient(t, nil, func(*http.Request) (*http.Response, error) { calls++; return pollJSON(`{"ret":-1}`), nil })
		done := make(chan error, 1)
		go func() { done <- c.Start(ctx) }()
		synctest.Wait()
		cancel()
		synctest.Wait()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if calls != 1 {
			t.Fatalf("requests during canceled backoff: %d", calls)
		}
	})
}

func TestListenerBoundedConcurrencyAndUserOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var messages []map[string]any
		for i := 0; i < 3; i++ {
			for _, user := range []string{"a", "b", "c", "d"} {
				messages = append(messages, map[string]any{"message_id": i + 1, "message_type": 1, "message_state": 2, "from_user_id": user, "context_token": user + "-token", "item_list": []any{map[string]any{"type": 1, "text_item": map[string]string{"text": user}}}})
			}
		}
		body, err := json.Marshal(map[string]any{"ret": 0, "get_updates_buf": "done", "msgs": messages})
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		c := newPollClient(t, nil, func(*http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return pollJSON(string(body)), nil
			}
			cancel()
			return nil, ctx.Err()
		}, WithHandlerConcurrency(2))
		var active, maxActive atomic.Int32
		var mu sync.Mutex
		orders := map[string][]string{}
		inFlight := map[string]bool{}
		c.OnMessage(func(_ context.Context, m *InboundMessage) error {
			n := active.Add(1)
			defer active.Add(-1)
			for old := maxActive.Load(); n > old && !maxActive.CompareAndSwap(old, n); old = maxActive.Load() {
			}
			mu.Lock()
			if inFlight[m.FromUserID] {
				t.Error("same user processed concurrently")
			}
			inFlight[m.FromUserID] = true
			mu.Unlock()
			if c.ContextToken(m.FromUserID) != m.ContextToken {
				t.Error("conversation token not available to handler")
			}
			time.Sleep(time.Second)
			mu.Lock()
			orders[m.FromUserID] = append(orders[m.FromUserID], m.MessageID)
			inFlight[m.FromUserID] = false
			mu.Unlock()
			return nil
		})
		if err := c.Start(ctx); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if maxActive.Load() != 2 {
			t.Fatalf("max concurrent handlers=%d", maxActive.Load())
		}
		for user, order := range orders {
			if !reflect.DeepEqual(order, []string{"1", "2", "3"}) {
				t.Fatalf("%s order=%v", user, order)
			}
		}
		if len(orders) != 4 {
			t.Fatalf("processed users=%d", len(orders))
		}
	})
}

func TestListenerPreservesRawAndUnsignedIDs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := `{"ret":0,"msgs":[{"message_id":18446744073709551615,"message_type":1,"message_state":2,"from_user_id":"u","future_field":{"preserved":true},"item_list":[{"type":3,"voice_item":{}},{"type":4,"file_item":{"file_name":"x"}},{"type":5,"video_item":{}},{"type":99}]}]}`
	calls, handled := 0, 0
	c := newPollClient(t, nil, func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return pollJSON(body), nil
		}
		cancel()
		return nil, ctx.Err()
	})
	c.OnMessage(func(_ context.Context, m *InboundMessage) error {
		handled++
		if m.MessageID != "18446744073709551615" {
			t.Fatalf("message ID truncated: %s", m.MessageID)
		}
		if !strings.Contains(string(m.RawMessage), `"future_field":{"preserved":true}`) {
			t.Fatal("unknown wire data lost")
		}
		return nil
	})
	if err := c.Start(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if handled != 4 {
		t.Fatalf("items dropped: received %d", handled)
	}
}

func TestConcurrentRepliesRetainTokenAfterCacheEviction(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ready := make(chan struct{})
		var entered atomic.Int32
		var mu sync.Mutex
		sent := map[string]string{}
		polls := 0
		body := `{"ret":0,"get_updates_buf":"next","msgs":[{"message_type":1,"message_state":2,"from_user_id":"alice","context_token":"alice-token","item_list":[{"type":1,"text_item":{"text":"a"}}]},{"message_type":1,"message_state":2,"from_user_id":"bob","context_token":"bob-token","item_list":[{"type":1,"text_item":{"text":"b"}}]}]}`
		c := newPollClient(t, nil, func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/ilink/bot/sendmessage" {
				var req wechatSendRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				mu.Lock()
				sent[req.Msg.ToUserID] = req.Msg.ContextToken
				mu.Unlock()
				return pollJSON(`{"ret":0}`), nil
			}
			polls++
			if polls == 1 {
				return pollJSON(body), nil
			}
			cancel()
			return nil, ctx.Err()
		}, WithHandlerConcurrency(2), func(cfg *Config) { cfg.MaxContextTokens = 1 })
		c.OnMessage(func(ctx context.Context, m *InboundMessage) error {
			if entered.Add(1) == 2 {
				close(ready)
			}
			<-ready
			return c.SendText(ctx, m.FromUserID, "reply")
		})
		if err := c.Start(ctx); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(sent, map[string]string{"alice": "alice-token", "bob": "bob-token"}) {
			t.Fatalf("tokens lost during concurrent reply: %v", sent)
		}
	})
}
