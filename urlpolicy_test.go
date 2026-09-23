package wechat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A server-supplied upload URL that looks like HTTPS must not be able to
// redirect the encrypted body onto a plaintext or private host.
func TestMediaUploadRejectsRedirectDowngrade(t *testing.T) {
	var landed int
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		landed++
		w.Header().Set("X-Encrypted-Param", "param")
	}))
	defer attacker.Close()
	cdn := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attacker.URL+"/upload", http.StatusTemporaryRedirect)
	}))
	defer cdn.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ilink/bot/getuploadurl" {
			_ = json.NewEncoder(w).Encode(map[string]any{"ret": 0, "upload_full_url": cdn.URL + "/upload"})
			return
		}
		_ = json.NewEncoder(w).Encode(wechatSendResponse{Ret: 0})
	}))
	defer api.Close()

	c, err := NewClient(Config{BotToken: "token", ILinkBotID: "bot@im.bot", BaseURL: api.URL, HTTPClient: cdn.Client()})
	if err != nil {
		t.Fatal(err)
	}
	err = c.SendImage(context.Background(), "user", []byte("secret media"))
	if !errors.Is(err, ErrBlockedURL) {
		t.Fatalf("redirect downgrade was not refused: %v", err)
	}
	if landed != 0 {
		t.Fatalf("encrypted body reached the redirect target %d times", landed)
	}
}

// The initial URL is checked too, not only redirects.
func TestMediaPolicyRejectsServerSuppliedTargets(t *testing.T) {
	c, err := NewClient(Config{BotToken: "token", ILinkBotID: "bot@im.bot"})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{
		"http://cdn.example/image",      // plaintext downgrade
		"https://127.0.0.1/image",       // loopback
		"https://10.0.0.5/image",        // private
		"https://169.254.169.254/image", // link-local metadata
		"https://[::1]/image",           // IPv6 loopback
		"file:///etc/passwd",            // unsupported scheme
		"https://user:pw@cdn.example/i", // embedded credentials
		"https:///image",                // no host
	} {
		t.Run(target, func(t *testing.T) {
			_, _, err := c.downloadAndDecryptImage(context.Background(), &ImageItem{Media: &MediaInfo{FullURL: target}})
			if !errors.Is(err, ErrBlockedURL) {
				t.Fatalf("accepted %s: %v", target, err)
			}
		})
	}
}

// Public hosts stay reachable by default, and MediaHosts pins them further.
func TestMediaPolicyAllowsConfiguredAndPinnedHosts(t *testing.T) {
	policy := newURLPolicy(DefaultCDNBaseURL, nil)
	for _, ok := range []string{
		"https://novac2c.cdn.weixin.qq.com/c2c/download?encrypted_query_param=x",
		"https://some-other-cdn.example/media?sig=abc", // signed URLs keep their query
	} {
		if err := policy.checkString(ok); err != nil {
			t.Fatalf("rejected legitimate URL %s: %v", ok, err)
		}
	}

	pinned := newURLPolicy(DefaultCDNBaseURL, []string{"Allowed.Example"})
	if err := pinned.checkString("https://allowed.example/media"); err != nil {
		t.Fatalf("allowlist is case sensitive: %v", err)
	}
	if err := pinned.checkString("https://novac2c.cdn.weixin.qq.com/c2c/x"); err != nil {
		t.Fatalf("configured CDN host must stay allowed: %v", err)
	}
	if err := pinned.checkString("https://other.example/media"); !errors.Is(err, ErrBlockedURL) {
		t.Fatalf("allowlist did not exclude other hosts: %v", err)
	}
}

// An operator who configures a plaintext base URL opts into plaintext, but
// only for that host.
func TestMediaPolicyPlaintextOnlyForConfiguredHost(t *testing.T) {
	policy := newURLPolicy("http://localhost:9000", nil)
	if err := policy.checkString("http://localhost:9000/upload"); err != nil {
		t.Fatalf("configured plaintext host refused: %v", err)
	}
	if err := policy.checkString("http://10.0.0.5/upload"); !errors.Is(err, ErrBlockedURL) {
		t.Fatalf("plaintext base widened to other private hosts: %v", err)
	}
}

func TestURLPolicyBoundsRedirectHops(t *testing.T) {
	policy := newURLPolicy(DefaultCDNBaseURL, nil)
	req, err := http.NewRequest(http.MethodGet, "https://cdn.example/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	via := make([]*http.Request, maxServerRedirects)
	if err := policy.checkRedirect(req, via); !errors.Is(err, ErrBlockedURL) {
		t.Fatalf("redirect hop limit not enforced: %v", err)
	}
	if err := policy.checkRedirect(req, via[:maxServerRedirects-1]); err != nil {
		t.Fatalf("refused a hop within the limit: %v", err)
	}
}

// The guarded client must not mutate the caller's http.Client.
func TestRedirectGuardedClientLeavesCallerClientAlone(t *testing.T) {
	base := &http.Client{Timeout: 7 * time.Second}
	guarded := newURLPolicy(DefaultCDNBaseURL, nil).redirectGuardedClient(base)
	if base.CheckRedirect != nil {
		t.Fatal("caller's client was mutated")
	}
	if guarded.CheckRedirect == nil || guarded.Timeout != base.Timeout {
		t.Fatal("guarded client lost its base settings or its redirect check")
	}
}

// A login redirect steers where credentials come from, so it gets the same
// treatment as media URLs.
func TestQRRedirectRejectsNonPublicHosts(t *testing.T) {
	policy := newURLPolicy("https://ilinkai.weixin.qq.com", nil)
	for _, host := range []string{"127.0.0.1", "10.0.0.5", "169.254.169.254", "[::1]", "192.168.1.1"} {
		if _, err := qrRedirectURL(host, policy); !errors.Is(err, ErrBlockedURL) {
			t.Errorf("accepted redirect to %s: %v", host, err)
		}
	}
	if _, err := qrRedirectURL("other-region.weixin.qq.com", policy); err != nil {
		t.Errorf("refused a public redirect host: %v", err)
	}
}

// WithLoginHosts pins login redirects for deployments that want it.
func TestQRRedirectHonorsLoginHostAllowlist(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(QRStatusResult{Status: QRStatusRedirect, RedirectHost: "elsewhere.example"})
	}))
	defer server.Close()
	_, err := WaitForQRConfirmation(context.Background(), "qr", time.Hour, time.Second, nil,
		WithLoginBaseURL(server.URL), WithLoginHosts("only-this.example"))
	if !errors.Is(err, ErrBlockedURL) {
		t.Fatalf("login host allowlist not enforced: %v", err)
	}
}

