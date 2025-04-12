package storage

import (
	// Добавим для sql.NullString в Postgres

	"context" // Keep for Postgres potentially

	// Keep for Postgres potentially
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect" // Added for checking nil pointer inside EnsureIndexes
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	_ "github.com/lib/pq" // Postgres driver, keep if postgres is still an option
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive" // Добавим для MongoDB ID
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
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

	// === Общие методы ===
	// Close закрывает соединение с хранилищем.
	Close() error

	// GetStatus возвращает строку с текущим статусом хранилища (тип, подключение, кол-во сообщений и т.д.)
	GetStatus(chatID int64) string
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

// ChatSettings представляет настройки для конкретного чата
type ChatSettings struct {
	ChatID                    int64    `bson:"chat_id"`
	ConversationStyle         string   `bson:"conversation_style,omitempty"`
	Temperature               *float64 `bson:"temperature,omitempty"` // Указатель, чтобы отличить 0 от отсутствия значения
	Model                     string   `bson:"model,omitempty"`
	GeminiSafetyThreshold     string   `bson:"gemini_safety_threshold,omitempty"`
	VoiceTranscriptionEnabled *bool    `bson:"voice_transcription_enabled,omitempty"` // Включена ли транскрипция ГС
	// Другие настройки чата можно добавить сюда
}

// GetChatSettings для FileStorage (заглушка)
func (fs *FileStorage) GetChatSettings(chatID int64) (*ChatSettings, error) {
	log.Printf("[FileStorage WARN] GetChatSettings не реализован для FileStorage. Возвращаются дефолтные значения.")
	// Возвращаем дефолтные настройки, но без сохранения/загрузки
	// Важно: Эти значения НЕ берутся из config, т.к. FileStorage его не получает.
	// Это просто пример структуры для совместимости интерфейса.
	temp := 0.7
	enabled := true
	return &ChatSettings{
		ChatID:                    chatID,
		ConversationStyle:         "balanced", // Пример
		Temperature:               &temp,
		Model:                     "default_file_model",     // Пример
		GeminiSafetyThreshold:     "BLOCK_MEDIUM_AND_ABOVE", // Пример
		VoiceTranscriptionEnabled: &enabled,                 // Пример
	}, nil
}

func (fs *FileStorage) SetChatSettings(settings *ChatSettings) error {
	log.Printf("[FileStorage WARN] SetChatSettings не реализован для FileStorage. Настройки для чата %d не сохранены.", settings.ChatID)
	// Ничего не делаем
	return nil
}

// --- Методы MongoStorage для настроек ---

// GetChatSettings получает настройки чата из MongoDB
func (ms *MongoStorage) GetChatSettings(chatID int64) (*ChatSettings, error) {
	var settings ChatSettings
	// Исправлено: Используем поле структуры ms
	collection := ms.settingsCollection
	if collection == nil {
		log.Printf("[ERROR][GetChatSettings] Коллекция настроек (ms.settingsCollection) равна nil для чата %d!", chatID)
		// Возвращаем ошибку ИЛИ дефолтные настройки? Лучше ошибку, т.к. это проблема инициализации.
		// Однако, для пользователя может быть лучше вернуть дефолтные. Пока вернем ошибку.
		return nil, fmt.Errorf("внутренняя ошибка: коллекция настроек не инициализирована")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	filter := bson.M{"chat_id": chatID}
	err := collection.FindOne(ctx, filter).Decode(&settings)

	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			// Настройки не найдены, создаем и возвращаем дефолтные
			if ms.debug {
				log.Printf("[DEBUG][GetChatSettings] Настройки для чата %d не найдены, создаю дефолтные.", chatID)
			}
			// Используем метод ensureChatSettings, который теперь использует ms.cfg
			defaultSettings, createErr := ms.ensureChatSettings(ctx, chatID) // Передаем только chatID
			if createErr != nil {
				log.Printf("[ERROR][GetChatSettings] Ошибка при создании дефолтных настроек для чата %d: %v", chatID, createErr)
				return nil, fmt.Errorf("ошибка создания настроек чата: %w", createErr)
			}
			return defaultSettings, nil
		}
		// Другая ошибка при поиске
		log.Printf("[ERROR][GetChatSettings] Ошибка получения настроек для чата %d: %v", chatID, err)
		return nil, fmt.Errorf("ошибка получения настроек чата: %w", err)
	}

	// Настройки найдены, проверяем и устанавливаем значения по умолчанию для отсутствующих полей
	needsUpdate := ms.applyDefaultsToSettings(&settings) // Выносим логику в отдельный метод

	// Если нужно обновить документ в базе с дефолтными значениями
	if needsUpdate {
		if ms.debug {
			log.Printf("[DEBUG][GetChatSettings] Обновляю документ настроек для чата %d с дефолтными значениями.", chatID)
		}
		// Запускаем обновление в фоне, чтобы не блокировать основной поток
		go func(s ChatSettings) { // Передаем копию настроек
			if err := ms.SetChatSettings(&s); err != nil {
				log.Printf("[WARN][GetChatSettings Background Update] Ошибка фонового обновления настроек для чата %d: %v", s.ChatID, err)
			}
		}(settings) // Передаем копию текущего состояния settings
	}

	if ms.debug {
		log.Printf("[DEBUG][GetChatSettings] Настройки для чата %d успешно получены.", chatID)
	}
	return &settings, nil
}

