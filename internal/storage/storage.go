package storage

import (
	// Добавим для sql.NullString в Postgres

	// Keep for Postgres potentially

	// Keep for Postgres potentially

	"fmt"
	"log"
	"os"
	"path/filepath" // Added for checking nil pointer inside EnsureIndexes
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	_ "github.com/lib/pq"                        // Postgres driver, keep if postgres is still an option
	"go.mongodb.org/mongo-driver/bson/primitive" // Добавим для MongoDB ID
)

// UserProfile содержит информацию о пользователе чата.
type UserProfile struct {
	ID       primitive.ObjectID `bson:"_id,omitempty"`            // ID для MongoDB
	ChatID   int64              `bson:"chat_id" db:"chat_id"`     // ID чата, к которому привязан профиль
	UserID   int64              `bson:"user_id" db:"user_id"`     // ID пользователя Telegram
	Username string             `bson:"username" db:"username"`   // Никнейм Telegram (@username)
	Alias    string             `bson:"alias" db:"alias"`         // Прозвище / Короткое имя (ранее FirstName)
	Gender   string             `bson:"gender" db:"gender"`       // Пол (ранее LastName)
	RealName string             `bson:"real_name" db:"real_name"` // Реальное имя (если известно)
	Bio      string             `bson:"bio" db:"bio"`             // Редактируемое описание/бэкграунд
	LastSeen time.Time          `bson:"last_seen" db:"last_seen"` // Время последнего сообщения (для актуальности)
	// Можно добавить другие поля по необходимости
	CreatedAt time.Time `bson:"created_at" db:"created_at"` // Время создания записи
	UpdatedAt time.Time `bson:"updated_at" db:"updated_at"` // Время последнего обновления
}

// ChatHistoryStorage определяет общий интерфейс для хранения истории сообщений и профилей пользователей.
// Включает методы для работы с настройками чата и долгосрочной памятью.
type ChatHistoryStorage interface {
	// === Методы для истории сообщений ===
	AddMessage(chatID int64, message *tgbotapi.Message)
	GetMessages(chatID int64) []*tgbotapi.Message
	GetMessagesSince(chatID int64, since time.Time) []*tgbotapi.Message
	LoadChatHistory(chatID int64) ([]*tgbotapi.Message, error)
	SaveChatHistory(chatID int64) error
	ClearChatHistory(chatID int64) error
	AddMessagesToContext(chatID int64, messages []*tgbotapi.Message)
	GetAllChatIDs() ([]int64, error)

	// === Методы для профилей пользователей ===
	// GetUserProfile возвращает профиль пользователя для конкретного чата.
	// Возвращает nil, nil если профиль не найден.
	GetUserProfile(chatID int64, userID int64) (*UserProfile, error)

	// SetUserProfile создает или обновляет профиль пользователя для конкретного чата.
	SetUserProfile(profile *UserProfile) error

	// GetAllUserProfiles возвращает все профили пользователей для указанного чата.
	GetAllUserProfiles(chatID int64) ([]*UserProfile, error)

	// === Методы для настроек чатов ===
	// GetChatSettings возвращает настройки для указанного чата.
	GetChatSettings(chatID int64) (*ChatSettings, error)
	// SetChatSettings сохраняет настройки для указанного чата.
	SetChatSettings(settings *ChatSettings) error

	// --- Методы для обновления отдельных настроек лимитов ---
	UpdateDirectLimitEnabled(chatID int64, enabled bool) error
	UpdateDirectLimitCount(chatID int64, count int) error
	UpdateDirectLimitDuration(chatID int64, duration time.Duration) error

	// === Общие методы ===
	// Close закрывает соединение с хранилищем.
	Close() error

	// GetStatus возвращает строку с текущим статусом хранилища (тип, подключение, кол-во сообщений и т.д.)
	GetStatus(chatID int64) string

	// === Методы для долгосрочной памяти ===
	// SearchRelevantMessages ищет сообщения, семантически близкие к queryText, используя векторный поиск.
	// Возвращает до k наиболее релевантных сообщений.
	SearchRelevantMessages(chatID int64, queryText string, k int) ([]*tgbotapi.Message, error)
}

// FileStorage реализует ChatHistoryStorage с использованием файлов.
// Переименовано из Storage.
type FileStorage struct {
	messages      map[int64][]*tgbotapi.Message
	userProfiles  map[int64]map[int64]*UserProfile // map[chatID]map[userID]UserProfile
	contextWindow int
	mutex         sync.RWMutex
	autoSave      bool
}

// Убедимся, что FileStorage реализует интерфейс ChatHistoryStorage.
var _ ChatHistoryStorage = (*FileStorage)(nil)

// NewFileStorage создает новое файловое хранилище.
// Переименовано из New.
func NewFileStorage(contextWindow int, autoSave bool) *FileStorage {
	return &FileStorage{
		messages:      make(map[int64][]*tgbotapi.Message),
		userProfiles:  make(map[int64]map[int64]*UserProfile), // Инициализируем мапу профилей
		contextWindow: contextWindow,
		mutex:         sync.RWMutex{},
		autoSave:      autoSave,
	}
}

