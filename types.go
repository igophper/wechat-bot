package wechat

import (
	"context"
	"encoding/json"
	"time"
)

// Default protocol constants.
const (
	DefaultBaseURL    = "https://ilinkai.weixin.qq.com"
	DefaultCDNBaseURL = "https://novac2c.cdn.weixin.qq.com/c2c"

	DefaultLongPollTimeout = 35 * time.Second
	DefaultSendTimeout     = 15 * time.Second
	DefaultMediaTimeout    = 90 * time.Second
	DefaultTypingTimeout   = 8 * time.Second

	DefaultBackoffInitial = 3 * time.Second
	DefaultBackoffMax     = 60 * time.Second

	// DefaultEmptyBufExpiredThreshold is the threshold of consecutive empty-buf
	// SessionExpired responses before asking the caller to reauthenticate.
	DefaultEmptyBufExpiredThreshold       = 20
	DefaultMaxResponseBytes         int64 = 1 << 20
	DefaultMaxMediaBytes            int64 = 32 << 20
	DefaultMaxContextTokens               = 1024

	// DefaultVerificationAttempts is how many verification codes a QR login
	// submits before giving up with ErrVerificationAttemptsExhausted.
	DefaultVerificationAttempts = 3
)

// Server error codes.
const (
	ErrCodeSessionExpired = -14 // Server-side sync token rotated or expired
)

// Message types.
const (
	MsgTypeUser = 1
	MsgTypeBot  = 2
)

// Message states.
const (
	MsgStateFinish = 2
)

// ItemType identifies the content of an inbound or outbound item.
type ItemType int

// Supported message item types.
const (
	ItemTypeText  ItemType = 1
	ItemTypeImage ItemType = 2
	ItemTypeVoice ItemType = 3
	ItemTypeFile  ItemType = 4
	ItemTypeVideo ItemType = 5
)

// CDNMediaType identifies an upload media category.
type CDNMediaType int

// Supported CDN upload categories.
const (
	CDNMediaTypeImage CDNMediaType = 1
	CDNMediaTypeVideo CDNMediaType = 2
	CDNMediaTypeFile  CDNMediaType = 3
)

// Encryption types.
const (
	EncryptTypeAES128ECB = 1
)

// Typing statuses.
const (
	TypingStatusTyping = 1
	TypingStatusCancel = 2
)

// QR Code login status values.
const (
	QRStatusWait              = "wait"
	QRStatusScanned           = "scaned"
	QRStatusConfirmed         = "confirmed"
	QRStatusExpired           = "expired"
	QRStatusNeedVerifyCode    = "need_verifycode"
	QRStatusVerifyCodeBlocked = "verify_code_blocked"
	QRStatusRedirect          = "scaned_but_redirect"
	QRStatusBoundRedirect     = "binded_redirect"
)

// Credentials holds WeChat bot authentication parameters obtained via QR login.
type Credentials struct {
	BotToken    string `json:"bot_token"`
	ILinkBotID  string `json:"ilink_bot_id"`
	BaseURL     string `json:"baseurl,omitempty"`
	ILinkUserID string `json:"ilink_user_id,omitempty"`
}

// QRCodeResult holds the QR code token and rendered image data.
type QRCodeResult struct {
	QRCode           string `json:"qrcode"`
	QRCodeImgContent string `json:"qrcode_img_content"` // Base64 data or URL
}

// QRStatusResult represents the current status of a QR scan session.
type QRStatusResult struct {
	Status       string `json:"status"`
	RedirectHost string `json:"redirect_host,omitempty"`
	BotToken     string `json:"bot_token,omitempty"`
	ILinkBotID   string `json:"ilink_bot_id,omitempty"`
	BaseURL      string `json:"baseurl,omitempty"`
	ILinkUserID  string `json:"ilink_user_id,omitempty"`
}

