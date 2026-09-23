package wechat

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientConfigValidation(t *testing.T) {
	// Missing bot token
	_, err := NewClient(Config{ILinkBotID: "bot123"})
	if err == nil {
		t.Fatal("expected error when BotToken is empty")
	}

	// Missing bot ID
	_, err = NewClient(Config{BotToken: "token123"})
	if err == nil {
		t.Fatal("expected error when ILinkBotID is empty")
	}

	// Valid config
	client, err := NewClient(Config{
		BotToken:   "token123",
		ILinkBotID: "bot123",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.BotID() != "bot123" {
		t.Fatalf("expected bot ID bot123, got %s", client.BotID())
	}
	if client.BaseURL() != DefaultBaseURL {
		t.Fatalf("expected default base URL %s, got %s", DefaultBaseURL, client.BaseURL())
	}
}

func TestSendTextMessage(t *testing.T) {
	var receivedReq wechatSendRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ilink/bot/sendmessage" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("AuthorizationType") != "ilink_bot_token" {
			http.Error(w, "bad auth type", http.StatusBadRequest)
			return
		}
		if r.Header.Get("X-WECHAT-UIN") == "" {
			http.Error(w, "missing uin", http.StatusBadRequest)
			return
		}

		_ = json.NewDecoder(r.Body).Decode(&receivedReq)
		_ = json.NewEncoder(w).Encode(wechatSendResponse{Ret: 0})
	}))
	defer server.Close()

	client, err := NewClient(Config{
		BotToken:   "test-token",
		ILinkBotID: "test-bot@im.bot",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	ctx := context.Background()
	err = client.SendText(ctx, "user_123", "Hello from test")
	if err != nil {
		t.Fatalf("SendText failed: %v", err)
	}

	if receivedReq.Msg.ToUserID != "user_123" {
		t.Fatalf("expected ToUserID user_123, got %s", receivedReq.Msg.ToUserID)
	}
	if receivedReq.Msg.FromUserID != "test-bot@im.bot" {
		t.Fatalf("expected FromUserID test-bot@im.bot, got %s", receivedReq.Msg.FromUserID)
	}
	if len(receivedReq.Msg.ItemList) == 0 || receivedReq.Msg.ItemList[0].TextItem.Text != "Hello from test" {
		t.Fatalf("expected message item 'Hello from test', got %+v", receivedReq.Msg.ItemList)
	}
}

func TestSendTyping(t *testing.T) {
	var configCalled, typingCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/getconfig":
			configCalled = true
			_ = json.NewEncoder(w).Encode(wechatGetConfigResponse{
				Ret:          0,
				TypingTicket: "ticket_xyz",
			})
		case "/ilink/bot/sendtyping":
			typingCalled = true
			var body wechatSendTypingRequest
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.TypingTicket != "ticket_xyz" {
				http.Error(w, "wrong ticket", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(wechatSendTypingResponse{Ret: 0})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient(Config{
		BotToken:   "test-token",
		ILinkBotID: "test-bot@im.bot",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	ctx := context.Background()
	if err := client.SendTyping(ctx, "user_123"); err != nil {
		t.Fatalf("SendTyping failed: %v", err)
	}
	if !configCalled || !typingCalled {
		t.Fatalf("expected both getconfig and sendtyping called, got config=%v, typing=%v", configCalled, typingCalled)
	}
}

func TestQRLoginFlow(t *testing.T) {
	var pollCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/get_bot_qrcode":
			_ = json.NewEncoder(w).Encode(QRCodeResult{
				QRCode:           "qr_token_abc",
				QRCodeImgContent: "data:image/png;base64,mock",
			})
		case "/ilink/bot/get_qrcode_status":
			count := atomic.AddInt32(&pollCount, 1)
			switch count {
			case 1:
				_ = json.NewEncoder(w).Encode(QRStatusResult{Status: QRStatusWait})
			case 2:
				_ = json.NewEncoder(w).Encode(QRStatusResult{Status: QRStatusScanned})
			default:
				_ = json.NewEncoder(w).Encode(QRStatusResult{
					Status:      QRStatusConfirmed,
					BotToken:    "confirmed_token",
					ILinkBotID:  "bot_confirmed@im.bot",
					ILinkUserID: "operator_user",
					BaseURL:     "https://ilinkai.weixin.qq.com",
				})
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ctx := context.Background()
	qr, err := GetQRCode(ctx, WithLoginBaseURL(server.URL), WithLoginHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("GetQRCode failed: %v", err)
	}
	if qr.QRCode != "qr_token_abc" {
		t.Fatalf("unexpected qrcode: %s", qr.QRCode)
	}

	creds, err := WaitForQRConfirmation(
		ctx,
		qr.QRCode,
		10*time.Millisecond,
		2*time.Second,
		nil,
		WithLoginBaseURL(server.URL),
		WithLoginHTTPClient(server.Client()),
	)
	if err != nil {
		t.Fatalf("WaitForQRConfirmation failed: %v", err)
	}
	if creds.BotToken != "confirmed_token" || creds.ILinkBotID != "bot_confirmed@im.bot" {
		t.Fatalf("unexpected creds: %+v", creds)
	}
}

func TestMediaUploadAndSend(t *testing.T) {
	var uploadRequested, cdnUploaded, messageSent bool

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/getuploadurl":
			uploadRequested = true
			_ = json.NewEncoder(w).Encode(wechatGetUploadURLResponse{
				Ret:           0,
				UploadFullURL: server.URL + "/cdn/upload",
			})
		case "/cdn/upload":
			cdnUploaded = true
			w.Header().Set("X-Encrypted-Param", "enc_param_token_123")
			w.WriteHeader(http.StatusOK)
		case "/ilink/bot/sendmessage":
			messageSent = true
			var req wechatSendRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			if len(req.Msg.ItemList) > 0 && req.Msg.ItemList[0].Type == int(ItemTypeImage) {
				_ = json.NewEncoder(w).Encode(wechatSendResponse{Ret: 0})
			} else {
				http.Error(w, "missing image item", http.StatusBadRequest)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient(Config{
		BotToken:   "test-token",
		ILinkBotID: "test-bot@im.bot",
		BaseURL:    server.URL,
		CDNBaseURL: server.URL + "/cdn",
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	ctx := context.Background()
	err = client.SendImage(ctx, "user_123", []byte("fake_image_bytes"))
	if err != nil {
		t.Fatalf("SendImage failed: %v", err)
	}

	if !uploadRequested || !cdnUploaded || !messageSent {
		t.Fatalf("expected all legs completed: uploadReq=%v, cdnUp=%v, msgSent=%v",
			uploadRequested, cdnUploaded, messageSent)
	}
}

func TestPrintTerminalQR(t *testing.T) {
	var buf bytes.Buffer
	qr := &QRCodeResult{
		QRCode:           "123456789",
		QRCodeImgContent: "https://ilinkai.weixin.qq.com/test",
	}
	qr.PrintTerminal(&buf)

	if buf.Len() == 0 {
		t.Fatal("expected non-empty output from PrintTerminal")
	}

	// Test fallback when QRCodeImgContent is empty
	var buf2 bytes.Buffer
	qrFallback := &QRCodeResult{QRCode: "https://ilinkai.weixin.qq.com/fallback"}
	qrFallback.PrintTerminal(&buf2)
	if buf2.Len() == 0 {
		t.Fatal("expected non-empty output from PrintTerminal fallback")
	}
}