// AddMessage добавляет сообщение в файловое хранилище.
func (fs *FileStorage) AddMessage(chatID int64, message *tgbotapi.Message) {
	fs.mutex.Lock()
	defer fs.mutex.Unlock()

	// Создаем историю для чата, если ее еще нет
	if _, exists := fs.messages[chatID]; !exists {
		fs.messages[chatID] = make([]*tgbotapi.Message, 0, fs.contextWindow)
	}

	// Добавляем сообщение
	fs.messages[chatID] = append(fs.messages[chatID], message)

	// Удаляем старые сообщения, если превышен контекстный лимит
	if len(fs.messages[chatID]) > fs.contextWindow {
		fs.messages[chatID] = fs.messages[chatID][len(fs.messages[chatID])-fs.contextWindow:]
	}

	// Автосохранение (если включено)
	if fs.autoSave {
		// Запускаем сохранение в отдельной горутине, чтобы не блокировать
		go func(cid int64) {
			if err := fs.SaveChatHistory(cid); err != nil {
				log.Printf("[FileStorage AutoSave ERROR] Чат %d: %v", cid, err)
			}
		}(chatID)
	}
}

// GetMessages возвращает историю сообщений для чата из файлового хранилища.
func (fs *FileStorage) GetMessages(chatID int64) []*tgbotapi.Message {
	fs.mutex.RLock()
	defer fs.mutex.RUnlock()

	if messages, exists := fs.messages[chatID]; exists {
		// Возвращаем копию среза, чтобы избежать гонок при модификации вне мьютекса
		// (хотя GetMessages обычно только читает, но для безопасности)
		result := make([]*tgbotapi.Message, len(messages))
		copy(result, messages)
		return result
	}

	return []*tgbotapi.Message{}
}

// GetMessagesSince возвращает сообщения начиная с указанного времени из файлового хранилища.
func (fs *FileStorage) GetMessagesSince(chatID int64, since time.Time) []*tgbotapi.Message {
	fs.mutex.RLock()
	defer fs.mutex.RUnlock()

	if messages, exists := fs.messages[chatID]; exists {
		result := make([]*tgbotapi.Message, 0)
		for _, msg := range messages {
			if msg.Time().After(since) {
				result = append(result, msg)
			}
		}
		return result
	}

	return []*tgbotapi.Message{}
}

// AddMessagesToContext добавляет массив сообщений в контекст чата файлового хранилища.
func (fs *FileStorage) AddMessagesToContext(chatID int64, messages []*tgbotapi.Message) {
	fs.mutex.Lock()
	defer fs.mutex.Unlock()

	// Создаем историю для чата, если ее еще нет
	if _, exists := fs.messages[chatID]; !exists {
		fs.messages[chatID] = make([]*tgbotapi.Message, 0, fs.contextWindow)
	}

	// Добавляем сообщения
	fs.messages[chatID] = append(fs.messages[chatID], messages...)

	// Удаляем старые сообщения, если превышен контекстный лимит
	if len(fs.messages[chatID]) > fs.contextWindow {
		fs.messages[chatID] = fs.messages[chatID][len(fs.messages[chatID])-fs.contextWindow:]
	}

	// Автосохранение здесь убрано, т.к. вызывается только при загрузке истории
	// Периодическое сохранение позаботится об этом
}

// ClearChatHistory очищает историю сообщений для указанного чата в файловом хранилище (память и файл).
func (fs *FileStorage) ClearChatHistory(chatID int64) error {
	fs.mutex.Lock()
	delete(fs.messages, chatID)
	fs.mutex.Unlock()
	log.Printf("[FileStorage Clear] Чат %d: История в памяти очищена.", chatID)

	// Также удаляем соответствующий файл
	err := fs.deleteChatHistoryFile(chatID)
	if err != nil {
		// Логируем ошибку, но не считаем это фатальным
		log.Printf("[FileStorage Clear WARN] Чат %d: Ошибка при удалении файла истории: %v", chatID, err)
	}
	return nil // Интерфейс требует возвращать ошибку, но в данном случае удаление файла не критично
}

// Close для FileStorage (ничего не делает)
func (fs *FileStorage) Close() error {
	// При закрытии сохраняем все истории
	fs.mutex.RLock()
	chatIDs := make([]int64, 0, len(fs.messages))
	for id := range fs.messages {
		chatIDs = append(chatIDs, id)
	}
	fs.mutex.RUnlock()

	for _, id := range chatIDs {
		if err := fs.SaveChatHistory(id); err != nil {
			log.Printf("[FileStorage Close ERROR] Чат %d: %v", id, err)
		}
	}
	return nil
}

// getChatHistoryFilename возвращает имя файла для хранения истории чата.
func (fs *FileStorage) getChatHistoryFilename(chatID int64) string {
	// Используем директорию /data, создавая ее при необходимости
	historyDir := "/data"
	if err := os.MkdirAll(historyDir, 0755); err != nil {
		// В случае ошибки создания директории, логируем и возвращаем путь внутри нее
		// Функции записи/чтения обработают ошибку доступа, если она критична
		log.Printf("[getChatHistoryFilename WARN] Чат %d: Ошибка создания/доступа к директории %s: %v", chatID, historyDir, err)
	}
	return filepath.Join(historyDir, fmt.Sprintf("chat_%d.json", chatID))
}