// InboundMessage represents a received message from WeChat.
type InboundMessage struct {
	MessageID  string     `json:"message_id"`
	FromUserID string     `json:"from_user_id"`
	ToUserID   string     `json:"to_user_id"`
	Text       string     `json:"text"`
	ItemType   ItemType   `json:"item_type"`
	VoiceText  string     `json:"voice_text,omitempty"` // Voice STT transcription
	ImageItem  *ImageItem `json:"image_item,omitempty"`
	VoiceItem  *VoiceItem `json:"voice_item,omitempty"`
	FileItem   *FileItem  `json:"file_item,omitempty"`
	VideoItem  *VideoItem `json:"video_item,omitempty"`
	// RawMessage preserves the original JSON. Items in one message share this
	// read-only data; clone it before modifying or passing it to a mutating API.
	RawMessage   json.RawMessage `json:"raw_message,omitempty"`
	ContextToken string          `json:"context_token,omitempty"`

	client *Client
}

// IsText returns true if message contains text content.
func (m *InboundMessage) IsText() bool {
	return m.Text != "" && m.ItemType == ItemTypeText
}

// IsImage returns true if message contains an image.
func (m *InboundMessage) IsImage() bool {
	return m.ItemType == ItemTypeImage
}

// IsVoice reports whether the message is a voice note; transcription may be empty.
func (m *InboundMessage) IsVoice() bool {
	return m.ItemType == ItemTypeVoice
}

// DownloadDecryptedImage downloads and decrypts the image bytes if this message contains an image.
func (m *InboundMessage) DownloadDecryptedImage(ctx context.Context) ([]byte, string, error) {
	if m.client == nil || m.ImageItem == nil {
		return nil, "", ErrNoMediaInfo
	}
	return m.client.downloadAndDecryptImage(ctx, m.ImageItem)
}

// DownloadDecryptedImageDataURL returns a base64 Data URL (e.g. "data:image/png;base64,...") of the image.
func (m *InboundMessage) DownloadDecryptedImageDataURL(ctx context.Context) (string, error) {
	if m.client == nil || m.ImageItem == nil {
		return "", ErrNoMediaInfo
	}
	return m.client.downloadAndDecryptImageDataURL(ctx, m.ImageItem)
}

// MessageHandler processes one inbound item. A non-nil error stops Start without
// committing the batch cursor. Handlers must honor ctx and tolerate replay.
type MessageHandler func(ctx context.Context, msg *InboundMessage) error

// ExpiredHandler is called after the session recovery threshold is exhausted.
// This is a reauthentication signal, not proof of permanent token revocation.
type ExpiredHandler func(accountID string)

// VideoItem describes an inbound or outbound video.
type VideoItem struct {
	Media     *MediaInfo `json:"media,omitempty"`
	VideoSize int        `json:"video_size,omitempty"` // ciphertext size
}

// FileItem describes an inbound or outbound file.
type FileItem struct {
	Media    *MediaInfo `json:"media,omitempty"`
	FileName string     `json:"file_name,omitempty"`
	Len      string     `json:"len,omitempty"` // plaintext size as string
}

// VoiceItem describes a voice note and its optional transcription.
type VoiceItem struct {
	Media    *MediaInfo `json:"media,omitempty"`
	Text     string     `json:"text,omitempty"`     // STT transcription
	Playtime int        `json:"playtime,omitempty"` // ms
}

// ImageItem describes an image; AESKey is an optional hexadecimal key.
type ImageItem struct {
	AESKey  string     `json:"aeskey,omitempty"`
	URL     string     `json:"url,omitempty"`
	Media   *MediaInfo `json:"media,omitempty"`
	MidSize int        `json:"mid_size,omitempty"`
}

// MediaInfo holds a CDN reference and its optional decryption key.
type MediaInfo struct {
	EncryptQueryParam string `json:"encrypt_query_param"`
	AESKey            string `json:"aes_key"` // base64(raw key) or base64(hex key)
	FullURL           string `json:"full_url,omitempty"`
	EncryptType       int    `json:"encrypt_type"`
}

// UploadedFile holds the encrypted CDN reference returned by UploadToCDN.
type UploadedFile struct {
	DownloadParam string
	AESKeyHex     string
	FileSize      int
	CipherSize    int
}
