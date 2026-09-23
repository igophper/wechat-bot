package wechat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// BufStorage persists committed long-poll cursors. Implementations must be safe
// for concurrent use. A successful save must replace the old value completely.
// Use a separate account/consumer namespace for independent listeners.
type BufStorage interface {
	LoadBuf(ctx context.Context, accountID string) (string, error)
	SaveBuf(ctx context.Context, accountID string, buf string) error
	ClearBuf(ctx context.Context, accountID string) error
}

// MemoryBufStorage stores cursors in memory. Its zero value is ready to use.
type MemoryBufStorage struct {
	mu      sync.RWMutex
	buffers map[string]string
}

// NewMemoryBufStorage creates an in-memory cursor store.
func NewMemoryBufStorage() *MemoryBufStorage { return &MemoryBufStorage{} }

// LoadBuf returns the committed cursor, or an empty string if none exists.
func (s *MemoryBufStorage) LoadBuf(ctx context.Context, accountID string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.buffers[accountID], nil
}

// SaveBuf replaces the account's cursor.
func (s *MemoryBufStorage) SaveBuf(ctx context.Context, accountID string, buf string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.buffers == nil {
		s.buffers = make(map[string]string)
	}
	s.buffers[accountID] = buf
	return nil
}

// ClearBuf removes an account's cursor if it exists.
func (s *MemoryBufStorage) ClearBuf(ctx context.Context, accountID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.buffers, accountID)
	return nil
}

// FileBufStorage persists cursors using temporary-file replacement. Do not use
// multiple processes to poll the same account: this is not a distributed lock.
type FileBufStorage struct {
	baseDir string
	mu      sync.Mutex
}

type fileBufData struct {
	GetUpdatesBuf string `json:"get_updates_buf"`
}

// NewFileBufStorage creates a local cursor store. On Unix, newly created state
// directories use 0700 and files use 0600; Windows permissions depend on ACLs.
func NewFileBufStorage(baseDir string) *FileBufStorage { return &FileBufStorage{baseDir: baseDir} }

func (s *FileBufStorage) pathForAccount(accountID string) string {
	// Preserve existing ordinary bot IDs while encoding unsafe bytes injectively.
	// Percent itself is escaped so encoded names cannot collide with literal names.
	var safe strings.Builder
	for _, b := range []byte(accountID) {
		if b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '@' || b == '.' || b == '-' || b == '_' {
			safe.WriteByte(b)
		} else {
			fmt.Fprintf(&safe, "%%%02X", b)
		}
	}
	name := safe.String()
	if name == "" {
		name = "%"
	}
	// Escape the first byte of reserved Windows basenames without changing
	// ordinary existing bot filenames or introducing collisions with '%' inputs.
	stem := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	reserved := stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL"
	if len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9' {
		reserved = true
	}
	if reserved {
		name = fmt.Sprintf("%%%02X%s", name[0], name[1:])
	}
	return filepath.Join(s.baseDir, name+".json")
}

// LoadBuf reads a cursor or returns an empty string if it has never been saved.
func (s *FileBufStorage) LoadBuf(ctx context.Context, accountID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	data, err := os.ReadFile(s.pathForAccount(accountID))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var stored fileBufData
	if err := json.Unmarshal(data, &stored); err != nil {
		return "", err
	}
	return stored.GetUpdatesBuf, nil
}

// SaveBuf writes a complete new cursor file before replacing the old one.
func (s *FileBufStorage) SaveBuf(ctx context.Context, accountID string, buf string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(fileBufData{GetUpdatesBuf: buf})
	if err != nil {
		return err
	}
	return writePrivateFile(ctx, s.pathForAccount(accountID), data)
}

// ClearBuf deletes an account's cursor if present.
func (s *FileBufStorage) ClearBuf(ctx context.Context, accountID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	err := os.Remove(s.pathForAccount(accountID))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// SaveCredentials replaces the credentials file using a temporary file with
// 0600 permissions on Unix. On Windows, protect the directory using ACLs.
// Existing files with broader Unix permissions are replaced, not reused.
func SaveCredentials(filePath string, creds *Credentials) error {
	if creds == nil || creds.BotToken == "" || creds.ILinkBotID == "" {
		return ErrCredentialsRequired
	}
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateFile(context.Background(), filePath, data)
}

// LoadCredentials reads valid credentials. A missing file returns nil, nil;
// malformed JSON or incomplete credentials return an error.
func LoadCredentials(filePath string) (*Credentials, error) {
	data, err := os.ReadFile(filePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, err
	}
	if creds.BotToken == "" || creds.ILinkBotID == "" {
		return nil, ErrCredentialsRequired
	}
	return &creds, nil
}

func writePrivateFile(ctx context.Context, path string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".wechat-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	// CreateTemp creates 0600; explicit chmod also covers unusual umask behavior.
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Same-directory rename is atomic on Unix filesystems. Go does not guarantee
	// atomic rename on every non-Unix filesystem; this does not promise power-loss durability.
	return os.Rename(tmp.Name(), path)
}
