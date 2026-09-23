// Command basic demonstrates QR login and a bot that replies to incoming messages.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/igophper/wechat-bot"
)

func main() {
	if err := run(); err != nil {
		log.Println(err)
		os.Exit(1)
	}
}

func run() error {
	forceLogin := flag.Bool("login", false, "重新扫码登录并替换已保存的凭据")
	stateDir := flag.String("state-dir", "./state", "保存凭据和消息游标的目录")
	debug := flag.Bool("debug", false, "记录 API 请求阶段及消息处理耗时，不记录正文或凭据")
	flag.Parse()
	logger := slog.Default()
	if *debug {
		log.SetFlags(log.LstdFlags | log.Lmicroseconds)
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	credsFile := filepath.Join(*stateDir, "credentials.json")
	creds, err := credentials(ctx, credsFile, *forceLogin)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	bot, err := wechat.NewClient(wechat.Config{
		BotToken:    creds.BotToken,
		ILinkBotID:  creds.ILinkBotID,
		ILinkUserID: creds.ILinkUserID,
		BaseURL:     creds.BaseURL,
		Storage:     wechat.NewFileBufStorage(*stateDir),
		Logger:      logger,
	})
	if err != nil {
		return fmt.Errorf("初始化客户端: %w", err)
	}

	deadLetter := filepath.Join(*stateDir, "dead-letter.jsonl")
	bot.OnMessage(func(ctx context.Context, msg *wechat.InboundMessage) error {
		// 分流是这个示例最值得抄的部分。Start 在 handler 返回错误时会停下，
		// 并且不提交本批游标；如果每次重启都在同一条消息上失败，机器人会卡死
		// 在这条消息上，再也收不到新消息。所以只有「重试有意义」的错误才返回。
		err := reply(ctx, bot, msg)
		switch {
		case err == nil:
			return nil

		case ctx.Err() != nil:
			// 正在关闭：交回给 Start，这批消息重启后重放。
			return err

		case permanent(err):
			// 永久失败，重试多少次都一样。先可靠记录下来，记录成功才返回 nil，
			// 让游标继续前进、后面的消息得以处理。
			if rec := record(deadLetter, msg, err); rec != nil {
				// 连记录都失败了，这才是真的该停下来的情况。
				return fmt.Errorf("记录失败消息 %s: %w (原始错误: %v)", msg.MessageID, rec, err)
			}
			log.Printf("已跳过无法处理的消息 %s 并记入 %s: %v", msg.MessageID, deadLetter, err)
			return nil

		default:
			// 临时故障（网络、5xx、超时）：返回错误让 Start 停下，
			// 由进程管理器重启后重放这一批。
			return fmt.Errorf("处理消息 %s: %w", msg.MessageID, err)
		}
	})
	bot.OnExpired(func(_ string) {
		log.Println("会话恢复次数已耗尽。请检查服务状态，必要时用 --login 重新扫码；已保存凭据未删除。")
	})

	fmt.Println("机器人已启动；发送 help 查看命令，按 Ctrl+C 退出。")
	if err := bot.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("监听停止（当前批次可能在重启后重放）: %w", err)
	}
	return nil
}

// reply 处理单条消息。它只负责业务逻辑，错误分流交给 OnMessage。
func reply(ctx context.Context, bot *wechat.Client, msg *wechat.InboundMessage) (err error) {
	started := time.Now()
	defer func() {
		bot.Logger().DebugContext(ctx, "wechat example reply finished", "message_id", msg.MessageID, "elapsed", time.Since(started), "success", err == nil)
	}()
	// 只记录元数据，避免默认把聊天内容或 context token 写进日志。
	log.Printf("收到消息: id=%s type=%d", msg.MessageID, msg.ItemType)
	// 此示例直接回复，省去同步 SendTyping 的 getconfig / sendtyping 请求。
	// 耗时业务可以自行使用输入态，但不要让可选提示阻塞即时回复。
	if msg.IsImage() {
		data, contentType, err := msg.DownloadDecryptedImage(ctx)
		if err != nil {
			return fmt.Errorf("下载图片: %w", err)
		}
		return bot.SendText(ctx, msg.FromUserID,
			fmt.Sprintf("已接收图片：%s，%d 字节", contentType, len(data)))
	}
	if msg.IsVoice() {
		if msg.VoiceText == "" {
			return bot.SendText(ctx, msg.FromUserID, "收到语音，服务端未提供转写文本。")
		}
		return bot.SendText(ctx, msg.FromUserID, "语音转写："+msg.VoiceText)
	}
	switch msg.Text {
	case "ping":
		return bot.SendText(ctx, msg.FromUserID, "pong")
	case "help":
		return bot.SendText(ctx, msg.FromUserID,
			"发送 ping 测试连接，report 查看表格排版，图片测试解密，或发送文本获取回复。")
	case "report":
		return bot.SendMarkdown(ctx, msg.FromUserID, `## 示例监控报表
| 服务 | 状态 | 延迟 |
| --- | --- | --- |
| API | 正常 | 22ms |
| 缓存 | 正常 | 3ms |`)
	default:
		if msg.Text == "" {
			return bot.SendText(ctx, msg.FromUserID, "收到消息；此示例只处理文本、图片和语音转写。")
		}
		return bot.SendText(ctx, msg.FromUserID, "收到："+msg.Text)
	}
}

