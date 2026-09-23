package wechat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"runtime/debug"
	"strconv"
	"sync"
	"time"
)

// OnMessage registers the processor used by the next Start call. Registration is
// safe during a running listener, but does not replace its snapshotted processor.
func (c *Client) OnMessage(handler MessageHandler) {
	c.handlersMu.Lock()
	defer c.handlersMu.Unlock()
	c.onMessage = handler
}

// OnExpired registers a callback for exhausted session recovery attempts.
// Like OnMessage, changes take effect on the next Start call.
func (c *Client) OnExpired(handler ExpiredHandler) {
	c.handlersMu.Lock()
	defer c.handlersMu.Unlock()
	c.onExpired = handler
}

// Start polls and processes messages until cancellation, a handler/storage error,
// or exhausted session recovery. It rejects concurrent Start calls on this client.
// A batch cursor is saved only after every item succeeds. Failed or interrupted
// batches may replay on restart: handlers must be idempotent. Start waits for its
// handlers to return; they must honor ctx and must not call Start recursively.
func (c *Client) Start(ctx context.Context) error {
	if !c.running.CompareAndSwap(false, true) {
		return ErrAlreadyStarted
	}
	defer c.running.Store(false)
	c.handlersMu.RLock()
	handler, expired := c.onMessage, c.onExpired
	c.handlersMu.RUnlock()
	if handler == nil {
		return ErrNoMessageHandler
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	storage := c.cfg.Storage
	cursor, err := storage.LoadBuf(ctx, c.BotID())
	if err != nil {
		return fmt.Errorf("load sync cursor: %w", err)
	}
	failures, expiredCount := 0, 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		resp, err := c.getUpdates(ctx, cursor)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failures++
			delay := c.backoff(failures)
			c.cfg.Logger.Warn("wechat poll failed", "error", err, "retry_in", delay)
			if err := waitContext(ctx, delay); err != nil {
				return err
			}
			continue
		}
		c.cfg.Logger.DebugContext(ctx, "wechat poll received", "messages", len(resp.Msgs), "ret", resp.Ret, "errcode", resp.ErrCode)
		if resp.Ret == ErrCodeSessionExpired || resp.ErrCode == ErrCodeSessionExpired {
			if cursor != "" {
				if err := storage.ClearBuf(ctx, c.BotID()); err != nil {
					return fmt.Errorf("reset sync cursor: %w", err)
				}
				cursor = ""
				expiredCount = 0
			} else {
				expiredCount++
				if expiredCount >= c.cfg.EmptyBufExpiredThreshold {
					if err := storage.ClearBuf(ctx, c.BotID()); err != nil {
						return errors.Join(ErrTokenRevoked, fmt.Errorf("clear sync cursor: %w", err))
					}
					if expired != nil {
						if err := c.invokeExpired(expired); err != nil {
							return errors.Join(ErrTokenRevoked, err)
						}
					}
					return ErrTokenRevoked
				}
			}
			failures++
			delay := c.backoff(failures)
			c.cfg.Logger.WarnContext(ctx, "wechat session recovery retry", "empty_cursor_failures", expiredCount, "retry_in", delay)
			if err := waitContext(ctx, delay); err != nil {
				return err
			}
			continue
		}
		if resp.Ret != 0 || resp.ErrCode != 0 {
			failures++
			delay := c.backoff(failures)
			// Do not log arbitrary server error text, which may contain credentials.
			c.cfg.Logger.Warn("wechat poll rejected", "ret", resp.Ret, "errcode", resp.ErrCode, "retry_in", delay)
			if err := waitContext(ctx, delay); err != nil {
				return err
			}
			continue
		}
		failures, expiredCount = 0, 0
		batchStarted := time.Now()
		err = c.processBatch(ctx, resp.Msgs, handler)
		c.cfg.Logger.DebugContext(ctx, "wechat batch handled", "messages", len(resp.Msgs), "elapsed", time.Since(batchStarted), "success", err == nil)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if resp.GetUpdatesBuf != "" && resp.GetUpdatesBuf != cursor {
			if err := storage.SaveBuf(ctx, c.BotID(), resp.GetUpdatesBuf); err != nil {
				return fmt.Errorf("save sync cursor: %w", err)
			}
			cursor = resp.GetUpdatesBuf
		}
	}
}

func (c *Client) getUpdates(ctx context.Context, buf string) (*wechatGetUpdatesResponse, error) {
	req := wechatGetUpdatesRequest{GetUpdatesBuf: buf, BaseInfo: wechatBaseInfo{ChannelVersion: Version}}
	pollCtx, cancel := context.WithTimeout(ctx, c.cfg.LongPollTimeout+5*time.Second)
	defer cancel()
	var resp wechatGetUpdatesResponse
	if err := c.doPost(pollCtx, "/ilink/bot/getupdates", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) processBatch(ctx context.Context, messages []wechatMessage, handler MessageHandler) error {
	if c.cfg.HandlerConcurrency == 1 {
		for _, m := range messages {
			if err := c.dispatchInbound(ctx, m, handler); err != nil {
				return err
			}
		}
		return nil
	}
	// Group each conversation before scheduling so two workers can never process
	// the same user's items concurrently. The next poll waits for this entire batch.
	indexes := make(map[string]int)
	groups := make([][]wechatMessage, 0)
	for _, m := range messages {
		if m.MessageType != MsgTypeUser || m.MessageState != MsgStateFinish {
			continue
		}
		index, ok := indexes[m.FromUserID]
		if !ok {
			index = len(groups)
			indexes[m.FromUserID] = index
			groups = append(groups, nil)
		}
		groups[index] = append(groups[index], m)
	}
	batchCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	jobs := make(chan []wechatMessage)
	var workers sync.WaitGroup
	for i := 0; i < min(c.cfg.HandlerConcurrency, len(groups)); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for group := range jobs {
				for _, m := range group {
					if err := c.dispatchInbound(batchCtx, m, handler); err != nil {
						cancel(err)
						return
					}
				}
			}
		}()
	}