func TestClientVersionHeaderMatchesVersion(t *testing.T) {
	if Version != "0.1.0" || clientVersionHeader != "256" {
		t.Fatalf("version %q encodes to %q; update both or fix encodeClientVersion", Version, clientVersionHeader)
	}
	for version, want := range map[string]string{
		"0.1.0":   "256",
		"2.4.8":   "132104", // the reference plugin's documented example
		"1.0.0":   "65536",
		"0.0.1":   "1",
		"bad":     "0",
		"1.2":     "0",
		"1.2.3.4": "0",
		"1.2.256": "0",
	} {
		if got := encodeClientVersion(version); got != want {
			t.Errorf("encodeClientVersion(%q)=%q want %q", version, got, want)
		}
	}
}

func TestEqualJitterStaysWithinBounds(t *testing.T) {
	for _, base := range []time.Duration{time.Nanosecond, time.Millisecond, 3 * time.Second, time.Minute} {
		var sawSpread bool
		first := equalJitter(base)
		for range 200 {
			got := equalJitter(base)
			if got > base || got < base/2 {
				t.Fatalf("equalJitter(%v)=%v escaped [%v, %v]", base, got, base/2, base)
			}
			if got != first {
				sawSpread = true
			}
		}
		if base > time.Millisecond && !sawSpread {
			t.Fatalf("equalJitter(%v) never varied", base)
		}
	}
}

// Jittered backoff must still respect the configured maximum.
func TestBackoffJitterRespectsMax(t *testing.T) {
	c, err := NewClient(Config{BotToken: "t", ILinkBotID: "b"}, WithBackoff(time.Second, 4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for failures := range 20 {
		if got := c.backoff(failures); got > 4*time.Second {
			t.Fatalf("backoff(%d)=%v exceeded BackoffMax", failures, got)
		}
	}
	if base := c.backoffBase(20); base != 4*time.Second {
		t.Fatalf("backoffBase saturates at %v, want 4s", base)
	}
}

// Empty payloads must not report success without delivering anything.
func TestEmptySendsReportErrEmptyMessage(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_ = json.NewEncoder(w).Encode(wechatSendResponse{Ret: 0})
	}))
	defer server.Close()
	c, err := NewClient(Config{BotToken: "t", ILinkBotID: "b", BaseURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for name, send := range map[string]func() error{
		"blank text":     func() error { return c.SendText(ctx, "user", "   \n\t ") },
		"empty text":     func() error { return c.SendText(ctx, "user", "") },
		"blank markdown": func() error { return c.SendMarkdown(ctx, "user", "\n\n") },
		"empty image":    func() error { return c.SendImage(ctx, "user", nil) },
		"empty video":    func() error { return c.SendVideo(ctx, "user", nil) },
	} {
		if err := send(); !errors.Is(err, ErrEmptyMessage) {
			t.Errorf("%s: got %v, want ErrEmptyMessage", name, err)
		}
	}
	if requests != 0 {
		t.Fatalf("empty sends still reached the server %d times", requests)
	}
}

// SendFile deliberately keeps accepting zero-byte documents.
func TestSendFileStillAcceptsEmptyDocuments(t *testing.T) {
	var uploaded, sent bool
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/getuploadurl":
			_ = json.NewEncoder(w).Encode(map[string]any{"ret": 0, "upload_full_url": server.URL + "/upload"})
		case "/upload":
			body, _ := io.ReadAll(r.Body)
			uploaded = len(body) == 16 // one padding block for empty plaintext
			w.Header().Set("X-Encrypted-Param", "param")
		default:
			sent = true
			_ = json.NewEncoder(w).Encode(wechatSendResponse{Ret: 0})
		}
	}))
	defer server.Close()
	c, err := NewClient(Config{BotToken: "t", ILinkBotID: "b", BaseURL: server.URL,
		CDNBaseURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SendFile(context.Background(), "user", "empty.txt", nil); err != nil {
		t.Fatalf("empty file rejected: %v", err)
	}
	if !uploaded || !sent {
		t.Fatalf("empty file not delivered: uploaded=%v sent=%v", uploaded, sent)
	}
}

func TestErrEmptyMessageIsDistinctFromBlockedURL(t *testing.T) {
	if errors.Is(ErrEmptyMessage, ErrBlockedURL) || errors.Is(ErrVerificationAttemptsExhausted, ErrVerificationBlocked) {
		t.Fatal("new sentinels must stay distinguishable")
	}
	if !strings.HasPrefix(ErrBlockedURL.Error(), "wechat: ") {
		t.Fatal("sentinel messages should stay package-prefixed")
	}
}
