# WeChat iLink Bot SDK for Go

[中文](README.md) | [English](README.en.md)

[![CI](https://github.com/igophper/wechat-bot/actions/workflows/ci.yml/badge.svg)](https://github.com/igophper/wechat-bot/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/igophper/wechat-bot.svg)](https://pkg.go.dev/github.com/igophper/wechat-bot)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

微信 iLink 协议的独立 Go 客户端，参考 [Tencent/openclaw-weixin](https://github.com/Tencent/openclaw-weixin)。支持扫码登录、接收消息、文本与媒体回复、图片解密和消息游标存储。**本项目不是腾讯官方 SDK，也不是 OpenClaw 插件。**

API 在 v0 系列中可能调整，建议固定依赖版本。

## 环境与使用范围

- 最低 Go **1.26.0**，建议使用受支持分支的最新补丁版。CI 覆盖 Go 1.26、1.27，并在 Linux、macOS、Windows 上运行测试。
- 需要能完成 iLink 扫码授权的微信账号，以及访问 iLink API 和微信 CDN 的网络环境。平台准入、账号限制、限流和消息有效期由微信控制。
- 目标用户 ID 来自入站消息的 `FromUserID`，不是微信号或手机号。回复通常需要该用户近期消息携带的 `ContextToken`；持有 BotToken 并不代表可以向任意用户主动推送。
- 支持发送文本、经纯文本排版的 Markdown、图片、文件和视频；支持接收文本、图片、文件、视频和语音元数据。图片可直接下载解密；语音文字来自服务端转写，不包含本地语音识别功能。
- 已在真实账号的已有登录态下验证文字收发；新扫码登录、验证码分支和媒体收发尚未经过真机确认。自动化回归使用模拟服务。

## 安装

在你的 Go 项目中安装：

```bash
go get github.com/igophper/wechat-bot@v0.1.0
```

## 运行完整示例

克隆仓库后，在项目根目录运行：

```bash
go run ./examples/basic
```

[完整示例](examples/basic/main.go) 会在首次启动时显示二维码，将登录凭据保存到 `./state/credentials.json`，并保存消息游标。之后优先加载本地凭据；没有本地凭据时，也可使用 `WECHAT_BOT_TOKEN` 和 `WECHAT_BOT_ID`（可选 `WECHAT_BASE_URL`）。环境变量凭据不会自动写入文件。

发送 `ping` 测试回复、`report` 查看表格排版、`help` 查看指令；也可以发送图片或语音。按 `Ctrl+C` 取消监听并等待正在执行的回调结束。发生处理错误时，示例返回非零退出码，不会把失败批次标记为已完成。

重新扫码登录或指定状态目录：

```bash
go run ./examples/basic --login
go run ./examples/basic --state-dir ./state
```

回复延迟排查可开启分段日志：

```bash
go run ./examples/basic --debug
```

日志记录 API 路径、连接与请求耗时、消息 ID 和回调耗时，不记录请求/响应正文、完整 URL 或凭据。`connection_wait`、`dns`、`tcp`、`tls` 用于检查建连；`response_wait` 是请求发出到首字节返回的时间；`message_age` 在服务端提供创建时间时显示消息年龄估计，受双方时钟误差影响，也包含等待前序回调的时间。长轮询在没有消息时保持等待是正常行为，不能把整个 `getupdates` 请求耗时直接当成消息延迟；应结合你实际发送消息的时间、`wechat poll received`、`wechat message dispatch` 和 `wechat example reply finished` 判断。`retry_in` 表示请求失败或会话恢复造成的退避。

基础示例直接回复，不在回复前同步发送输入态提示。`SendTyping` 会额外请求 `getconfig` 和（服务端支持时）`sendtyping`，同步调用会增加回复时间，也会推迟下一批消息的轮询；耗时业务可按需使用。

二维码过期后重新运行即可。如果平台要求验证码，基础示例会提示在终端输入，按回车提交。在自己的登录界面中可通过 `WithVerificationCodeHandler` 提供类型为 `func(context.Context) (string, error)` 的回调，作为 `WaitForQRConfirmation` 的可选参数。自定义回调应响应 context 取消，并避免记录验证码；基础示例的同步终端读取需要先结束输入才能返回。扫码重定向由 SDK 处理。

`state/` 已被 Git 忽略。凭据是明文敏感数据，请保存在自己控制的目录中，不要加入仓库或 Issue；自定义状态目录也需要自行加入忽略规则。

## 接入你的程序

以下完整程序复用基础示例保存的凭据，并回复文本消息：

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
        return fmt.Errorf("请先运行 examples/basic 扫码登录")
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
        return bot.SendText(ctx, msg.FromUserID, "收到："+msg.Text)
    })
    err = bot.Start(ctx)
    if errors.Is(err, context.Canceled) {
        return nil
    }
    return err
}
```

常用 API：

| 方法 | 用途 |
| --- | --- |
| `SendText(ctx, userID, text)` | 发送文本 |
| `SendMarkdown(ctx, userID, markdown)` | 将 Markdown 转成适合微信阅读的纯文本 |
| `SendImage(ctx, userID, data)` | 加密上传并发送图片 |
| `SendVideo(ctx, userID, data)` | 加密上传并发送视频 |
| `SendFile(ctx, userID, filename, data)` | 加密上传并发送文件 |
| `SendTyping(ctx, userID)` | 发送输入态提示；平台可能不提供此能力 |
| `msg.DownloadDecryptedImage(ctx)` | 获取图片内容、识别后的 MIME 类型和错误 |

所有发送和下载方法都应检查返回的错误。`InboundMessage` 提供可命名的媒体类型，`RawMessage` 为共享只读的 `json.RawMessage`，可用于读取尚未建模的协议字段；修改前请先复制。AES-128-ECB 与 MD5 用于兼容平台媒体协议，不应当作其他应用的加密方案。

## 消息处理与重启语义

- `MessageHandler` 返回 `error`；只有整批消息的全部回调成功，才保存新的游标。回调错误、panic、取消或存储失败会使 `Start` 返回错误，当前批次不被确认。
- 默认串行执行。设置 `HandlerConcurrency` 或 `WithHandlerConcurrency(n)` 可并行处理不同用户，同一用户的消息保持接收顺序。回调在一次 `Start` 开始时固定；运行中注册的变更在下一次启动生效。
- 回调必须响应 `ctx`，并为自己的网络或数据库操作设置超时。取消后 `Start` 会等待已启动的回调返回；忽略取消的回调会拖住退出。
- 对同一个客户端并发调用 `Start` 会返回 `ErrAlreadyStarted`。前一次 `Start` 返回后可以再次启动。
- 已产生副作用但游标尚未保存时，重启可能重放消息。请按业务需要使用消息 ID 和处理步骤实现幂等；SDK 不提供恰好一次交付，服务端消息保留时间也影响可恢复范围。

> **重要：不要对永久失败返回 `error`。** 游标是按「批」保存的。如果某条消息每次都让回调失败，`Start` 每次都会在它上面停下、游标永不前进，而 systemd 或 Kubernetes 的自动重启会不断重放同一批——机器人就此卡死，**后面的新消息一条也收不到**（同批中排在它后面的消息同样从未被处理）。
>
> 正确做法是在回调里分流：
>
> - **临时错误**（网络抖动、5xx、超时）→ 返回 `error`，让 `Start` 停下，重启后重放。
> - **永久错误**（数据本身不合法、4xx、内容超限）→ 先把消息可靠写入死信存储，**确认写入成功后返回 `nil`**，让游标继续前进。
> - 写死信都失败时，才返回 `error` 停下来——这时停机确实比丢消息更好。
>
> [完整示例](examples/basic/main.go) 实现了这个分流，可直接参考其 `permanent` 与 `record` 函数。判断清单要按自己的业务调整：判得太宽会丢消息，判得太窄会卡死。
- `OnExpired` 表示空游标恢复次数已耗尽，并不能证明凭据永久被吊销。检查返回错误和服务状态，必要时重新登录，不要无条件删除凭据。

默认 `MemoryBufStorage` 不跨进程保存游标；示例使用 `FileBufStorage`。文件存储适用于单进程，多个进程不应共享同一账号的监听与游标目录。可实现 `BufStorage` 接口接入自己的持久化存储。

## Context token 与通知

监听时，SDK 自动保存用户最近的 context token。使用回调传入的 `ctx` 回复时，SDK 保留该消息的 token，避免并发缓存淘汰影响回复。缓存默认最多 **1024 个用户**，按 LRU 淘汰，并且重启后清空；它不负责将 token 写入磁盘。没有有效上下文时，服务端可能拒绝发送。

如果需要在独立任务中发送通知，请由应用安全保存入站消息的 `FromUserID` 和 `ContextToken`，发送前调用 `SetContextToken(userID, token)` 恢复。可以用 `ContextToken(userID)` 查询缓存、`ForgetContextToken(userID)` 删除缓存。恢复 token 不会延长平台赋予它的有效期，也不能绕过平台发送限制。

## 配置与资源限制

可以直接设置 `Config`，或使用对应的 `With...` 选项。零值采用默认配置：

| 配置 | 默认值 | 说明 |
| --- | --- | --- |
| `HandlerConcurrency` | `1` | 同时执行回调的最大数量 |
| `MaxMediaBytes` | `32 MiB` | 媒体大小上限 |
| `MaxResponseBytes` | `1 MiB` | API 响应大小上限 |
| `MaxContextTokens` | `1024` | 内存中保存的用户上下文数量 |
| `SendTimeout` | `15s` | 单次消息发送超时 |
| `MediaTimeout` | `90s` | 媒体操作超时 |
| `TypingTimeout` | `8s` | 输入态操作超时 |
| `LongPollTimeout` | `35s` | 长轮询等待基线，HTTP 请求另加 5s 容差 |
| `BackoffInitial` / `BackoffMax` | `3s` / `60s` | 可重试轮询错误的退避范围，实际延迟在区间上半段随机抖动，不超过 `BackoffMax` |
| `EmptyBufExpiredThreshold` | `20` | 空游标会话恢复失败阈值 |
| `MediaHosts` | 空 | 媒体传输允许访问的主机白名单，见下 |

媒体在内存中加解密，提高并发或大小上限会增加内存用量。`BaseURL`、`CDNBaseURL`、`HTTPClient`、`Storage` 和 `Logger` 可替换，用于代理、测试或接入已有基础设施。

空内容不会被静默忽略：`SendText`、`SendMarkdown`、`SendImage`、`SendVideo` 在没有可发送内容时返回 `ErrEmptyMessage`，而不是返回 `nil` 假装成功。零字节文件是有效文档，`SendFile` 会照常上传发送。

### 服务端提供的 URL

媒体上传下载地址和扫码登录的跳转主机由服务端下发。SDK 在发起请求前校验这些地址，并对**每一跳重定向**重复校验：

- 配置 HTTPS 基址时，目标和重定向必须使用 HTTPS。显式配置 HTTP 基址会为该策略允许的主机放开 HTTP，并不限于基址主机。
- 默认拒绝未被显式信任的非公网 IP 字面地址（如 `127.0.0.1`、`10.0.0.1`、`169.254.169.254`）；配置的基址主机及白名单主机属于信任例外。
- 重定向最多 5 跳。
- `CDNBaseURL` 的主机始终允许；媒体签名 URL 允许携带 query 参数。

默认不限制公网主机，因为 CDN 节点域名可能与 `CDNBaseURL` 不同。要收紧到固定集合，使用 `WithMediaHosts("cdn.example", ...)`；登录跳转用 `WithLoginHosts(...)`。注意主机名不做 DNS 解析，指向内网的公网域名仍可访问——有这一层顾虑时请显式固定主机。

### 扫码验证码

微信要求验证码时，`WaitForQRConfirmation` 调用 `WithVerificationCodeHandler` 提供的回调。首次轮询立即发出；普通重试和提交新验证码前等待 `interval`，有限次的登录重定向立即跟进。因此验证码回调秒回也不会跳过重试间隔。默认最多接受 `DefaultVerificationAttempts`（3）次验证码输入，再次要求输入时返回 `ErrVerificationAttemptsExhausted`；可用 `WithVerificationAttempts(n)` 调整。这是**客户端**的上限，与服务端封禁的 `ErrVerificationBlocked` 是两个不同的错误。

SDK 未识别的非空扫码状态不会直接报错，而是通过 `onStatus` 回调上报并继续按 `interval` 轮询，避免服务端新增状态时登录直接中断。

## 贡献与反馈

欢迎提交中文或英文 Issue 和 Pull Request。开发检查与发布流程见 [贡献说明](CONTRIBUTING.md)，安全问题请按 [安全问题反馈](SECURITY.md) 私密报告，版本变化见 [更新记录](CHANGELOG.md)。

## 协议与许可

- 协议参考：[Tencent/openclaw-weixin](https://github.com/Tencent/openclaw-weixin)。服务端协议可能变化，欢迎携带脱敏复现报告兼容问题。
- 微信相关名称和商标归腾讯所有；使用时应遵守平台规则。
- [MIT License](LICENSE)。
