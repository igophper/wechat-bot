package wechat

import "encoding/json"

type wechatBaseInfo struct {
	ChannelVersion string `json:"channel_version,omitempty"`
}

type wechatGetUpdatesRequest struct {
	GetUpdatesBuf string         `json:"get_updates_buf"`
	BaseInfo      wechatBaseInfo `json:"base_info"`
}

type wechatGetUpdatesResponse struct {
	Ret           int             `json:"ret"`
	ErrCode       int             `json:"errcode,omitempty"`
	ErrMsg        string          `json:"errmsg,omitempty"`
	Msgs          []wechatMessage `json:"msgs"`
	GetUpdatesBuf string          `json:"get_updates_buf"`
}

type wechatMessage struct {
	Raw          json.RawMessage `json:"-"`
	Seq          uint64          `json:"seq,omitempty"`
	MessageID    uint64          `json:"message_id,omitempty"`
	CreateTimeMS int64           `json:"create_time_ms,omitempty"`
	FromUserID   string          `json:"from_user_id"`
	ToUserID     string          `json:"to_user_id"`
	MessageType  int             `json:"message_type"`
	MessageState int             `json:"message_state"`
	ItemList     []wechatItem    `json:"item_list"`
	ContextToken string          `json:"context_token"`
}

type wechatItem struct {
	Type      int              `json:"type"`
	TextItem  *wechatTextItem  `json:"text_item,omitempty"`
	ImageItem *wechatImageItem `json:"image_item,omitempty"`
	VoiceItem *wechatVoiceItem `json:"voice_item,omitempty"`
	VideoItem *wechatVideoItem `json:"video_item,omitempty"`
	FileItem  *wechatFileItem  `json:"file_item,omitempty"`
}

type wechatVideoItem = VideoItem

type wechatFileItem = FileItem

type wechatTextItem struct {
	Text string `json:"text"`
}

type wechatVoiceItem = VoiceItem

type wechatSendRequest struct {
	Msg      wechatSendMsg  `json:"msg"`
	BaseInfo wechatBaseInfo `json:"base_info"`
}

type wechatSendMsg struct {
	FromUserID   string       `json:"from_user_id"`
	ToUserID     string       `json:"to_user_id"`
	ClientID     string       `json:"client_id"`
	MessageType  int          `json:"message_type"`
	MessageState int          `json:"message_state"`
	ItemList     []wechatItem `json:"item_list"`
	ContextToken string       `json:"context_token,omitempty"`
}

type wechatSendResponse struct {
	Ret    int    `json:"ret"`
	ErrMsg string `json:"errmsg,omitempty"`
}

type wechatGetConfigRequest struct {
	ILinkUserID  string         `json:"ilink_user_id"`
	ContextToken string         `json:"context_token,omitempty"`
	BaseInfo     wechatBaseInfo `json:"base_info"`
}

type wechatGetConfigResponse struct {
	Ret          int    `json:"ret"`
	ErrMsg       string `json:"errmsg,omitempty"`
	TypingTicket string `json:"typing_ticket,omitempty"`
}

type wechatSendTypingRequest struct {
	ILinkUserID  string         `json:"ilink_user_id"`
	TypingTicket string         `json:"typing_ticket"`
	Status       int            `json:"status"`
	BaseInfo     wechatBaseInfo `json:"base_info"`
}

type wechatSendTypingResponse struct {
	Ret    int    `json:"ret"`
	ErrMsg string `json:"errmsg,omitempty"`
}

type wechatImageItem = ImageItem

type wechatMediaInfo = MediaInfo

type wechatGetUploadURLRequest struct {
	FileKey     string         `json:"filekey"`
	MediaType   int            `json:"media_type"`
	ToUserID    string         `json:"to_user_id"`
	RawSize     int            `json:"rawsize"`
	RawFileMD5  string         `json:"rawfilemd5"`
	FileSize    int            `json:"filesize"`
	NoNeedThumb bool           `json:"no_need_thumb"`
	AESKey      string         `json:"aeskey"`
	BaseInfo    wechatBaseInfo `json:"base_info"`
}

type wechatGetUploadURLResponse struct {
	Ret           int    `json:"ret"`
	ErrMsg        string `json:"errmsg,omitempty"`
	UploadParam   string `json:"upload_param"`
	UploadFullURL string `json:"upload_full_url,omitempty"`
}

// UnmarshalJSON preserves unknown protocol fields for callers inspecting RawMessage.
func (m *wechatMessage) UnmarshalJSON(data []byte) error {
	type plain wechatMessage
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*m = wechatMessage(decoded)
	m.Raw = append(json.RawMessage(nil), data...)
	return nil
}