// permanent 判断一个错误是否重试也不会变好。这份清单要按你自己的业务调整：
// 判断得太宽会丢消息，判断得太窄会让机器人卡在一条坏消息上。
func permanent(err error) bool {
	switch {
	case errors.Is(err, wechat.ErrEmptyMessage),
		errors.Is(err, wechat.ErrNoMediaInfo),
		errors.Is(err, wechat.ErrInvalidPadding),
		errors.Is(err, wechat.ErrInvalidBlockSize),
		errors.Is(err, wechat.ErrMediaTooLarge),
		errors.Is(err, wechat.ErrBlockedURL):
		return true
	}
	// 4xx 表示这个请求本身有问题，重发同样的内容不会成功；5xx 留给重试。
	var httpErr *wechat.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode >= 400 && httpErr.StatusCode < 500 &&
			httpErr.StatusCode != http.StatusRequestTimeout &&
			httpErr.StatusCode != http.StatusTooManyRequests
	}
	return false
}

// record 把一条处理不了的消息追加到死信文件。返回 nil 才代表已经可靠落盘，
// 调用方据此决定能否放行游标。
func record(path string, msg *wechat.InboundMessage, cause error) error {
	entry, err := json.Marshal(map[string]any{
		"time":       time.Now().Format(time.RFC3339),
		"message_id": msg.MessageID,
		"from":       msg.FromUserID,
		"item_type":  msg.ItemType,
		"error":      cause.Error(),
		"raw":        json.RawMessage(msg.RawMessage),
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(entry, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	// 关闭前先落盘，避免进程随后退出时记录丢失、游标却已经前进。
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func credentials(ctx context.Context, filename string, forceLogin bool) (*wechat.Credentials, error) {
	if !forceLogin {
		creds, err := wechat.LoadCredentials(filename)
		if err != nil {
			return nil, fmt.Errorf("读取凭据（使用 --login 可重新扫码）: %w", err)
		}
		if creds != nil {
			return creds, nil
		}
		token, botID := os.Getenv("WECHAT_BOT_TOKEN"), os.Getenv("WECHAT_BOT_ID")
		if token != "" || botID != "" {
			if token == "" || botID == "" {
				return nil, errors.New("请同时设置 WECHAT_BOT_TOKEN 和 WECHAT_BOT_ID")
			}
			return &wechat.Credentials{
				BotToken: token, ILinkBotID: botID,
				BaseURL: os.Getenv("WECHAT_BASE_URL"),
			}, nil
		}
	}

	qr, err := wechat.GetQRCode(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取登录二维码: %w", err)
	}
	fmt.Println("请使用微信扫描二维码并确认登录：")
	qr.PrintTerminal()
	creds, err := wechat.WaitForQRConfirmation(ctx, qr.QRCode, 2*time.Second, 5*time.Minute,
		func(status string) {
			if status == wechat.QRStatusScanned {
				fmt.Println("二维码已扫描，请在微信确认登录。")
			}
		},
		// 微信要求验证码时，从终端读取。默认最多尝试 DefaultVerificationAttempts 次，
		// 之后返回 ErrVerificationAttemptsExhausted。
		wechat.WithVerificationCodeHandler(func(ctx context.Context) (string, error) {
			fmt.Print("微信要求输入验证码，请输入后回车：")
			var code string
			if _, err := fmt.Scanln(&code); err != nil {
				return "", fmt.Errorf("读取验证码: %w", err)
			}
			return code, nil
		}))
	if err != nil {
		return nil, fmt.Errorf("扫码登录: %w", err)
	}
	if err := wechat.SaveCredentials(filename, creds); err != nil {
		return nil, fmt.Errorf("保存凭据: %w", err)
	}
	fmt.Printf("凭据已保存至 %s。请勿提交或分享该文件。\n", filename)
	return creds, nil
}
