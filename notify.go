package wechat

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// SendText sends a plain text message to a user. Blank text returns
// ErrEmptyMessage rather than reporting success without delivering anything.
func (c *Client) SendText(ctx context.Context, toUserID, text string) error {
	if strings.TrimSpace(text) == "" {
		return ErrEmptyMessage
	}
	return c.sendSingleItem(ctx, toUserID, wechatItem{
		Type:     int(ItemTypeText),
		TextItem: &wechatTextItem{Text: text},
	})
}

// SendMarkdown formats a markdown string into a WeChat-compatible plain text
// message (flattening tables and stripping unrendered markdown tags) and sends it.
// Markdown that formats to nothing returns ErrEmptyMessage.
func (c *Client) SendMarkdown(ctx context.Context, toUserID, markdown string) error {
	formatted := FormatForWeChat(markdown)
	if strings.TrimSpace(formatted) == "" {
		return ErrEmptyMessage
	}
	return c.SendText(ctx, toUserID, formatted)
}

// SendImage uploads an image to the WeChat CDN and sends it to the user.
// Empty data returns ErrEmptyMessage. MediaTimeout bounds the whole operation,
// so the final submission shares whatever remains after the upload.
func (c *Client) SendImage(ctx context.Context, toUserID string, data []byte) error {
	if len(data) == 0 {
		return ErrEmptyMessage
	}
	mediaCtx, cancel := context.WithTimeout(ctx, c.cfg.MediaTimeout)
	defer cancel()

	uploaded, err := c.UploadToCDN(mediaCtx, toUserID, data, CDNMediaTypeImage)
	if err != nil {
		return fmt.Errorf("upload image: %w", err)
	}

	mediaInfo := &wechatMediaInfo{
		EncryptQueryParam: uploaded.DownloadParam,
		AESKey:            base64.StdEncoding.EncodeToString([]byte(uploaded.AESKeyHex)),
		EncryptType:       EncryptTypeAES128ECB,
	}

	return c.sendSingleItem(mediaCtx, toUserID, wechatItem{
		Type: int(ItemTypeImage),
		ImageItem: &wechatImageItem{
			Media:   mediaInfo,
			MidSize: uploaded.CipherSize,
		},
	})
}

// SendFile uploads a document/file to the WeChat CDN and sends it to the user.
// Unlike SendImage and SendVideo, a zero-byte file is a valid document and is
// uploaded and sent. MediaTimeout bounds the whole operation, so the final
// submission shares whatever remains after the upload.
func (c *Client) SendFile(ctx context.Context, toUserID, filename string, data []byte) error {
	if filename == "" {
		filename = "file"
	}
	mediaCtx, cancel := context.WithTimeout(ctx, c.cfg.MediaTimeout)
	defer cancel()

	uploaded, err := c.UploadToCDN(mediaCtx, toUserID, data, CDNMediaTypeFile)
	if err != nil {
		return fmt.Errorf("upload file: %w", err)
	}

	mediaInfo := &wechatMediaInfo{
		EncryptQueryParam: uploaded.DownloadParam,
		AESKey:            base64.StdEncoding.EncodeToString([]byte(uploaded.AESKeyHex)),
		EncryptType:       EncryptTypeAES128ECB,
	}

	return c.sendSingleItem(mediaCtx, toUserID, wechatItem{
		Type: int(ItemTypeFile),
		FileItem: &wechatFileItem{
			Media:    mediaInfo,
			FileName: filename,
			Len:      strconv.Itoa(uploaded.FileSize),
		},
	})
}

