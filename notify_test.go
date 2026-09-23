package wechat

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMediaSendTimeoutIncludesFinalSend(t *testing.T) {
	for _, kind := range []string{"image", "video", "file", "empty-file"} {
		t.Run(kind, func(t *testing.T) {
			var requests atomic.Int32
			var overallDeadline time.Time
			c := newMediaTestClient(t, WithMediaTimeout(40*time.Millisecond), WithSendTimeout(time.Second), WithHTTPClient(&http.Client{
				Transport: mediaRoundTripper(func(r *http.Request) (*http.Response, error) {
					requests.Add(1)
					deadline, ok := r.Context().Deadline()
					if !ok {
						t.Error("request has no media deadline")
					}
					if overallDeadline.IsZero() {
						overallDeadline = deadline
					} else if !deadline.Equal(overallDeadline) {
						t.Errorf("media deadline changed: %v -> %v", overallDeadline, deadline)
					}
					body := ""
					header := make(http.Header)
					switch r.URL.Path {
					case "/ilink/bot/getuploadurl":
						body = `{"ret":0,"upload_full_url":"https://cdn.example/upload"}`
					case "/upload":
						header.Set("X-Encrypted-Param", "param")
					case "/ilink/bot/sendmessage":
						<-r.Context().Done()
						return nil, r.Context().Err()
					default:
						t.Errorf("unexpected path: %s", r.URL.Path)
					}
					return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(body))}, nil
				}),
			}))
			var err error
			switch kind {
			case "image":
				err = c.SendImage(context.Background(), "user", []byte("image"))
			case "video":
				err = c.SendVideo(context.Background(), "user", []byte("video"))
			case "file", "empty-file":
				err = c.SendFile(context.Background(), "user", "test.txt", []byte("file"))
			}
			if !errors.Is(err, context.DeadlineExceeded) || requests.Load() != 3 {
				t.Fatalf("send error=%v requests=%d", err, requests.Load())
			}
		})
	}
}

func TestUploadToCDNUsesMediaTimeout(t *testing.T) {
	c := newMediaTestClient(t, WithMediaTimeout(20*time.Millisecond), WithHTTPClient(&http.Client{Transport: mediaRoundTripper(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}))
	if _, err := c.UploadToCDN(context.Background(), "user", []byte("data"), CDNMediaTypeFile); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("direct upload timeout error=%v", err)
	}
}

func TestSendDoesNotRetryAmbiguousFailure(t *testing.T) {
	var requests atomic.Int32
	c := newMediaTestClient(t, WithHTTPClient(&http.Client{Transport: mediaRoundTripper(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		return &http.Response{StatusCode: http.StatusBadGateway, Body: io.NopCloser(strings.NewReader("secret server response")), Header: make(http.Header)}, nil
	})}))
	var httpErr *HTTPError
	if err := c.SendText(context.Background(), "user", "hello"); !errors.As(err, &httpErr) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("send error=%v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("ambiguous message send was retried %d times", requests.Load())
	}
}

func TestMediaSendPayloadMatchesUploadedBytes(t *testing.T) {
	for _, kind := range []string{"image", "video", "file", "empty-file"} {
		t.Run(kind, func(t *testing.T) {
			plain := []byte("media payload")
			if kind == "empty-file" {
				plain = nil
			}
			var uploadRequest wechatGetUploadURLRequest
			var sent wechatSendRequest
			c := newMediaTestClient(t, WithCDNBaseURL("https://cdn.example/"), WithHTTPClient(&http.Client{Transport: mediaRoundTripper(func(r *http.Request) (*http.Response, error) {
				body := `{"ret":0}`
				header := make(http.Header)
				switch r.URL.Path {
				case "/ilink/bot/getuploadurl":
					if err := json.NewDecoder(r.Body).Decode(&uploadRequest); err != nil {
						t.Error(err)
					}
					body = `{"ret":0,"upload_param":"upload+token&/=value"}`
				case "/upload":
					if r.URL.Query().Get("encrypted_query_param") != "upload+token&/=value" || r.URL.Query().Get("filekey") != uploadRequest.FileKey {
						t.Errorf("unexpected upload parameters: %v", r.URL.Query())
					}
					ciphertext, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					key, err := hex.DecodeString(uploadRequest.AESKey)
					if err != nil {
						t.Error(err)
					}
					got, err := AESECBDecrypt(ciphertext, key)
					if err != nil || !bytes.Equal(got, plain) || len(ciphertext) != uploadRequest.FileSize || uploadRequest.RawSize != len(plain) {
						t.Errorf("upload mismatch: data=%q request=%+v err=%v", got, uploadRequest, err)
					}
					header.Set("X-Encrypted-Param", "download-param")
					body = ""
				case "/ilink/bot/sendmessage":
					if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
						t.Error(err)
					}
				default:
					t.Errorf("unexpected URL path: %s", r.URL.Path)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}))
			var err error
			switch kind {
			case "image":
				err = c.SendImage(context.Background(), "user", plain)
			case "video":
				err = c.SendVideo(context.Background(), "user", plain)
			case "file", "empty-file":
				err = c.SendFile(context.Background(), "user", "report.txt", plain)
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(sent.Msg.ItemList) != 1 || sent.Msg.ToUserID != "user" {
				t.Fatalf("unexpected sent payload: %+v", sent)
			}
			item := sent.Msg.ItemList[0]
			var media *MediaInfo
			switch kind {
			case "image":
				if item.Type != int(ItemTypeImage) || item.ImageItem == nil || item.ImageItem.MidSize != uploadRequest.FileSize {
					t.Fatalf("bad image item: %+v", item)
				}
				media = item.ImageItem.Media
			case "video":
				if item.Type != int(ItemTypeVideo) || item.VideoItem == nil || item.VideoItem.VideoSize != uploadRequest.FileSize {
					t.Fatalf("bad video item: %+v", item)
				}
				media = item.VideoItem.Media
			case "file", "empty-file":
				if item.Type != int(ItemTypeFile) || item.FileItem == nil || item.FileItem.FileName != "report.txt" || item.FileItem.Len != strconv.Itoa(len(plain)) {
					t.Fatalf("bad file item: %+v", item)
				}
				media = item.FileItem.Media
			}
			if media == nil || media.EncryptQueryParam != "download-param" || media.AESKey != base64.StdEncoding.EncodeToString([]byte(uploadRequest.AESKey)) || media.EncryptType != EncryptTypeAES128ECB {
				t.Fatalf("unexpected media reference: %+v", media)
			}
		})
	}
}
