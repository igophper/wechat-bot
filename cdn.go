package wechat

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// UploadToCDN uploads file data to the WeChat CDN and prepares encrypted media references.
// MaxMediaBytes limits the plaintext size and MediaTimeout covers the entire upload.
func (c *Client) UploadToCDN(ctx context.Context, toUserID string, data []byte, mediaType CDNMediaType) (*UploadedFile, error) {
	if int64(len(data)) > c.cfg.MaxMediaBytes {
		return nil, ErrMediaTooLarge
	}
	mediaCtx, cancel := context.WithTimeout(ctx, c.cfg.MediaTimeout)
	defer cancel()

	filekey, err := GenerateRandomBytes(16)
	if err != nil {
		return nil, fmt.Errorf("generate filekey: %w", err)
	}
	aeskey, err := GenerateRandomBytes(16)
	if err != nil {
		return nil, fmt.Errorf("generate aeskey: %w", err)
	}

	filekeyHex := hex.EncodeToString(filekey)
	aeskeyHex := hex.EncodeToString(aeskey)
	hash := md5.Sum(data)
	cipherSize := AESECBPaddedSize(len(data))

	upReq := wechatGetUploadURLRequest{
		FileKey:     filekeyHex,
		MediaType:   int(mediaType),
		ToUserID:    toUserID,
		RawSize:     len(data),
		RawFileMD5:  hex.EncodeToString(hash[:]),
		FileSize:    cipherSize,
		NoNeedThumb: true,
		AESKey:      aeskeyHex,
		BaseInfo:    wechatBaseInfo{ChannelVersion: Version},
	}

	var upResp wechatGetUploadURLResponse
	if err := c.doPost(mediaCtx, "/ilink/bot/getuploadurl", upReq, &upResp); err != nil {
		return nil, fmt.Errorf("getuploadurl: %w", err)
	}
	if upResp.Ret != 0 {
		return nil, &APIError{Endpoint: "/ilink/bot/getuploadurl", Ret: upResp.Ret, ErrMsg: upResp.ErrMsg}
	}

	encrypted, err := AESECBEncrypt(data, aeskey)
	if err != nil {
		return nil, fmt.Errorf("encrypt media: %w", err)
	}

	cdnURL := strings.TrimSpace(upResp.UploadFullURL)
	if cdnURL == "" {
		if upResp.UploadParam == "" {
			return nil, fmt.Errorf("getuploadurl returned empty upload URL")
		}
		cdnURL = fmt.Sprintf("%s/upload?encrypted_query_param=%s&filekey=%s",
			strings.TrimRight(c.cfg.CDNBaseURL, "/"), url.QueryEscape(upResp.UploadParam), url.QueryEscape(filekeyHex))
	}
	// The server picks this URL, so vet it before sending encrypted bytes to it.
	if err := c.mediaPolicy.checkString(cdnURL); err != nil {
		return nil, err
	}

	downloadParam, err := c.uploadCDNBytes(mediaCtx, encrypted, cdnURL)
	if err != nil {
		return nil, fmt.Errorf("upload to cdn: %w", err)
	}

	return &UploadedFile{
		DownloadParam: downloadParam,
		AESKeyHex:     aeskeyHex,
		FileSize:      len(data),
		CipherSize:    cipherSize,
	}, nil
}

// Retrying the same encrypted bytes at the already allocated file key is safe.
// Message submission is deliberately outside this retry boundary.
func (c *Client) uploadCDNBytes(ctx context.Context, encrypted []byte, cdnURL string) (string, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		param, err := c.uploadCDNBytesOnce(ctx, encrypted, cdnURL)
		if err == nil {
			return param, nil
		}
		lastErr = err
		var httpErr *HTTPError
		if (errors.As(err, &httpErr) && httpErr.StatusCode >= 400 && httpErr.StatusCode < 500) ||
			errors.Is(err, ErrResponseTooLarge) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", err
		}
		if attempt < 2 {
			timer := time.NewTimer(time.Duration(1<<attempt) * 100 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return "", ctx.Err()
			case <-timer.C:
			}
		}
	}
	return "", lastErr
}

