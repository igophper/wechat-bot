package wechat

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMemoryBufStorage(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryBufStorage()

	// Initially empty
	buf, err := s.LoadBuf(ctx, "bot1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buf != "" {
		t.Fatalf("expected empty buf, got %q", buf)
	}

	// Save and load
	if err := s.SaveBuf(ctx, "bot1", "cursor_token_123"); err != nil {
		t.Fatalf("save failed: %v", err)
	}
	buf, err = s.LoadBuf(ctx, "bot1")
	if err != nil || buf != "cursor_token_123" {
		t.Fatalf("expected cursor_token_123, got %q, err: %v", buf, err)
	}

	// Clear
	if err := s.ClearBuf(ctx, "bot1"); err != nil {
		t.Fatalf("clear failed: %v", err)
	}
	buf, err = s.LoadBuf(ctx, "bot1")
	if err != nil || buf != "" {
		t.Fatalf("expected empty buf after clear, got %q", buf)
	}
}

func TestFileBufStorage(t *testing.T) {
	ctx := context.Background()
	tmpDir, err := os.MkdirTemp("", "wechat_storage_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	s := NewFileBufStorage(tmpDir)
	botID := "user@bot/123"

	// Initial load should be empty
	buf, err := s.LoadBuf(ctx, botID)
	if err != nil || buf != "" {
		t.Fatalf("expected empty, got %q, err: %v", buf, err)
	}

	// Save and verify file created
	if err := s.SaveBuf(ctx, botID, "persisted_token_abc"); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	expectedFile := filepath.Join(tmpDir, "user@bot%2F123.json")
	if _, err := os.Stat(expectedFile); err != nil {
		t.Fatalf("expected storage file %s to exist: %v", expectedFile, err)
	}

	// Load again
	buf, err = s.LoadBuf(ctx, botID)
	if err != nil || buf != "persisted_token_abc" {
		t.Fatalf("expected persisted_token_abc, got %q, err: %v", buf, err)
	}

	// Clear and verify removed
	if err := s.ClearBuf(ctx, botID); err != nil {
		t.Fatalf("clear failed: %v", err)
	}
	buf, err = s.LoadBuf(ctx, botID)
	if err != nil || buf != "" {
		t.Fatalf("expected empty after clear, got %q", buf)
	}
}

func TestSaveAndLoadCredentials(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "wechat_creds_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	filePath := filepath.Join(tmpDir, "credentials.json")

	// Load non-existent
	creds, err := LoadCredentials(filePath)
	if err != nil || creds != nil {
		t.Fatalf("expected nil creds for missing file, got %v, err: %v", creds, err)
	}

	// Save
	original := &Credentials{
		BotToken:    "token_12345",
		ILinkBotID:  "bot_id_abc@im.bot",
		BaseURL:     "https://ilinkai.weixin.qq.com",
		ILinkUserID: "user_operator",
	}
	if err := SaveCredentials(filePath, original); err != nil {
		t.Fatalf("save credentials failed: %v", err)
	}

	// Load
	loaded, err := LoadCredentials(filePath)
	if err != nil {
		t.Fatalf("load credentials failed: %v", err)
	}
	if loaded.BotToken != original.BotToken || loaded.ILinkBotID != original.ILinkBotID ||
		loaded.BaseURL != original.BaseURL || loaded.ILinkUserID != original.ILinkUserID {
		t.Fatalf("loaded creds %+v do not match original %+v", loaded, original)
	}
}
