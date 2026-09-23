package wechat

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func diagnosticClient(t *testing.T, output io.Writer, level slog.Level, transport http.RoundTripper) *Client {
	t.Helper()
	return newSafetyTestClient(t,
		WithLogger(slog.New(slog.NewJSONHandler(output, &slog.HandlerOptions{Level: level}))),
		WithHTTPClient(&http.Client{Transport: transport}),
	)
}

func diagnosticRecords(t *testing.T, output *bytes.Buffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	for decoder.More() {
		var record map[string]any
		if err := decoder.Decode(&record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	return records
}

func TestHTTPDiagnosticsDisabled(t *testing.T) {
	var output bytes.Buffer
	client := diagnosticClient(t, &output, slog.LevelInfo, safetyRoundTripper(func(req *http.Request) (*http.Response, error) {
		if httptrace.ContextClientTrace(req.Context()) != nil {
			t.Error("installed trace when debug logging was disabled")
		}
		return safetyJSONResponse(`{"ret":0}`), nil
	}))
	if err := client.SendText(context.Background(), "user-secret", "message-secret"); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatalf("unexpected output: %s", output.String())
	}
}

type delayedDiagnosticBody struct {
	io.Reader
	wait time.Duration
}

func (b *delayedDiagnosticBody) Read(p []byte) (int, error) {
	if b.wait != 0 {
		time.Sleep(b.wait)
		b.wait = 0
	}
	return b.Reader.Read(p)
}

func (*delayedDiagnosticBody) Close() error { return nil }

func TestHTTPDiagnosticsStagesAndRedaction(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var output bytes.Buffer
		client := diagnosticClient(t, &output, slog.LevelDebug, safetyRoundTripper(func(req *http.Request) (*http.Response, error) {
			trace := httptrace.ContextClientTrace(req.Context())
			if trace == nil {
				t.Fatal("missing debug trace")
			}
			trace.GetConn("host-secret:443")
			trace.DNSStart(httptrace.DNSStartInfo{Host: "host-secret"})
			time.Sleep(2 * time.Second)
			trace.DNSDone(httptrace.DNSDoneInfo{})
			trace.ConnectStart("tcp", "address-secret")
			time.Sleep(3 * time.Second)
			trace.ConnectDone("tcp", "address-secret", nil)
			trace.TLSHandshakeStart()
			time.Sleep(5 * time.Second)
			trace.TLSHandshakeDone(tls.ConnectionState{}, nil)
			trace.GotConn(httptrace.GotConnInfo{})
			trace.WroteRequest(httptrace.WroteRequestInfo{})
			time.Sleep(7 * time.Second)
			trace.GotFirstResponseByte()
			return &http.Response{StatusCode: 200, Header: http.Header{"Secret": {"header-secret"}}, Body: &delayedDiagnosticBody{
				Reader: strings.NewReader(`{"ret":0,"token":"response-secret"}`), wait: 11 * time.Second,
			}}, nil
		}))
		var result map[string]any
		if err := client.doPost(context.Background(), "/ilink/bot/getupdates", map[string]string{"context_token": "request-secret"}, &result); err != nil {
			t.Fatal(err)
		}
		records := diagnosticRecords(t, &output)
		if len(records) != 2 || records[0]["msg"] != "wechat HTTP request started" || records[1]["msg"] != "wechat HTTP request completed" {
			t.Fatalf("unexpected records: %s", output.String())
		}
		completed := records[1]
		for key, want := range map[string]time.Duration{
			"elapsed": 28 * time.Second, "connection_wait": 10 * time.Second,
			"dns": 2 * time.Second, "tcp": 3 * time.Second, "tls": 5 * time.Second,
			"response_wait": 7 * time.Second, "response_read": 11 * time.Second,
		} {
			if completed[key] != float64(want) {
				t.Errorf("%s=%v, want %v", key, completed[key], want)
			}
		}
		if completed["success"] != true || completed["status_code"] != float64(200) || completed["connection_reused"] != false {
			t.Fatalf("unexpected result fields: %v", completed)
		}
		for _, secret := range []string{"test-bot-token", "request-secret", "response-secret", "header-secret", "host-secret", "address-secret", DefaultBaseURL} {
			if strings.Contains(output.String(), secret) {
				t.Errorf("diagnostics leaked %q", secret)
			}
		}
	})
}

func TestHTTPDiagnosticsFailuresComplete(t *testing.T) {
	for _, mode := range []string{"transport", "status", "decode", "cancel", "marshal"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var output bytes.Buffer
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				client := diagnosticClient(t, &output, slog.LevelDebug, safetyRoundTripper(func(req *http.Request) (*http.Response, error) {
					switch mode {
					case "transport":
						return nil, errors.New("transport-secret")
					case "status":
						resp := safetyJSONResponse(`{"token":"response-secret"}`)
						resp.StatusCode = 503
						return resp, nil
					case "decode":
						return safetyJSONResponse(`decode-secret`), nil
					case "cancel":
						httptrace.ContextClientTrace(req.Context()).WroteRequest(httptrace.WroteRequestInfo{})
						time.Sleep(2 * time.Second)
						cancel()
						return nil, ctx.Err()
					default:
						t.Fatal("unexpected request")
						return nil, nil
					}
				}))
				var body any = struct{}{}
				if mode == "marshal" {
					body = make(chan int)
				}
				var result map[string]any
				if err := client.doPost(ctx, "/ilink/bot/getupdates", body, &result); err == nil {
					t.Fatal("expected error")
				}
				records := diagnosticRecords(t, &output)
				if len(records) != 2 || records[1]["success"] != false {
					t.Fatalf("missing failed completion: %s", output.String())
				}
				if mode == "cancel" && records[1]["response_wait"] != float64(2*time.Second) {
					t.Fatalf("missing unfinished response wait: %v", records[1])
				}
				if strings.Contains(output.String(), "secret") {
					t.Fatalf("sensitive failure text logged: %s", output.String())
				}
			})
		})
	}
}

func TestHTTPDiagnosticsConnectionReuse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"ret":0}`)
	}))
	defer server.Close()
	var output bytes.Buffer
	client := newSafetyTestClient(t, WithBaseURL(server.URL), WithHTTPClient(server.Client()),
		WithLogger(slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	for range 2 {
		if err := client.SendText(context.Background(), "user", "text"); err != nil {
			t.Fatal(err)
		}
	}
	records := diagnosticRecords(t, &output)
	if len(records) != 4 || records[1]["connection_reused"] != false || records[3]["connection_reused"] != true {
		t.Fatalf("unexpected reuse: %s", output.String())
	}
}

func TestHTTPDiagnosticsConcurrentHooks(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ctx, diagnostic := startHTTPDiagnostic(context.Background(), logger, "/ilink/bot/getupdates")
	trace := httptrace.ContextClientTrace(ctx)
	var workers sync.WaitGroup
	for range 10 {
		workers.Go(func() {
			for range 100 {
				trace.ConnectStart("tcp", "address-secret")
				trace.ConnectDone("tcp", "address-secret", nil)
			}
		})
	}
	diagnostic.finish(ctx, false)
	workers.Wait()
}
