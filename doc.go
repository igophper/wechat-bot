// Package wechat provides an independent Go client for the WeChat iLink bot
// protocol. It supports QR login, polling incoming messages, text and media
// replies, image decryption, and cursor storage.
//
// Create a Client with NewClient, register a MessageHandler with OnMessage, and
// call Start with a cancelable context. Handlers run serially by default. With
// HandlerConcurrency greater than one, different users can be handled in
// parallel while messages from the same user remain ordered. Handlers must honor
// their context; Start waits for active handlers before returning.
//
// A polling cursor is persisted only after every handler in its batch succeeds.
// Handler errors, panics, cancellation, and storage failures stop Start without
// acknowledging that batch. A restart may replay messages, so applications
// should make side effects idempotent. This is not an exactly-once delivery
// guarantee; upstream message retention also limits recovery.
//
// Because the cursor covers a whole batch, a handler that always fails on one
// message blocks every later message too: Start stops, the cursor stays put,
// and a supervised restart replays the same batch indefinitely. Return an error
// only for failures worth retrying. For a failure that will never succeed,
// record the message somewhere durable first and return nil once that record is
// safe, so the cursor can advance. The basic example implements this split.
//
// Media transfers and login redirects follow URLs chosen by the server. Those
// URLs, and every redirect hop they take, are checked before a request is sent:
// HTTPS base URLs require HTTPS targets. Non-public IP literals are refused
// unless explicitly trusted by base-host or allowlist configuration. Hostnames
// are not resolved for this check. Configuring an HTTP base URL permits HTTP
// for the policy's accepted hosts. MediaHosts and WithLoginHosts can restrict
// hostnames; these URL checks do not provide complete protection against SSRF.
//
// Reply context tokens are cached in memory. Applications sending outside a
// handler can use ContextToken and SetContextToken to persist and restore them
// securely. Applications should not assume a bot token permits sending to
// arbitrary users; acceptance depends on the backend's conversation policy.
//
// This package is a community implementation, not an official Tencent SDK.
package wechat
