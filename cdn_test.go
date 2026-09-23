package wechat

import (
	"bytes"
	"context"
	"crypto/aes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newMediaTestClient(t *testing.T, opts ...Option) *Client {
	t.Helper()
	c, err := NewClient(Config{BotToken: "test-token", ILinkBotID: "bot"}, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestImageDownloadKeyFormats(t *testing.T) {
	key := []byte("0123456789abcdef")
	plain := []byte("image payload")
	ciphertext, err := AESECBEncrypt(plain, key)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/preferred" {
			t.Errorf("did not prefer full_url: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("API authorization leaked to CDN")
		}
		if r.URL.Query().Get("plain") == "true" {
			_, _ = w.Write(plain)
		} else {
			_, _ = w.Write(ciphertext)
		}
	}))
	defer server.Close()
	c := newMediaTestClient(t, WithHTTPClient(server.Client()), WithCDNBaseURL(server.URL))

	for _, tc := range []struct {
		name, imageKey, mediaKey string
		plaintext                bool
	}{
		{name: "raw base64", mediaKey: base64.StdEncoding.EncodeToString(key)},
		{name: "hex base64", mediaKey: base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString(key)))},
		{name: "image key takes precedence", imageKey: hex.EncodeToString(key), mediaKey: "invalid ignored key"},
		{name: "plaintext without key", plaintext: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fullURL := server.URL + "/preferred"
			if tc.plaintext {
				fullURL += "?plain=true"
			}
			wire, err := json.Marshal(map[string]any{
				"aeskey": tc.imageKey,
				"media":  map[string]any{"aes_key": tc.mediaKey, "full_url": fullURL, "encrypt_query_param": "must-not-use"},
			})
			if err != nil {
				t.Fatal(err)
			}
			var img ImageItem
			if err := json.Unmarshal(wire, &img); err != nil {
				t.Fatal(err)
			}
			got, mime, err := c.downloadAndDecryptImage(context.Background(), &img)
			if err != nil || !bytes.Equal(got, plain) || mime == "" {
				t.Fatalf("data=%q MIME=%q err=%v", got, mime, err)
			}
		})
	}
}

func TestImageDownloadEscapesFallbackParameter(t *testing.T) {
	param := "secret+param&/=other"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("encrypted_query_param") != param || len(r.URL.Query()) != 1 {
			t.Errorf("bad escaped parameter: %v", r.URL.Query())
		}
		_, _ = w.Write([]byte("image"))
	}))
	defer server.Close()
	c := newMediaTestClient(t, WithCDNBaseURL(server.URL+"/"))
	got, err := c.downloadAndDecryptImageDataURL(context.Background(), &ImageItem{Media: &MediaInfo{EncryptQueryParam: param}})
	if err != nil || !strings.HasSuffix(got, ";base64,aW1hZ2U=") {
		t.Fatalf("data URL=%q err=%v", got, err)
	}
}

func TestImageDownloadRejectsInvalidKeyAndPadding(t *testing.T) {
	key := []byte("0123456789abcdef")
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	invalid := make([]byte, aes.BlockSize)
	block.Encrypt(invalid, make([]byte, aes.BlockSize))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(invalid)
	}))
	defer server.Close()
	c := newMediaTestClient(t, WithCDNBaseURL(server.URL))
	for _, img := range []*ImageItem{
		{AESKey: "not-hex", Media: &MediaInfo{FullURL: server.URL}},
		{AESKey: "abcd", Media: &MediaInfo{FullURL: server.URL}},
		{Media: &MediaInfo{AESKey: "not-base64", FullURL: server.URL}},
		{Media: &MediaInfo{AESKey: base64.StdEncoding.EncodeToString([]byte("bad key")), FullURL: server.URL}},
	} {
		if _, _, err := c.downloadAndDecryptImage(context.Background(), img); err == nil {
			t.Error("accepted invalid key")
		}
	}
	img := &ImageItem{AESKey: hex.EncodeToString(key), Media: &MediaInfo{FullURL: server.URL}}
	if got, _, err := c.downloadAndDecryptImage(context.Background(), img); got != nil || !errors.Is(err, ErrInvalidPadding) {
		t.Fatalf("corrupted ciphertext returned %x, %v", got, err)
	}
	if _, _, err := c.downloadAndDecryptImage(context.Background(), &ImageItem{}); !errors.Is(err, ErrNoMediaInfo) {
		t.Fatalf("missing media error=%v", err)
	}
}