// applyDefaultsToSettings проверяет и устанавливает значения по умолчанию для отсутствующих полей ChatSettings.
// Возвращает true, если были применены какие-либо дефолтные значения.
func (ms *MongoStorage) applyDefaultsToSettings(settings *ChatSettings) bool {
	needsUpdate := false
	if settings.ConversationStyle == "" {
		settings.ConversationStyle = ms.cfg.DefaultConversationStyle
		needsUpdate = true
	}
	if settings.Temperature == nil {
		temp := ms.cfg.DefaultTemperature
		settings.Temperature = &temp
		needsUpdate = true
	}
	if settings.Model == "" {
		settings.Model = ms.cfg.DefaultModel
		needsUpdate = true
	}
	if settings.GeminiSafetyThreshold == "" {
		settings.GeminiSafetyThreshold = ms.cfg.DefaultSafetyThreshold
		needsUpdate = true
	}
	if settings.VoiceTranscriptionEnabled == nil {
		enabled := ms.cfg.VoiceTranscriptionEnabledDefault
		settings.VoiceTranscriptionEnabled = &enabled
		needsUpdate = true
	}
	return needsUpdate
}

// ensureChatSettings проверяет наличие настроек и создает их с дефолтными значениями, если отсутствуют.
// Вызывается из GetChatSettings, если документ не найден.
// Теперь использует ms.cfg напрямую.
func (ms *MongoStorage) ensureChatSettings(ctx context.Context, chatID int64) (*ChatSettings, error) {
	// Исправлено: Используем правильный доступ к коллекции через поле структуры ms
	collection := ms.settingsCollection
	if collection == nil {
		log.Printf("[ERROR][ensureChatSettings] Коллекция настроек (ms.settingsCollection) равна nil при попытке создать настройки для чата %d!", chatID)
		return nil, fmt.Errorf("внутренняя ошибка: коллекция настроек не инициализирована")
	}

	// Создаем новый документ с дефолтными значениями из конфига ms.cfg
	defaultTemp := ms.cfg.DefaultTemperature
	defaultVoiceEnabled := ms.cfg.VoiceTranscriptionEnabledDefault
	newSettings := ChatSettings{
		ChatID:                    chatID,
		ConversationStyle:         ms.cfg.DefaultConversationStyle,
		Temperature:               &defaultTemp,
		Model:                     ms.cfg.DefaultModel,
		GeminiSafetyThreshold:     ms.cfg.DefaultSafetyThreshold,
		VoiceTranscriptionEnabled: &defaultVoiceEnabled,
	}

	// Пытаемся вставить новый документ
	_, insertErr := collection.InsertOne(ctx, newSettings)
	if insertErr != nil {
		// Проверяем, возможно, документ уже был создан другим потоком (ошибка дубликата)
		if mongo.IsDuplicateKeyError(insertErr) {
			if ms.debug {
				log.Printf("[DEBUG][ensureChatSettings] Настройки для чата %d уже существуют (ошибка дубликата). Повторно запрашиваю.", chatID)
			}
			// Повторно запрашиваем существующие настройки
			var existingSettings ChatSettings
			filter := bson.M{"chat_id": chatID}
			// Используем новый контекст для повторного запроса
			findCtx, findCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer findCancel()
			findErr := collection.FindOne(findCtx, filter).Decode(&existingSettings)
			if findErr != nil {
				log.Printf("[ERROR][ensureChatSettings] Ошибка повторного получения настроек для чата %d после ошибки дубликата: %v", chatID, findErr)
				return nil, fmt.Errorf("ошибка повторного получения настроек: %w", findErr)
			}
			// Применяем дефолты к только что полученным настройкам на случай, если они неполные
			_ = ms.applyDefaultsToSettings(&existingSettings) // Игнорируем needsUpdate здесь
			return &existingSettings, nil
		}
		// Другая ошибка при вставке
		log.Printf("[ERROR][ensureChatSettings] Ошибка вставки дефолтных настроек для чата %d: %v", chatID, insertErr)
		return nil, fmt.Errorf("ошибка вставки настроек чата: %w", insertErr)
	}

	if ms.debug {
		log.Printf("[DEBUG][ensureChatSettings] Дефолтные настройки для чата %d успешно созданы и вставлены.", chatID)
	}
	// Возвращаем только что созданные настройки
	return &newSettings, nil
}