enqueue:
	for _, group := range groups {
		select {
		case jobs <- group:
		case <-batchCtx.Done():
			break enqueue
		}
	}
	close(jobs)
	workers.Wait()
	return context.Cause(batchCtx)
}

func (c *Client) dispatchInbound(ctx context.Context, m wechatMessage, handler MessageHandler) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.MessageType != MsgTypeUser || m.MessageState != MsgStateFinish {
		c.cfg.Logger.DebugContext(ctx, "wechat message skipped", "message_id", m.MessageID, "message_type", m.MessageType, "message_state", m.MessageState)
		return nil
	}
	if c.cfg.Logger.Enabled(ctx, slog.LevelDebug) {
		attrs := []any{"message_id", m.MessageID, "items", len(m.ItemList)}
		if m.CreateTimeMS > 0 {
			// This is an estimate: it compares the server timestamp with the
			// local wall clock, and includes time queued behind other handlers.
			attrs = append(attrs, "message_age", time.Since(time.UnixMilli(m.CreateTimeMS)))
		}
		c.cfg.Logger.DebugContext(ctx, "wechat message dispatch", attrs...)
	}
	if m.FromUserID != "" && m.ContextToken != "" {
		c.saveContextToken(m.FromUserID, m.ContextToken)
		ctx = context.WithValue(ctx, conversationContextKey{}, conversationContext{client: c, userID: m.FromUserID, token: m.ContextToken})
	}
	raw := m.Raw
	if len(raw) == 0 {
		var err error
		raw, err = json.Marshal(m)
		if err != nil {
			return fmt.Errorf("encode inbound message: %w", err)
		}
	}
	for _, item := range m.ItemList {
		if err := ctx.Err(); err != nil {
			return err
		}
		inbound := &InboundMessage{
			MessageID: strconv.FormatUint(m.MessageID, 10), FromUserID: m.FromUserID, ToUserID: m.ToUserID,
			ItemType: ItemType(item.Type), ContextToken: m.ContextToken, RawMessage: raw, client: c,
			ImageItem: item.ImageItem, VoiceItem: item.VoiceItem, FileItem: item.FileItem, VideoItem: item.VideoItem,
		}
		switch ItemType(item.Type) {
		case ItemTypeText:
			if item.TextItem != nil {
				inbound.Text = item.TextItem.Text
			}
		case ItemTypeImage:
			if item.ImageItem != nil {
				inbound.Text = "[image]"
			}
		case ItemTypeVoice:
			if item.VoiceItem != nil {
				inbound.VoiceText = item.VoiceItem.Text
				inbound.Text = item.VoiceItem.Text
			}
		}
		// Preserve even unrecognized or non-transcribed items through RawMessage.
		if err := c.invokeHandler(ctx, handler, inbound); err != nil {
			return fmt.Errorf("process message %s: %w", inbound.MessageID, err)
		}
	}
	return nil
}

func (c *Client) invokeHandler(ctx context.Context, handler MessageHandler, msg *InboundMessage) (err error) {
	defer func() {
		if recover() != nil {
			stack := debug.Stack()
			c.cfg.Logger.Error("wechat message callback panicked", "stack", string(stack))
			err = &HandlerPanicError{Stack: stack}
		}
	}()
	return handler(ctx, msg)
}

func (c *Client) invokeExpired(handler ExpiredHandler) (err error) {
	defer func() {
		if recover() != nil {
			stack := debug.Stack()
			c.cfg.Logger.Error("wechat expiry callback panicked", "stack", string(stack))
			err = &HandlerPanicError{Stack: stack}
		}
	}()
	handler(c.BotID())
	return nil
}

// backoff returns the jittered delay before the next poll attempt. Jitter
// keeps many clients from retrying in lockstep after a shared outage.
func (c *Client) backoff(failures int) time.Duration {
	return c.jitter(c.backoffBase(failures))
}

// backoffBase is the un-jittered exponential delay, capped at BackoffMax.
func (c *Client) backoffBase(failures int) time.Duration {
	delay := c.cfg.BackoffInitial
	for i := 1; i < failures; i++ {
		if delay >= c.cfg.BackoffMax-delay {
			return c.cfg.BackoffMax
		}
		delay *= 2
	}
	return min(delay, c.cfg.BackoffMax)
}

// equalJitter picks a delay in [d/2, d], so a jittered retry still grows
// exponentially and never exceeds the configured maximum.
func equalJitter(d time.Duration) time.Duration {
	if d <= 1 {
		return d
	}
	half := d / 2
	return half + time.Duration(rand.Int64N(int64(d-half)+1))
}

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
