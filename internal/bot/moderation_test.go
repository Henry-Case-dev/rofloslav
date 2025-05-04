package bot

import (
	"context"
	"testing"
	"time"

	"github.com/Henry-Case-dev/rofloslav/internal/config"
	"github.com/Henry-Case-dev/rofloslav/internal/storage"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// fakeLLM всегда возвращает положительный вердикт
type fakeLLM struct{}

func (f *fakeLLM) GenerateArbitraryResponse(systemPrompt, contextText string) (string, error) {
	return "ПОЛОЖИТЕЛЬНО: нарушение обнаружено", nil
}
func (f *fakeLLM) GenerateResponseFromTextContext(systemPrompt, contextText string) (string, error) {
	return "", nil
}
func (f *fakeLLM) GenerateResponse(systemPrompt string, history []*tgbotapi.Message, lastMessage *tgbotapi.Message) (string, error) {
	return "", nil
}
func (f *fakeLLM) TranscribeAudio(audioData []byte, mimeType string) (string, error) {
	return "", nil
}
func (f *fakeLLM) EmbedContent(text string) ([]float32, error) {
	return nil, nil
}
func (f *fakeLLM) GenerateContentWithImage(ctx context.Context, systemPrompt string, imageData []byte, caption string) (string, error) {
	return "", nil
}
func (f *fakeLLM) Close() error { return nil }

// fakeStorage возвращает заранее определённые сообщения
type fakeStorage struct {
	messages map[int64][]*tgbotapi.Message
}

func (fs *fakeStorage) GetMessagesSince(ctx context.Context, chatID, userID int64, since time.Time, limit int) ([]*tgbotapi.Message, error) {
	return fs.messages[chatID], nil
}
func (fs *fakeStorage) GetStatus(chatID int64) string { return "ok" }
func (fs *fakeStorage) Close() error                  { return nil }

// Остальные методы интерфейса не используются в тесте
func (fs *fakeStorage) AddMessage(_ int64, _ *tgbotapi.Message)                 {}
func (fs *fakeStorage) GetMessages(_ int64, _ int) ([]*tgbotapi.Message, error) { return nil, nil }
func (fs *fakeStorage) LoadChatHistory(_ int64) ([]*tgbotapi.Message, error)    { return nil, nil }
func (fs *fakeStorage) SaveChatHistory(_ int64) error                           { return nil }
func (fs *fakeStorage) ClearChatHistory(_ int64) error                          { return nil }
func (fs *fakeStorage) AddMessagesToContext(_ int64, _ []*tgbotapi.Message)     {}
func (fs *fakeStorage) GetAllChatIDs() ([]int64, error)                         { return nil, nil }
func (fs *fakeStorage) GetUserProfile(_ int64, _ int64) (*storage.UserProfile, error) {
	return nil, nil
}
func (fs *fakeStorage) SetUserProfile(_ *storage.UserProfile) error                { return nil }
func (fs *fakeStorage) GetAllUserProfiles(_ int64) ([]*storage.UserProfile, error) { return nil, nil }
func (fs *fakeStorage) GetChatSettings(_ int64) (*storage.ChatSettings, error)     { return nil, nil }
func (fs *fakeStorage) SetChatSettings(_ *storage.ChatSettings) error              { return nil }
func (fs *fakeStorage) UpdateDirectLimitEnabled(_ int64, _ bool) error             { return nil }
func (fs *fakeStorage) UpdateDirectLimitCount(_ int64, _ int) error                { return nil }
func (fs *fakeStorage) UpdateDirectLimitDuration(_ int64, _ time.Duration) error   { return nil }
func (fs *fakeStorage) UpdateVoiceTranscriptionEnabled(_ int64, _ bool) error      { return nil }
func (fs *fakeStorage) UpdateSrachAnalysisEnabled(_ int64, _ bool) error           { return nil }
func (fs *fakeStorage) SearchRelevantMessages(_ int64, _ string, _ int) ([]*tgbotapi.Message, error) {
	return nil, nil
}
func (fs *fakeStorage) GetReplyChain(_ context.Context, _ int64, _ int, _ int) ([]*tgbotapi.Message, error) {
	return nil, nil
}
func (fs *fakeStorage) ResetAutoBioTimestamps(_ int64) error         { return nil }
func (fs *fakeStorage) GetTotalMessagesCount(_ int64) (int64, error) { return 0, nil }
func (fs *fakeStorage) FindMessagesWithoutEmbedding(_ int64, _ int, _ []int) ([]storage.MongoMessage, error) {
	return nil, nil
}
func (fs *fakeStorage) UpdateMessageEmbedding(_ int64, _ int, _ []float32) error { return nil }

func TestModeration_PurgeTaskScheduled(t *testing.T) {
	chatID := int64(100)
	userID := int64(200)
	// Настроим правило: любое нарушение по ключевому слову "badword"
	rule := config.ModerationRule{
		RuleName:       "test-rule",
		ParsedChatID:   0, // глобально
		ParsedUserID:   0,
		Keywords:       []string{"badword"},
		Punishment:     config.PunishPurge,
		NotifyUser:     false,
		NotifyChat:     false,
		PunishmentNote: "",
	}
	cfg := &config.Config{
		ModInterval:         1,
		ModPurgeDuration:    500 * time.Millisecond,
		ModCheckAdminRights: false,
		ModDefaultNotify:    false,
		ModRules:            []config.ModerationRule{rule},
	}
	// Подготовка фейкового хранилища с одним сообщением, содержащим ключевое слово
	fs := &fakeStorage{messages: map[int64][]*tgbotapi.Message{
		chatID: {{Chat: &tgbotapi.Chat{ID: chatID}, From: &tgbotapi.User{ID: userID}, Text: "this contains badword here"}},
	}}
	// Фейковый бот
	bot := &Bot{llm: &fakeLLM{}, storage: fs, config: cfg}
	ms := NewModerationService(bot)
	// Включаем модерацию для чата
	ms.mutex.Lock()
	ms.activeChats = map[int64]bool{chatID: true}
	ms.mutex.Unlock()
	// Запускаем ручной вызов обработки батча
	ms.processMessageBatch(chatID, fs.messages[chatID])
	// Проверяем, что запланирована задача purge
	ms.mutex.RLock()
	defer ms.mutex.RUnlock()
	if ms.activePurges[chatID] == nil {
		t.Fatal("ожидали запись activePurges для чата")
	}
	if _, exists := ms.activePurges[chatID][userID]; !exists {
		t.Errorf("ожидали активную задачу очистки для пользователя %d", userID)
	}
}

// Инструкция по запуску тестов:
// В терминале из корня проекта выполните:
//   go test ./internal/bot -timeout 5s
// или запустите все тесты:
//   go test ./... -timeout 10s