// SetChatSettings сохраняет настройки чата в MongoDB (UPSERT)
func (ms *MongoStorage) SetChatSettings(settings *ChatSettings) error {
	if settings == nil || settings.ChatID == 0 {
		return fmt.Errorf("невалидные настройки для сохранения (nil или chat_id=0)")
	}

	// Исправлено: Используем правильный доступ к коллекции
	collection := ms.settingsCollection
	if collection == nil {
		log.Printf("[ERROR][SetChatSettings] Коллекция настроек (ms.settingsCollection) равна nil для чата %d!", settings.ChatID)
		return fmt.Errorf("внутренняя ошибка: коллекция настроек не инициализирована")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	filter := bson.M{"chat_id": settings.ChatID}

	// Формируем обновление. Используем $set для всех полей, чтобы перезаписать их.
	// Используем reflect для динамического добавления ненулевых полей в $set и $unset
	updateDoc := bson.M{}
	unsetDoc := bson.M{} // Документ для $unset
	v := reflect.ValueOf(*settings)
	t := v.Type()

	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		fieldType := t.Field(i)
		bsonTag := fieldType.Tag.Get("bson")
		parts := strings.Split(bsonTag, ",") // Получаем имя поля из bson тега
		if len(parts) > 0 && parts[0] != "" && parts[0] != "-" {
			bsonFieldName := parts[0]
			// Игнорируем chat_id, так как он в фильтре
			if bsonFieldName != "chat_id" {
				// Обработка указателей (Temperature, VoiceTranscriptionEnabled)
				if field.Kind() == reflect.Ptr {
					if !field.IsNil() { // Добавляем в $set только если указатель не nil
						updateDoc[bsonFieldName] = field.Elem().Interface() // Развертываем указатель
					} else {
						// Если указатель nil, добавляем его в $unset, чтобы удалить поле из документа
						unsetDoc[bsonFieldName] = ""
					}
				} else if field.IsValid() && !field.IsZero() { // Добавляем ненулевые значения
					updateDoc[bsonFieldName] = field.Interface()
				} else if field.IsValid() && field.IsZero() {
					// Если значение нулевое (например, пустая строка), но не указатель,
					// возможно, мы хотим его удалить? Или оставить как есть?
					// Пока что добавляем в $set (поведение по умолчанию)
					// Если нужно удалять пустые строки, логику нужно изменить.
					// Для ConversationStyle, Model, GeminiSafetyThreshold пустая строка может означать "использовать дефолт".
					// Лучше явно устанавливать их в дефолт при GET, чем удалять здесь.
					// Поэтому оставляем добавление в $set.
					updateDoc[bsonFieldName] = field.Interface()
				}
			}
		}
	}

	// Собираем итоговый документ для update
	update := bson.M{}
	if len(updateDoc) > 0 {
		update["$set"] = updateDoc
	}
	// Добавляем $unset, если он не пустой
	if len(unsetDoc) > 0 {
		update["$unset"] = unsetDoc
	}

	// Если нет ни $set, ни $unset, то обновлять нечего
	if len(update) == 0 {
		if ms.debug {
			log.Printf("[DEBUG][SetChatSettings] Нет полей для обновления настроек чата %d.", settings.ChatID)
		}
		return nil // Не ошибка, просто нет изменений
	}

	opts := options.Update().SetUpsert(true)

	result, err := collection.UpdateOne(ctx, filter, update, opts)
	if err != nil {
		log.Printf("[ERROR][SetChatSettings] Ошибка сохранения настроек для чата %d: %v", settings.ChatID, err)
		return fmt.Errorf("ошибка сохранения настроек чата: %w", err)
	}

	if ms.debug {
		if result.UpsertedCount > 0 {
			log.Printf("[DEBUG][SetChatSettings] Настройки для чата %d успешно созданы (UpsertedID: %v).", settings.ChatID, result.UpsertedID)
		} else if result.ModifiedCount > 0 {
			log.Printf("[DEBUG][SetChatSettings] Настройки для чата %d успешно обновлены.", settings.ChatID)
		} else if result.MatchedCount > 0 {
			log.Printf("[DEBUG][SetChatSettings] Настройки для чата %d не изменились (Matched: %d).", settings.ChatID, result.MatchedCount)
		} else {
			log.Printf("[DEBUG][SetChatSettings] Запрос UpdateOne для чата %d завершен без изменений (Upserted: %d, Modified: %d, Matched: %d).", settings.ChatID, result.UpsertedCount, result.ModifiedCount, result.MatchedCount)
		}
	}

	return nil
}