// SendVideo uploads a video to the WeChat CDN and sends it to the user.
// Empty data returns ErrEmptyMessage. MediaTimeout bounds the whole operation,
// so the final submission shares whatever remains after the upload.
func (c *Client) SendVideo(ctx context.Context, toUserID string, data []byte) error {
	if len(data) == 0 {
		return ErrEmptyMessage
	}
	mediaCtx, cancel := context.WithTimeout(ctx, c.cfg.MediaTimeout)
	defer cancel()

	uploaded, err := c.UploadToCDN(mediaCtx, toUserID, data, CDNMediaTypeVideo)
	if err != nil {
		return fmt.Errorf("upload video: %w", err)
	}

	mediaInfo := &wechatMediaInfo{
		EncryptQueryParam: uploaded.DownloadParam,
		AESKey:            base64.StdEncoding.EncodeToString([]byte(uploaded.AESKeyHex)),
		EncryptType:       EncryptTypeAES128ECB,
	}

	return c.sendSingleItem(mediaCtx, toUserID, wechatItem{
		Type: int(ItemTypeVideo),
		VideoItem: &wechatVideoItem{
			Media:     mediaInfo,
			VideoSize: uploaded.CipherSize,
		},
	})
}

// SendTyping sends a typing indicator ("对方正在输入...") to the specified user.
func (c *Client) SendTyping(ctx context.Context, toUserID string) error {
	if toUserID == "" {
		return nil
	}

	typingCtx, cancel := context.WithTimeout(ctx, c.cfg.TypingTimeout)
	defer cancel()

	contextToken := c.contextTokenFor(ctx, toUserID)
	cfgBody := wechatGetConfigRequest{
		ILinkUserID:  toUserID,
		ContextToken: contextToken,
		BaseInfo:     wechatBaseInfo{ChannelVersion: Version},
	}

	var cfgResp wechatGetConfigResponse
	if err := c.doPost(typingCtx, "/ilink/bot/getconfig", cfgBody, &cfgResp); err != nil {
		return fmt.Errorf("getconfig: %w", err)
	}
	if cfgResp.Ret != 0 {
		return &APIError{Endpoint: "/ilink/bot/getconfig", Ret: cfgResp.Ret, ErrMsg: cfgResp.ErrMsg}
	}
	if cfgResp.TypingTicket == "" {
		return nil // Typing indicator disabled for this user/bot
	}

	typingBody := wechatSendTypingRequest{
		ILinkUserID:  toUserID,
		TypingTicket: cfgResp.TypingTicket,
		Status:       TypingStatusTyping,
		BaseInfo:     wechatBaseInfo{ChannelVersion: Version},
	}
	var typingResp wechatSendTypingResponse
	if err := c.doPost(typingCtx, "/ilink/bot/sendtyping", typingBody, &typingResp); err != nil {
		return fmt.Errorf("sendtyping: %w", err)
	}
	if typingResp.Ret != 0 {
		return &APIError{Endpoint: "/ilink/bot/sendtyping", Ret: typingResp.Ret, ErrMsg: typingResp.ErrMsg}
	}
	return nil
}

func (c *Client) sendSingleItem(ctx context.Context, toUserID string, item wechatItem) error {
	sendCtx, cancel := context.WithTimeout(ctx, c.cfg.SendTimeout)
	defer cancel()

	contextToken := c.contextTokenFor(ctx, toUserID)
	req := wechatSendRequest{
		Msg: wechatSendMsg{
			FromUserID:   c.cfg.ILinkBotID,
			ToUserID:     toUserID,
			ClientID:     GenerateUUID(),
			MessageType:  MsgTypeBot,
			MessageState: MsgStateFinish,
			ItemList:     []wechatItem{item},
			ContextToken: contextToken,
		},
		BaseInfo: wechatBaseInfo{ChannelVersion: Version},
	}

	var resp wechatSendResponse
	if err := c.doPost(sendCtx, "/ilink/bot/sendmessage", req, &resp); err != nil {
		return fmt.Errorf("sendmessage: %w", err)
	}
	if resp.Ret != 0 {
		return &APIError{Endpoint: "/ilink/bot/sendmessage", Ret: resp.Ret, ErrMsg: resp.ErrMsg}
	}
	return nil
}