func (c *Client) uploadCDNBytesOnce(ctx context.Context, encrypted []byte, cdnURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cdnURL, bytes.NewReader(encrypted))
	if err != nil {
		return "", transportError(err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := c.mediaClient.Do(req)
	if err != nil {
		return "", transportError(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", &HTTPError{Endpoint: "cdn upload", StatusCode: resp.StatusCode}
	}
	if _, err := readLimitedBody(resp.Body, c.cfg.MaxResponseBytes); err != nil {
		return "", transportError(err)
	}
	downloadParam := resp.Header.Get("X-Encrypted-Param")
	if downloadParam == "" {
		return "", fmt.Errorf("missing X-Encrypted-Param header in CDN response")
	}
	return downloadParam, nil
}

// downloadAndDecryptImage accepts both media key encodings and plaintext images.
func (c *Client) downloadAndDecryptImage(ctx context.Context, img *ImageItem) ([]byte, string, error) {
	if img == nil || img.Media == nil {
		return nil, "", ErrNoMediaInfo
	}
	cdnURL := strings.TrimSpace(img.Media.FullURL)
	if cdnURL == "" {
		if img.Media.EncryptQueryParam == "" {
			return nil, "", ErrNoMediaInfo
		}
		cdnURL = fmt.Sprintf("%s/download?encrypted_query_param=%s",
			strings.TrimRight(c.cfg.CDNBaseURL, "/"), url.QueryEscape(img.Media.EncryptQueryParam))
	}
	if err := c.mediaPolicy.checkString(cdnURL); err != nil {
		return nil, "", err
	}
	aesKey, err := imageAESKey(img)
	if err != nil {
		return nil, "", err
	}

	downloadCtx, cancel := context.WithTimeout(ctx, c.cfg.MediaTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(downloadCtx, http.MethodGet, cdnURL, nil)
	if err != nil {
		return nil, "", transportError(err)
	}
	resp, err := c.mediaClient.Do(req)
	if err != nil {
		return nil, "", transportError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, "", &HTTPError{Endpoint: "cdn download", StatusCode: resp.StatusCode}
	}

	// Ciphertext may include one extra block of padding beyond the plaintext limit.
	bodyLimit := c.cfg.MaxMediaBytes
	if len(aesKey) != 0 && bodyLimit <= math.MaxInt64-aes.BlockSize {
		bodyLimit += aes.BlockSize
	}
	if resp.ContentLength > bodyLimit {
		return nil, "", ErrMediaTooLarge
	}
	data, err := readLimitedBody(resp.Body, bodyLimit)
	if errors.Is(err, ErrResponseTooLarge) {
		return nil, "", ErrMediaTooLarge
	}
	if err != nil {
		return nil, "", fmt.Errorf("read CDN response: %w", transportError(err))
	}
	if len(aesKey) != 0 {
		data, err = AESECBDecrypt(data, aesKey)
		if err != nil {
			return nil, "", fmt.Errorf("decrypt media: %w", err)
		}
	}
	if int64(len(data)) > c.cfg.MaxMediaBytes {
		return nil, "", ErrMediaTooLarge
	}
	return data, http.DetectContentType(data), nil
}

func imageAESKey(img *ImageItem) ([]byte, error) {
	if img.AESKey != "" {
		key, err := hex.DecodeString(img.AESKey)
		if err != nil || len(key) != aes.BlockSize {
			return nil, fmt.Errorf("invalid image aeskey: expected a 32-character hexadecimal key")
		}
		return key, nil
	}
	if img.Media.AESKey == "" {
		return nil, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(img.Media.AESKey)
	if err != nil {
		return nil, fmt.Errorf("invalid media aes_key: expected base64")
	}
	if len(decoded) == aes.BlockSize {
		return decoded, nil
	}
	if len(decoded) == 2*aes.BlockSize {
		key, err := hex.DecodeString(string(decoded))
		if err == nil {
			return key, nil
		}
	}
	return nil, fmt.Errorf("invalid media aes_key: expected 16 raw bytes or 32 hexadecimal characters")
}

// downloadAndDecryptImageDataURL downloads, decrypts, and formats as a base64 Data URL.
func (c *Client) downloadAndDecryptImageDataURL(ctx context.Context, img *ImageItem) (string, error) {
	data, contentType, err := c.downloadAndDecryptImage(ctx, img)
	if err != nil {
		return "", err
	}
	b64 := base64.StdEncoding.EncodeToString(data)
	return fmt.Sprintf("data:%s;base64,%s", contentType, b64), nil
}