func TestMediaSizeLimits(t *testing.T) {
	key := []byte("0123456789abcdef")
	for _, tc := range []struct {
		name               string
		size               int
		encrypted, chunked bool
		wantErr            bool
	}{
		{name: "plain at limit", size: 16},
		{name: "plain oversized", size: 17, wantErr: true},
		{name: "chunked oversized", size: 17, chunked: true, wantErr: true},
		{name: "encrypted full padding block", size: 16, encrypted: true},
		{name: "encrypted oversized plaintext", size: 17, encrypted: true, wantErr: true},
		{name: "encrypted oversized ciphertext", size: 33, encrypted: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := bytes.Repeat([]byte("a"), tc.size)
			var err error
			if tc.encrypted {
				data, err = AESECBEncrypt(data, key)
				if err != nil {
					t.Fatal(err)
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.chunked {
					w.(http.Flusher).Flush()
				}
				_, _ = w.Write(data)
			}))
			defer server.Close()
			c := newMediaTestClient(t, WithCDNBaseURL(server.URL), func(cfg *Config) { cfg.MaxMediaBytes = 16 })
			img := &ImageItem{Media: &MediaInfo{FullURL: server.URL}}
			if tc.encrypted {
				img.AESKey = hex.EncodeToString(key)
			}
			got, _, err := c.downloadAndDecryptImage(context.Background(), img)
			if tc.wantErr {
				if !errors.Is(err, ErrMediaTooLarge) || got != nil {
					t.Fatalf("data=%q err=%v", got, err)
				}
			} else if err != nil || len(got) != tc.size {
				t.Fatalf("data=%q err=%v", got, err)
			}
		})
	}

	c := newMediaTestClient(t, func(cfg *Config) { cfg.MaxMediaBytes = 16; cfg.BaseURL = "http://must-not-call.invalid" })
	if _, err := c.UploadToCDN(context.Background(), "user", make([]byte, 17), CDNMediaTypeImage); !errors.Is(err, ErrMediaTooLarge) {
		t.Fatalf("oversized upload error=%v", err)
	}
}

func TestCDNUploadRetriesSameBytes(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ := io.ReadAll(r.Body)
		if string(got) != "ciphertext" {
			t.Errorf("upload data changed: %q", got)
		}
		if requests.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("X-Encrypted-Param", "download-param")
	}))
	defer server.Close()
	c := newMediaTestClient(t)
	got, err := c.uploadCDNBytes(context.Background(), []byte("ciphertext"), server.URL)
	if err != nil || got != "download-param" || requests.Load() != 3 {
		t.Fatalf("param=%q err=%v requests=%d", got, err, requests.Load())
	}
}

func TestCDNErrorsDoNotExposeSecrets(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "secret-response-token", http.StatusForbidden)
	}))
	defer server.Close()
	c := newMediaTestClient(t, WithCDNBaseURL(server.URL))
	secretURL := server.URL + "?token=secret-query-token"
	_, uploadErr := c.uploadCDNBytes(context.Background(), []byte("data"), secretURL)
	_, _, downloadErr := c.downloadAndDecryptImage(context.Background(), &ImageItem{Media: &MediaInfo{FullURL: secretURL}})
	for _, err := range []error{uploadErr, downloadErr} {
		var httpErr *HTTPError
		if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusForbidden || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe or untyped HTTP error: %v", err)
		}
	}
	if requests.Load() != 2 {
		t.Fatalf("4xx was retried: %d requests", requests.Load())
	}
}

func TestCDNUploadResponseBound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Encrypted-Param", "param")
		_, _ = w.Write([]byte("12345"))
	}))
	defer server.Close()
	c := newMediaTestClient(t, func(cfg *Config) { cfg.MaxResponseBytes = 4 })
	if _, err := c.uploadCDNBytes(context.Background(), nil, server.URL); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("oversized response error=%v", err)
	}
}

func TestImageDownloadMediaTimeout(t *testing.T) {
	c := newMediaTestClient(t, WithMediaTimeout(20*time.Millisecond), WithHTTPClient(&http.Client{Transport: mediaRoundTripper(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}))
	if _, _, err := c.downloadAndDecryptImage(context.Background(), &ImageItem{Media: &MediaInfo{FullURL: "https://cdn.example/image?token=secret"}}); !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("download timeout=%v", err)
	}
}

type mediaRoundTripper func(*http.Request) (*http.Response, error)

func (fn mediaRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }
