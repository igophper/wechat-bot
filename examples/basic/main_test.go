package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/igophper/wechat-bot"
)

func TestImmediateReplyDoesNotWaitForTyping(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sends := 0
		bot, err := wechat.NewClient(wechat.Config{
			BotToken: "test-token", ILinkBotID: "test-bot",
			HTTPClient: &http.Client{Transport: replyTransport(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/ilink/bot/getconfig" || req.URL.Path == "/ilink/bot/sendtyping" {
					// The optional indicator is unavailable; a text reply must not
					// wait for its eight-second timeout before it can be sent.
					<-req.Context().Done()
					return nil, req.Context().Err()
				}
				if req.URL.Path != "/ilink/bot/sendmessage" {
					t.Fatalf("unexpected request path: %s", req.URL.Path)
				}
				sends++
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ret":0}`))}, nil
			})},
		})
		if err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		err = reply(context.Background(), bot, &wechat.InboundMessage{MessageID: "1", FromUserID: "user", ItemType: wechat.ItemTypeText, Text: "ping"})
		if err != nil || sends != 1 {
			t.Fatalf("reply error=%v sends=%d", err, sends)
		}
		if elapsed := time.Since(started); elapsed >= time.Second {
			t.Fatalf("instant reply waited %v for an optional typing indicator", elapsed)
		}
	})
}

type replyTransport func(*http.Request) (*http.Response, error)

func (fn replyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}
