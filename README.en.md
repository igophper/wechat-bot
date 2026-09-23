# WeChat iLink Bot SDK for Go

[中文](README.md) | [English](README.en.md)

[![CI](https://github.com/igophper/wechat-bot/actions/workflows/ci.yml/badge.svg)](https://github.com/igophper/wechat-bot/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/igophper/wechat-bot.svg)](https://pkg.go.dev/github.com/igophper/wechat-bot)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

An independent Go client for the WeChat iLink bot protocol, based on [Tencent/openclaw-weixin](https://github.com/Tencent/openclaw-weixin). It provides QR login, message polling, text and media replies, image decryption, and cursor storage. **This is a community SDK, not an official Tencent SDK or an OpenClaw plugin.**

APIs may change during v0. Pin your dependency version.

## Requirements and scope

- Go **1.26.0 or later**, preferably the latest patch of a supported branch. CI covers Go 1.26 and 1.27 and tests Linux, macOS, and Windows.
- A WeChat account eligible for iLink QR authorization, with network access to iLink and the WeChat CDN. Account eligibility, rate limits, and message validity are controlled by the platform.
- Recipient IDs come from incoming messages' `FromUserID`, not a phone number or WeChat username. Replies normally need that user's recent `ContextToken`; do not assume a bot token permits arbitrary outbound messaging. Acceptance depends on the platform's policy.
- Send text, Markdown converted to plain text, images, files, and videos. Receive text, images, files, videos, and voice metadata. Incoming images can be downloaded and decrypted. Voice text is supplied by the server; the SDK does not run speech recognition.
- Text reception and replies have been verified with an existing live account session. Fresh QR login, verification-code branches, and media transfers have not yet been verified on a live account. Automated regression tests use simulated servers.

## Install

In your Go project:

```bash
go get github.com/igophper/wechat-bot@v0.1.0
```

## Run the example

From the cloned repository root:

```bash
go run ./examples/basic
```

The [complete example](examples/basic/main.go) displays a QR code on first launch, saves credentials to `./state/credentials.json`, and persists the message cursor. Subsequent launches prefer saved credentials. When none exist, it also accepts `WECHAT_BOT_TOKEN` and `WECHAT_BOT_ID`, with optional `WECHAT_BASE_URL`. Environment credentials are not automatically written to disk.

Send `ping`, `report`, or `help`, or send an image or voice message. Press `Ctrl+C` to cancel polling and wait for active handlers. A handler failure stops the example with a nonzero exit code and leaves the batch unacknowledged.

```bash
go run ./examples/basic --login
go run ./examples/basic --state-dir ./state
```

To investigate slow replies, enable timing logs:

```bash
go run ./examples/basic --debug
```

Logs include API paths, connection/request timings, message IDs, and handler timings, without request/response bodies, full URLs, or credentials. Use `connection_wait`, `dns`, `tcp`, and `tls` to inspect connection setup; `response_wait` measures the time from sending the request to receiving the first response byte. When the server supplies a creation timestamp, `message_age` estimates message age at dispatch, including time queued behind earlier handlers; clock differences affect this estimate. An idle long poll normally waits for a message, so its entire duration is not the message's delivery latency. Compare your actual send time with `wechat poll received`, `wechat message dispatch`, and `wechat example reply finished`. `retry_in` identifies delays from failed polls or session recovery.

The basic example replies immediately without first sending a typing indicator. `SendTyping` makes additional `getconfig` and, when supported, `sendtyping` requests. Calling it synchronously delays both the reply and polling the next batch; use it as needed for longer-running work.

`--login` requests a fresh QR login. Run again if a QR code expires. If the platform requires a verification code, the basic example prompts for terminal input; press Enter to submit it. Applications with their own login UI can pass `WithVerificationCodeHandler` to `WaitForQRConfirmation`. Its callback has type `func(context.Context) (string, error)` and should honor cancellation and avoid logging the code. The example uses synchronous terminal input, which must finish before the callback can return. The SDK handles QR login redirects.

The default `state/` directory is ignored by Git. Credentials are plaintext secrets; keep them in a directory you control and never attach them to issues. Add any custom state directory to your own ignore rules.

## Integrate

This complete program reuses credentials saved by the example and replies to text messages:

```go
package main

import (
    "context"
    "errors"
    "fmt"
    "log"
    "os"
    "os/signal"
    "syscall"

    "github.com/igophper/wechat-bot"
)

func main() {
    if err := run(); err != nil {
        log.Println(err)
        os.Exit(1)
    }
}

func run() error {
    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer stop()

    creds, err := wechat.LoadCredentials("./state/credentials.json")
    if err != nil {
        return err
    }
    if creds == nil {
        return fmt.Errorf("run examples/basic to log in first")
    }
    bot, err := wechat.NewClient(wechat.Config{
        BotToken: creds.BotToken,
        ILinkBotID: creds.ILinkBotID,
        ILinkUserID: creds.ILinkUserID,
        BaseURL: creds.BaseURL,
        Storage: wechat.NewFileBufStorage("./state"),
    })
    if err != nil {
        return err
    }
    bot.OnMessage(func(ctx context.Context, msg *wechat.InboundMessage) error {
        if !msg.IsText() {
            return nil
        }
        return bot.SendText(ctx, msg.FromUserID, "Received: "+msg.Text)
    })
    err = bot.Start(ctx)
    if errors.Is(err, context.Canceled) {
        return nil
    }
    return err
}
```

| Method | Purpose |
| --- | --- |
| `SendText(ctx, userID, text)` | Send text |
| `SendMarkdown(ctx, userID, markdown)` | Convert Markdown to readable plain text and send it |
| `SendImage(ctx, userID, data)` | Encrypt, upload, and send an image |
| `SendVideo(ctx, userID, data)` | Encrypt, upload, and send a video |
| `SendFile(ctx, userID, filename, data)` | Encrypt, upload, and send a file |
| `SendTyping(ctx, userID)` | Request a typing indicator, when available |
| `msg.DownloadDecryptedImage(ctx)` | Return image bytes, detected MIME type, and any error |

Check all send and download errors. Media types exposed by `InboundMessage` are exported, and `RawMessage` is `json.RawMessage` for inspecting additional protocol fields. Items from the same original message share this read-only byte slice; clone it before making changes. AES-128-ECB and MD5 are required for compatibility with the media protocol, not recommendations for other applications.

## Processing and restart behavior

- `MessageHandler` returns an error. A cursor is saved only after all handlers for the entire batch succeed. A handler error, panic, cancellation, or storage error stops `Start` without acknowledging the batch.
- Handlers run serially by default. `HandlerConcurrency` or `WithHandlerConcurrency(n)` permits concurrent processing across users while preserving each user's incoming order. Each `Start` snapshots the registered handlers; registrations made while it runs apply to the next start.
- Handlers must honor `ctx` and apply timeouts to their own I/O. `Start` waits for active handlers on cancellation; a handler that ignores cancellation can delay shutdown indefinitely.
- Concurrent calls to `Start` on one client return `ErrAlreadyStarted`. You can restart after the previous call returns.
- A crash or error after side effects but before saving the cursor can cause replay. Use message IDs and processing steps to make your application idempotent. The SDK does not promise exactly-once delivery, and server retention limits recovery.

> **Important: do not return an error for a permanent failure.** The cursor covers a whole batch. If one message always fails, `Start` always stops on it, the cursor never advances, and a supervisor such as systemd or Kubernetes replays the same batch forever. The bot wedges and **receives no further messages** — later messages in that batch are never processed either.
>
> Split failures inside the handler instead:
>
> - **Transient** (network blips, 5xx, timeouts) → return the error, let `Start` stop, and replay after restart.
> - **Permanent** (invalid data, 4xx, oversized content) → write the message to durable dead-letter storage and **return nil once that write is safe**, so the cursor can advance.
> - Return an error only when even the dead-letter write fails; stopping really is better than losing the message then.
>
> The [example](examples/basic/main.go) implements this split; see its `permanent` and `record` functions. Tune the classification for your workload: too broad drops messages, too narrow wedges the bot.
- `OnExpired` means that attempts to recover an empty-cursor session have been exhausted. It does not prove permanent credential revocation. Inspect errors and service status before logging in again; do not automatically delete credentials.

The default `MemoryBufStorage` does not survive process restarts. The example uses `FileBufStorage`, which is intended for a single process. Do not run multiple listeners sharing one account and cursor directory. Implement `BufStorage` to use another persistence backend.

## Context tokens and notifications

Polling automatically caches the latest context token for each user. The cache holds **1024 users** by default with LRU eviction, is cleared on restart, and is not persisted by the SDK. A handler's context also carries its message's token, so replies using that context are unaffected by concurrent cache eviction. The server may reject sends without valid context.

For notifications from separate jobs, securely persist the incoming `FromUserID` and `ContextToken` in your application and restore them using `SetContextToken(userID, token)`. `ContextToken(userID)` reads a cached token; `ForgetContextToken(userID)` removes it. Restoring a token neither extends its platform lifetime nor bypasses sending restrictions.

## Configuration

Use `Config` fields or their `With...` options. Zero values select defaults:

| Field | Default | Purpose |
| --- | --- | --- |
| `HandlerConcurrency` | `1` | Maximum concurrent handlers |
| `MaxMediaBytes` | `32 MiB` | Media size limit |
| `MaxResponseBytes` | `1 MiB` | API response size limit |
| `MaxContextTokens` | `1024` | Cached users |
| `SendTimeout` | `15s` | Message send timeout |
| `MediaTimeout` | `90s` | Media operation timeout |
| `TypingTimeout` | `8s` | Typing operation timeout |
| `LongPollTimeout` | `35s` | Long-poll baseline; the HTTP request allows an additional 5 seconds |
| `BackoffInitial` / `BackoffMax` | `3s` / `60s` | Retry backoff range; actual delays are jittered within the upper half and never exceed `BackoffMax` |
| `EmptyBufExpiredThreshold` | `20` | Empty-cursor recovery failure limit |
| `MediaHosts` | empty | Hosts media transfers may contact; see below |

Media is encrypted and decrypted in memory. Higher size or concurrency limits increase memory usage. `BaseURL`, `CDNBaseURL`, `HTTPClient`, `Storage`, and `Logger` are configurable for testing, proxies, and application infrastructure.

Empty content is not silently dropped: `SendText`, `SendMarkdown`, `SendImage`, and `SendVideo` return `ErrEmptyMessage` rather than reporting success without delivering anything. A zero-byte file is a valid document, so `SendFile` uploads and sends it.

### Server-supplied URLs

The server chooses media transfer URLs and the QR login redirect host. The SDK validates them before sending a request and re-validates **every redirect hop**:

- With an HTTPS base URL, targets and redirects must use HTTPS. Explicitly configuring an HTTP base URL also permits HTTP for other hosts accepted by that policy, not just the base host.
- Non-public IP literals are rejected unless explicitly trusted (for example `127.0.0.1`, `10.0.0.1`, and `169.254.169.254`). Configured base hosts and allowlisted hosts are trusted exceptions.
- Redirects stop after five hops.
- The `CDNBaseURL` host is always allowed, and signed media URLs keep their query strings.

Public hosts are not restricted by default, because CDN nodes may use a different hostname than `CDNBaseURL`. To pin an explicit set, use `WithMediaHosts("cdn.example", ...)`, or `WithLoginHosts(...)` for login redirects. Hostnames are not resolved, so a public name that resolves to an internal address is still reachable; pin hosts explicitly when that matters.

### QR verification codes

When WeChat asks for a verification code, `WaitForQRConfirmation` calls the `WithVerificationCodeHandler` callback. The first poll is immediate. Ordinary retries and submission of a new code wait for `interval`; bounded login redirects are followed immediately. An instantly returning verification callback therefore cannot bypass the retry delay. At most `DefaultVerificationAttempts` (3) code inputs are accepted; another request for input returns `ErrVerificationAttemptsExhausted`. Adjust the limit with `WithVerificationAttempts(n)`. This is a **client-side** limit and stays distinct from `ErrVerificationBlocked`, which reports a server-side block.

An unrecognized non-empty QR status is reported through `onStatus` and polled through at the normal interval, so a new server state does not break login outright.

## Contributing and feedback

Chinese and English issues and pull requests are welcome. See [CONTRIBUTING](CONTRIBUTING.md) for development checks and the release process, [SECURITY](SECURITY.md) for private vulnerability reports, and [CHANGELOG](CHANGELOG.md) for version history.

## Protocol and license

Protocol reference: [Tencent/openclaw-weixin](https://github.com/Tencent/openclaw-weixin). Server behavior may change; sanitized compatibility reports are welcome. WeChat names and trademarks belong to Tencent. Follow the platform's rules when using this library.

[MIT License](LICENSE).
