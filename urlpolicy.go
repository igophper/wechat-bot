package wechat

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// maxServerRedirects bounds redirect hops on server-supplied URLs.
const maxServerRedirects = 5

// urlPolicy decides which server-supplied URLs this client may contact. The
// server chooses media and redirect targets, so a URL is validated before the
// first request and again on every redirect hop: a target that passes the
// initial check can still redirect somewhere else.
type urlPolicy struct {
	// requireTLS rejects plaintext http so a redirect cannot downgrade a
	// request that started on https. It is relaxed only when the operator
	// configured a plaintext base URL themselves.
	requireTLS bool
	// configured is the operator-chosen host. It is trusted on every port.
	configured string
	// allowed optionally pins additional hosts. While it is empty, any public
	// host is accepted and only private, loopback, link-local, and other
	// non-global addresses are rejected.
	allowed []string
}

func newURLPolicy(baseURL string, allowed []string) urlPolicy {
	p := urlPolicy{requireTLS: true, allowed: allowed}
	if u, err := url.Parse(baseURL); err == nil {
		p.configured = strings.ToLower(u.Hostname())
		p.requireTLS = u.Scheme != "http"
	}
	return p
}

// checkString validates a server-supplied URL string before it is requested.
func (p urlPolicy) checkString(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: malformed URL", ErrBlockedURL)
	}
	return p.check(u)
}

func (p urlPolicy) check(u *url.URL) error {
	switch u.Scheme {
	case "https":
	case "http":
		if p.requireTLS {
			return fmt.Errorf("%w: refusing plaintext http", ErrBlockedURL)
		}
	default:
		return fmt.Errorf("%w: unsupported URL scheme", ErrBlockedURL)
	}
	if u.User != nil {
		return fmt.Errorf("%w: URL carries credentials", ErrBlockedURL)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return fmt.Errorf("%w: URL has no host", ErrBlockedURL)
	}
	if host == p.configured {
		return nil
	}
	if len(p.allowed) > 0 {
		for _, candidate := range p.allowed {
			if host == strings.ToLower(candidate) {
				return nil
			}
		}
		return fmt.Errorf("%w: host is not in the configured allowlist", ErrBlockedURL)
	}
	if ip := net.ParseIP(host); ip != nil && !isPublicIP(ip) {
		return fmt.Errorf("%w: refusing a non-public address", ErrBlockedURL)
	}
	return nil
}

// isPublicIP reports whether an address literal is routable on the internet.
// Hostnames are not resolved here, so a name that resolves to a private
// address is still reachable; pin hosts explicitly when that matters.
func isPublicIP(ip net.IP) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast()
}

// checkRedirect enforces the policy on every hop of a server-directed request.
func (p urlPolicy) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxServerRedirects {
		return fmt.Errorf("%w: too many redirects", ErrBlockedURL)
	}
	return p.check(req.URL)
}

// redirectGuardedClient copies base so the caller's client keeps its own
// redirect policy, and installs this policy's per-hop check on the copy. The
// transport, cookie jar, and timeout are shared with base.
func (p urlPolicy) redirectGuardedClient(base *http.Client) *http.Client {
	guarded := *base
	guarded.CheckRedirect = p.checkRedirect
	return &guarded
}
