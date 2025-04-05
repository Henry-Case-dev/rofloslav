package types

// Message представляет собой унифицированную структуру сообщения,
// используемую для хранения в Qdrant и передачи в Gemini.
type Message struct {
	ID           int64           `json:"id"`                // Уникальный ID сообщения (обычно из Telegram)
	ChatID       int64           `json:"chat_id"`           // ID чата, к которому относится сообщение
	UserID       int64           `json:"user_id,omitempty"` // ID пользователя-отправителя
	UserName     string          `json:"user_name,omitempty"`
	FirstName    string          `json:"first_name,omitempty"`
	IsBot        bool            `json:"is_bot,omitempty"`
	Text         string          `json:"text"`      // Текст сообщения
	Timestamp    int             `json:"timestamp"` // Unix timestamp времени отправки
	ReplyToMsgID int             `json:"reply_to_msg_id,omitempty"`
	Role         string          `json:"role,omitempty"`     // Роль отправителя ("user", "model")
	Entities     []MessageEntity `json:"entities,omitempty"` // Сущности в тексте (ссылки, упоминания и т.д.)

	// Поле Embedding используется только при чтении из Qdrant/передаче в Gemini,
	// в JSON его обычно нет.
	// Embedding []float32 `json:"-"`
}

// MessageEntityType представляет тип сущности в сообщении (например, "mention", "url").
type MessageEntityType string

// Константы для типов сущностей (соответствуют Telegram)
const (
	MessageEntityTypeMention       MessageEntityType = "mention"
	MessageEntityTypeHashtag       MessageEntityType = "hashtag"
	MessageEntityTypeCashtag       MessageEntityType = "cashtag"
	MessageEntityTypeBotCommand    MessageEntityType = "bot_command"
	MessageEntityTypeURL           MessageEntityType = "url"
	MessageEntityTypeEmail         MessageEntityType = "email"
	MessageEntityTypePhoneNumber   MessageEntityType = "phone_number"
	MessageEntityTypeBold          MessageEntityType = "bold"
	MessageEntityTypeItalic        MessageEntityType = "italic"
	MessageEntityTypeUnderline     MessageEntityType = "underline"
	MessageEntityTypeStrikethrough MessageEntityType = "strikethrough"
	MessageEntityTypeSpoiler       MessageEntityType = "spoiler"
	MessageEntityTypeCode          MessageEntityType = "code"
	MessageEntityTypePre           MessageEntityType = "pre"
	MessageEntityTypeTextLink      MessageEntityType = "text_link"
	MessageEntityTypeTextMention   MessageEntityType = "text_mention"
	MessageEntityTypeCustomEmoji   MessageEntityType = "custom_emoji"
)

// MessageEntity представляет сущность в тексте сообщения (аналог tgbotapi.MessageEntity).
type MessageEntity struct {
	Type          string `json:"type"` // Тип сущности (используем string для совместимости)
	Offset        int    `json:"offset"`
	Length        int    `json:"length"`
	URL           string `json:"url,omitempty"`             // Для text_link
	User          *User  `json:"user,omitempty"`            // Для text_mention
	Language      string `json:"language,omitempty"`        // Для pre
	CustomEmojiID string `json:"custom_emoji_id,omitempty"` // Для custom_emoji
}

// User представляет пользователя (аналог tgbotapi.User).
type User struct {
	ID           int64  `json:"id"`
	IsBot        bool   `json:"is_bot,omitempty"`
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name,omitempty"`
	UserName     string `json:"username,omitempty"`
	LanguageCode string `json:"language_code,omitempty"`
	// Можно добавить другие поля при необходимости
}