// deleteChatHistoryFile удаляет файл истории для указанного чата.
func (fs *FileStorage) deleteChatHistoryFile(chatID int64) error {
	filePath := fs.getChatHistoryFilename(chatID) // Теперь использует /data
	err := os.Remove(filePath)
	if err != nil {
		// Если файл не найден, это не ошибка в контексте очистки
		if os.IsNotExist(err) {
			log.Printf("[FileStorage DeleteFile] Чат %d: Файл истории %s не найден, удалять нечего.", chatID, filePath)
			return nil
		}
		log.Printf("[FileStorage DeleteFile ERROR] Чат %d: Не удалось удалить файл истории %s: %v", chatID, filePath, err)
		return fmt.Errorf("ошибка удаления файла истории чата %d: %w", chatID, err)
	}
	log.Printf("[FileStorage DeleteFile] Чат %d: Файл истории %s успешно удален.", chatID, filePath)
	return nil
}

// GetAllChatIDs возвращает список ID всех чатов, для которых есть история в памяти.
func (fs *FileStorage) GetAllChatIDs() ([]int64, error) {
	fs.mutex.RLock()
	defer fs.mutex.RUnlock()
	ids := make([]int64, 0, len(fs.messages))
	for id := range fs.messages {
		ids = append(ids, id)
	}
	return ids, nil
}

// --- Методы для профилей пользователей (не реализованы для FileStorage) ---

func (fs *FileStorage) GetUserProfile(chatID int64, userID int64) (*UserProfile, error) {
	fs.mutex.RLock()
	defer fs.mutex.RUnlock()
	if chatProfiles, ok := fs.userProfiles[chatID]; ok {
		if profile, ok := chatProfiles[userID]; ok {
			// Возвращаем копию
			pCopy := *profile
			return &pCopy, nil
		}
	}
	return nil, nil // Не найдено
}

func (fs *FileStorage) SetUserProfile(profile *UserProfile) error {
	fs.mutex.Lock()
	defer fs.mutex.Unlock()
	if _, ok := fs.userProfiles[profile.ChatID]; !ok {
		fs.userProfiles[profile.ChatID] = make(map[int64]*UserProfile)
	}
	pCopy := *profile // Сохраняем копию
	fs.userProfiles[profile.ChatID][profile.UserID] = &pCopy
	// Сохранение профилей в файл не реализовано в FileStorage
	return nil
}

func (fs *FileStorage) GetAllUserProfiles(chatID int64) ([]*UserProfile, error) {
	fs.mutex.RLock()
	defer fs.mutex.RUnlock()
	var profiles []*UserProfile
	if chatProfiles, ok := fs.userProfiles[chatID]; ok {
		for _, profile := range chatProfiles {
			pCopy := *profile
			profiles = append(profiles, &pCopy)
		}
	}
	return profiles, nil
}

// GetStatus для FileStorage
func (fs *FileStorage) GetStatus(chatID int64) string {
	fs.mutex.RLock()
	defer fs.mutex.RUnlock()
	count := 0
	if messages, exists := fs.messages[chatID]; exists {
		count = len(messages)
	}
	profileCount := 0
	if profiles, ok := fs.userProfiles[chatID]; ok {
		profileCount = len(profiles)
	}
	return fmt.Sprintf("Хранилище: Файл JSON. Сообщений в памяти: %d. Профилей в памяти: %d. Автосохранение: %t", count, profileCount, fs.autoSave)
}

// ChatSettings содержит настройки, специфичные для чата, которые сохраняются в БД.
type ChatSettings struct {
	ChatID                    int64    `bson:"chat_id" db:"chat_id"`
	ConversationStyle         string   `bson:"conversation_style,omitempty" db:"conversation_style"`
	Temperature               *float64 `bson:"temperature,omitempty" db:"temperature"` // Указатель, чтобы отличить 0 от отсутствия значения
	Model                     string   `bson:"model,omitempty" db:"model"`
	GeminiSafetyThreshold     string   `bson:"gemini_safety_threshold,omitempty" db:"gemini_safety_threshold"`
	VoiceTranscriptionEnabled *bool    `bson:"voice_transcription_enabled,omitempty" db:"voice_transcription_enabled"` // Включена ли транскрипция ГС
	// --- Новые поля для лимита прямых ответов ---
	DirectReplyLimitEnabled  *bool `bson:"direct_reply_limit_enabled,omitempty" db:"direct_reply_limit_enabled"`                   // Включен ли лимит
	DirectReplyLimitCount    *int  `bson:"direct_reply_limit_count,omitempty" db:"direct_reply_limit_count"`                       // Макс. кол-во обращений
	DirectReplyLimitDuration *int  `bson:"direct_reply_limit_duration_minutes,omitempty" db:"direct_reply_limit_duration_minutes"` // Длительность периода (в минутах)
	// Другие настройки чата можно добавить сюда
}
