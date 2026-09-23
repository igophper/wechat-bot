package wechat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestCredentialsReplacementRestrictsExistingPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows file protection uses ACLs rather than Unix permission bits")
	}
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(path, []byte(`{"bot_token":"old","ilink_bot_id":"bot"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := SaveCredentials(path, &Credentials{BotToken: "replacement", ILinkBotID: "bot"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("replacement permission=%#o, want 0600", got)
	}
	creds, err := LoadCredentials(path)
	if err != nil || creds == nil || creds.BotToken != "replacement" {
		t.Fatalf("replacement credentials=%+v err=%v", creds, err)
	}
	assertNoStorageTempFiles(t, filepath.Dir(path))
}

func TestCredentialsRepeatedWritesStayComplete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	for i := 0; i < 10; i++ {
		want := &Credentials{BotToken: fmt.Sprintf("token-%d", i), ILinkBotID: "bot", ILinkUserID: "user", BaseURL: "https://example.com"}
		if err := SaveCredentials(path, want); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var got Credentials
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("partial JSON after save: %v", err)
		}
		if got != *want {
			t.Fatalf("credentials=%+v want=%+v", got, want)
		}
		assertNoStorageTempFiles(t, dir)
	}
}

func TestCredentialsReadersNeverObservePartialReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("atomic rename is not guaranteed by Go on Windows filesystems")
	}
	path := filepath.Join(t.TempDir(), "credentials.json")
	makeCreds := func(i int) *Credentials {
		id := fmt.Sprintf("bot-%d", i)
		return &Credentials{BotToken: id + ":" + strings.Repeat("x", 8192), ILinkBotID: id}
	}
	if err := SaveCredentials(path, makeCreds(0)); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	readErrors := make(chan error, 4)
	var readers sync.WaitGroup
	var count atomic.Int32
	for i := 0; i < 4; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				creds, err := LoadCredentials(path)
				if err != nil {
					readErrors <- err
					return
				}
				if creds == nil || creds.BotToken != creds.ILinkBotID+":"+strings.Repeat("x", 8192) {
					readErrors <- fmt.Errorf("reader observed incomplete credentials")
					return
				}
				count.Add(1)
			}
		}()
	}
	var writeErr error
	for i := 1; i <= 20; i++ {
		if err := SaveCredentials(path, makeCreds(i)); err != nil {
			writeErr = err
			break
		}
	}
	close(stop)
	readers.Wait()
	close(readErrors)
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	for err := range readErrors {
		t.Error(err)
	}
	if count.Load() == 0 {
		t.Fatal("no concurrent reads completed")
	}
	assertNoStorageTempFiles(t, filepath.Dir(path))
}

func TestFailedCredentialSavePreservesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	original := &Credentials{BotToken: "original-token", ILinkBotID: "bot"}
	if err := SaveCredentials(path, original); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []*Credentials{nil, {}, {BotToken: "missing-bot"}, {ILinkBotID: "missing-token"}} {
		if err := SaveCredentials(path, invalid); !errors.Is(err, ErrCredentialsRequired) {
			t.Fatalf("invalid credentials error=%v", err)
		}
		got, err := LoadCredentials(path)
		if err != nil || got == nil || *got != *original {
			t.Fatalf("failed save changed old credentials: %+v, %v", got, err)
		}
	}
	assertNoStorageTempFiles(t, dir)
}

func TestFailedReplacementRemovesTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "occupied")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(path, "existing")
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SaveCredentials(path, &Credentials{BotToken: "token", ILinkBotID: "bot"}); err == nil {
		t.Fatal("replaced a nonempty directory")
	}
	got, err := os.ReadFile(marker)
	if err != nil || string(got) != "keep" {
		t.Fatalf("failed replacement changed old data: %q, %v", got, err)
	}
	assertNoStorageTempFiles(t, dir)
}

func TestCanceledReplacementPreservesOldFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancel at the final check after writing the temp file, before replacement.
	cancelCtx := &cancelOnSecondCheckContext{Context: ctx, cancel: cancel}
	if err := writePrivateFile(cancelCtx, path, []byte("replacement")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled replacement error=%v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "original" {
		t.Fatalf("canceled replacement changed old file: %q, %v", got, err)
	}
	assertNoStorageTempFiles(t, dir)
}

func TestCanceledCursorOperationsDoNotModifyStorage(t *testing.T) {
	for _, tc := range []struct {
		name    string
		storage BufStorage
	}{
		{name: "memory zero value", storage: &MemoryBufStorage{}},
		{name: "file", storage: NewFileBufStorage(t.TempDir())},
	} {
		t.Run(tc.name, func(t *testing.T) {
			background := context.Background()
			if err := tc.storage.SaveBuf(background, "bot", "original"); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(background)
			cancel()
			if err := tc.storage.SaveBuf(ctx, "bot", "changed"); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled save error=%v", err)
			}
			if err := tc.storage.ClearBuf(ctx, "bot"); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled clear error=%v", err)
			}
			if _, err := tc.storage.LoadBuf(ctx, "bot"); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled load error=%v", err)
			}
			got, err := tc.storage.LoadBuf(background, "bot")
			if err != nil || got != "original" {
				t.Fatalf("canceled operation mutated data: %q, %v", got, err)
			}
		})
	}
	dir := filepath.Join(t.TempDir(), "must-not-exist")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := NewFileBufStorage(dir).SaveBuf(ctx, "new", "cursor"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled save created a directory: %v", err)
	}
}

func TestCursorFilenamesAreSafeAndDistinct(t *testing.T) {
	dir := t.TempDir()
	storage := NewFileBufStorage(dir)
	ids := []string{"", ".", "..", "../outside", "a/b", "a%2Fb", "a\\b", "a:b", "a?b", "a_b", "Foo", "foo", "CON", "con", "con.txt", "LPT1", "lpt1", "NUL", "%", "消息", "bot@im.bot"}
	seen := make(map[string]string)
	for i, id := range ids {
		path := storage.pathForAccount(id)
		if filepath.Dir(path) != dir {
			t.Fatalf("account %q escaped state directory: %s", id, path)
		}
		folded := strings.ToLower(filepath.Base(path))
		if previous, exists := seen[folded]; exists {
			t.Fatalf("case-insensitive filename collision: %q and %q", previous, id)
		}
		seen[folded] = id
		if err := storage.SaveBuf(context.Background(), id, fmt.Sprintf("cursor-%d", i)); err != nil {
			t.Fatalf("account %q: %v", id, err)
		}
	}
	for i, id := range ids {
		got, err := storage.LoadBuf(context.Background(), id)
		if err != nil || got != fmt.Sprintf("cursor-%d", i) {
			t.Fatalf("account %q returned %q, %v", id, got, err)
		}
	}
	if got := storage.pathForAccount("4090de018d12@im.bot"); got != filepath.Join(dir, "4090de018d12@im.bot.json") {
		t.Fatalf("ordinary existing bot path changed: %s", got)
	}
	assertNoStorageTempFiles(t, dir)
}

func TestMemoryBufStorageZeroValueConcurrentUse(t *testing.T) {
	var storage MemoryBufStorage
	if got, err := storage.LoadBuf(context.Background(), "missing"); got != "" || err != nil {
		t.Fatalf("zero-value read=%q %v", got, err)
	}
	if err := storage.ClearBuf(context.Background(), "missing"); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			id := fmt.Sprintf("bot-%d", worker)
			for i := 0; i < 30; i++ {
				if err := storage.SaveBuf(context.Background(), id, "cursor"); err != nil {
					t.Error(err)
					return
				}
				if got, err := storage.LoadBuf(context.Background(), id); err != nil || got != "cursor" {
					t.Errorf("concurrent read=%q %v", got, err)
					return
				}
				if err := storage.ClearBuf(context.Background(), id); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	workers.Wait()
}

func assertNoStorageTempFiles(t *testing.T, dir string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, ".wechat-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("temporary state files remain: %v", files)
	}
}

type cancelOnSecondCheckContext struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (c *cancelOnSecondCheckContext) Err() error {
	c.checks++
	if c.checks == 2 {
		c.cancel()
	}
	return c.Context.Err()
}
