package wechat

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net/http/httptrace"
	"sync"
	"time"
)

// tracePhase accumulates elapsed time, treating overlapping attempts (such as
// IPv4/IPv6 dials) as one wall-clock interval rather than double-counting them.
type tracePhase struct {
	started time.Time
	pending int
	elapsed time.Duration
	seen    bool
}

func (p *tracePhase) start() {
	if p.pending == 0 {
		p.started = time.Now()
	}
	p.pending++
	p.seen = true
}

func (p *tracePhase) done() {
	if p.pending == 0 {
		return
	}
	p.pending--
	if p.pending == 0 {
		p.elapsed += time.Since(p.started)
	}
}

func (p *tracePhase) duration(now time.Time) time.Duration {
	d := p.elapsed
	if p.pending > 0 {
		d += now.Sub(p.started)
	}
	return d
}

type httpDiagnostic struct {
	logger   *slog.Logger
	endpoint string
	started  time.Time

	// Trace hooks can run concurrently, including after the HTTP call returns.
	mu                        sync.Mutex
	connection, dns, tcp, tls tracePhase
	responseWait              tracePhase
	gotConnection, reused     bool
	responseReceived          bool

	// Only doPost accesses these fields.
	statusCode   int
	readStarted  time.Time
	readDuration time.Duration
}

func startHTTPDiagnostic(ctx context.Context, logger *slog.Logger, endpoint string) (context.Context, *httpDiagnostic) {
	if !logger.Enabled(ctx, slog.LevelDebug) {
		return ctx, nil
	}
	d := &httpDiagnostic{logger: logger, endpoint: endpoint, started: time.Now()}
	logger.DebugContext(ctx, "wechat HTTP request started", "endpoint", endpoint)
	trace := &httptrace.ClientTrace{
		GetConn: func(string) {
			d.mu.Lock()
			defer d.mu.Unlock()
			d.connection.start()
			// A redirect or transport retry begins another exchange. End any
			// unsuccessful exchange's wait before acquiring its new connection.
			d.responseWait.done()
			d.responseReceived = false
		},
		GotConn: func(info httptrace.GotConnInfo) {
			d.mu.Lock()
			defer d.mu.Unlock()
			d.connection.done()
			d.gotConnection, d.reused = true, info.Reused
		},
		DNSStart: func(httptrace.DNSStartInfo) { d.startPhase(&d.dns) },
		DNSDone:  func(httptrace.DNSDoneInfo) { d.endPhase(&d.dns) },
		ConnectStart: func(string, string) {
			d.startPhase(&d.tcp)
		},
		ConnectDone:       func(string, string, error) { d.endPhase(&d.tcp) },
		TLSHandshakeStart: func() { d.startPhase(&d.tls) },
		TLSHandshakeDone:  func(tls.ConnectionState, error) { d.endPhase(&d.tls) },
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			d.mu.Lock()
			defer d.mu.Unlock()
			// HTTP/1 can receive a response concurrently with the write callback.
			if info.Err == nil && !d.responseReceived {
				d.responseWait.start()
			}
		},
		GotFirstResponseByte: func() {
			d.mu.Lock()
			defer d.mu.Unlock()
			d.responseReceived = true
			d.responseWait.done()
		},
	}
	return httptrace.WithClientTrace(ctx, trace), d
}

func (d *httpDiagnostic) startPhase(phase *tracePhase) {
	d.mu.Lock()
	defer d.mu.Unlock()
	phase.start()
}

func (d *httpDiagnostic) endPhase(phase *tracePhase) {
	d.mu.Lock()
	defer d.mu.Unlock()
	phase.done()
}

func (d *httpDiagnostic) finish(ctx context.Context, success bool) {
	d.mu.Lock()
	now := time.Now()
	attrs := []slog.Attr{
		slog.String("endpoint", d.endpoint),
		slog.Duration("elapsed", now.Sub(d.started)),
		slog.Int("status_code", d.statusCode),
		slog.Bool("success", success),
	}
	for _, phase := range []struct {
		name  string
		value *tracePhase
	}{
		{"connection_wait", &d.connection}, {"dns", &d.dns},
		{"tcp", &d.tcp}, {"tls", &d.tls}, {"response_wait", &d.responseWait},
	} {
		if phase.value.seen {
			attrs = append(attrs, slog.Duration(phase.name, phase.value.duration(now)))
		}
	}
	if d.gotConnection {
		attrs = append(attrs, slog.Bool("connection_reused", d.reused))
	}
	d.mu.Unlock()
	if !d.readStarted.IsZero() {
		attrs = append(attrs, slog.Duration("response_read", d.readDuration))
	}
	d.logger.LogAttrs(ctx, slog.LevelDebug, "wechat HTTP request completed", attrs...)
}