// --- Методы PostgresStorage --- (Добавляем заглушки для настроек)
// Убедимся, что PostgresStorage реализует интерфейс ChatHistoryStorage.
var _ ChatHistoryStorage = (*PostgresStorage)(nil)

// GetChatSettings для PostgresStorage (заглушка)
func (ps *PostgresStorage) GetChatSettings(chatID int64) (*ChatSettings, error) {
	log.Printf("[PostgresStorage WARN] GetChatSettings не реализован для PostgresStorage. Возвращаются дефолтные значения.")
	// Возвращаем дефолтные настройки, как и для FileStorage
	// Важно: Эти значения НЕ берутся из config, т.к. PostgresStorage его не получает напрямую в этой заглушке.
	temp := 0.7
	enabled := true
	return &ChatSettings{
		ChatID:                    chatID,
		ConversationStyle:         "balanced", // Пример
		Temperature:               &temp,
		Model:                     "default_pg_model",       // Пример
		GeminiSafetyThreshold:     "BLOCK_MEDIUM_AND_ABOVE", // Пример
		VoiceTranscriptionEnabled: &enabled,                 // Пример
	}, nil
}

// SetChatSettings для PostgresStorage (заглушка)
func (ps *PostgresStorage) SetChatSettings(settings *ChatSettings) error {
	log.Printf("[PostgresStorage WARN] SetChatSettings не реализован для PostgresStorage. Настройки для чата %d не сохранены.", settings.ChatID)
	return nil
}

// GetStatus для PostgresStorage был перемещен в postgres_storage.go
