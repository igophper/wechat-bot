package wechat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestListenerTimingsSeparateMessageAgeAndHandlerTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var logs bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		c := newPollClient(t, NewMemoryBufStorage(), func(*http.Request) (*http.Response, error) {
			calls++
			if calls > 1 {
				cancel()
				return nil, ctx.Err()
			}
			return pollJSON(fmt.Sprintf(`{"ret":0,"get_updates_buf":"secret-cursor","msgs":[{"message_id":1,"create_time_ms":%d,"message_type":1,"message_state":2,"from_user_id":"secret-user","context_token":"secret-context","item_list":[{"type":1,"text_item":{"text":"secret-body"}}]}]}`, time.Now().Add(-10*time.Second).UnixMilli())), nil
		}, WithLogger(logger))
		c.OnMessage(func(context.Context, *InboundMessage) error {
			time.Sleep(2 * time.Second)
			return nil
		})
		if err := c.Start(ctx); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		var foundAge, foundHandler bool
		for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
			var entry map[string]any
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				t.Fatal(err)
			}
			switch entry["msg"] {
			case "wechat message dispatch":
				foundAge = true
				if entry["message_age"] != float64(10*time.Second) {
					t.Fatalf("incorrect message age: %v", entry)
				}
			case "wechat batch handled":
				foundHandler = true
				if entry["elapsed"] != float64(2*time.Second) || entry["success"] != true {
					t.Fatalf("incorrect handler timing: %v", entry)
				}
			}
		}
		if !foundAge || !foundHandler {
			t.Fatalf("missing diagnostic stages: %s", logs.String())
		}
		for _, secret := range []string{"secret-cursor", "secret-user", "secret-context", "secret-body", "test-token"} {
			if strings.Contains(logs.String(), secret) {
				t.Fatalf("diagnostics leaked %q", secret)
			}
		}
	})
}
