package storage

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/Henry-Case-dev/rofloslav/internal/config"
	"github.com/Henry-Case-dev/rofloslav/internal/gemini"
	"github.com/Henry-Case-dev/rofloslav/internal/llm" // <--- Добавляем импорт
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Убедимся, что MongoStorage реализует интерфейс ChatHistoryStorage.
var _ ChatHistoryStorage = (*MongoStorage)(nil)

// Имя коллекции для настроек чата
const settingsCollectionName = "chat_settings"

// MongoStorage реализует ChatHistoryStorage с использованием MongoDB.
type MongoStorage struct {
	client                 *mongo.Client
	database               *mongo.Database
	messagesCollection     *mongo.Collection // Коллекция для сообщений
	userProfilesCollection *mongo.Collection // Коллекция для профилей пользователей
	settingsCollection     *mongo.Collection // Коллекция для настроек чатов
	cfg                    *config.Config    // Добавлена ссылка на конфиг
	debug                  bool              // Сохраняем флаг debug из конфига
	llmClient              llm.LLMClient     // Клиент LLM для генерации эмбеддингов
	embeddingClient        *gemini.Client    // Клиент Gemini ИМЕННО для эмбеддингов

	// Мьютекс для защиты map-ов с последними данными
	settingsMutex sync.RWMutex
}

// NewMongoStorage создает новый экземпляр MongoStorage.
// Принимает URI, имя БД, имена коллекций, конфиг и LLM клиент.
func NewMongoStorage(mongoURI, dbName, messagesCollectionName, userProfilesCollectionName string, cfg *config.Config, llmClient llm.LLMClient, embeddingClient *gemini.Client) (*MongoStorage, error) {
	if mongoURI == "" || dbName == "" || messagesCollectionName == "" || userProfilesCollectionName == "" || cfg.MongoDbSettingsCollection == "" {
		return nil, fmt.Errorf("необходимо указать MongoDB URI, имя БД и имена всех коллекций (messages, userProfiles, settings)")
	}
	if llmClient == nil {
		return nil, fmt.Errorf("необходимо передать инициализированный LLM клиент в MongoStorage")
	}

	log.Println("Подключение к MongoDB...")
	clientOptions := options.Client().ApplyURI(mongoURI)
	client, err := mongo.Connect(context.Background(), clientOptions)
	if err != nil {
		return nil, fmt.Errorf("ошибка подключения к MongoDB: %w", err)
	}

	// Проверка подключения
	err = client.Ping(context.Background(), nil)
	if err != nil {
		return nil, fmt.Errorf("ошибка пинга MongoDB: %w", err)
	}
	log.Println("Успешно подключено к MongoDB.")

	db := client.Database(dbName)
	messagesColl := db.Collection(messagesCollectionName)
	userProfilesColl := db.Collection(userProfilesCollectionName)
	settingsColl := db.Collection(cfg.MongoDbSettingsCollection)

	ms := &MongoStorage{
		client:                 client,
		database:               db,
		messagesCollection:     messagesColl,
		userProfilesCollection: userProfilesColl,
		settingsCollection:     settingsColl,
		cfg:                    cfg,
		debug:                  cfg.Debug,       // Берем из конфига
		llmClient:              llmClient,       // Сохраняем LLM клиент
		embeddingClient:        embeddingClient, // Сохраняем правильный клиент
	}

	// Создание индексов при инициализации
	if err := ms.ensureIndexes(); err != nil {
		// Логируем ошибку, но не прерываем запуск, возможно, индексы уже есть
		log.Printf("[WARN] Ошибка при создании индексов в MongoDB: %v", err)
	}

	return ms, nil
}

// ensureIndexes создает необходимые индексы в коллекциях MongoDB.
func (ms *MongoStorage) ensureIndexes() error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second) // Увеличим таймаут
	defer cancel()

	// --- Индексы для коллекции сообщений ---
	messagesIndexes := []mongo.IndexModel{
		{ // Добавляем индекс для выборки и сортировки сообщений по дате
			Keys:    bson.D{{Key: "chat_id", Value: 1}, {Key: "date", Value: -1}},
			Options: options.Index().SetName("chat_id_date_desc"), // Имя индекса
		},
		// Можно добавить другие индексы для сообщений при необходимости, например, для поиска по user_id
		// {
		// 	Keys:    bson.D{{Key: "chat_id", Value: 1}, {Key: "user_id", Value: 1}},
		// 	Options: options.Index().SetName("chat_id_user_id"),
		// },
	}
	// Убрал лог отсюда, т.к. он дублировался ниже

	// --- Индексы для коллекции профилей пользователей ---
	profilesIndexes := []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "chat_id", Value: 1}, {Key: "user_id", Value: 1}}, // Уникальный составной индекс
			Options: options.Index().SetName("chat_id_user_id_unique").SetUnique(true),
		},
		{
			Keys:    bson.D{{Key: "chat_id", Value: 1}, {Key: "last_seen", Value: -1}}, // Для выборки всех профилей чата и сортировки
			Options: options.Index().SetName("chat_id_last_seen_desc"),
		},
	}
	// --- Индексы для коллекции настроек чатов ---
	settingsIndexes := []mongo.IndexModel{
		{ // Индекс для быстрого поиска настроек по chat_id
			Keys:    bson.D{{Key: "chat_id", Value: 1}},
			Options: options.Index().SetName("chat_id_unique").SetUnique(true),
		},
	}

	// Создаем индексы для сообщений
	_, err := ms.messagesCollection.Indexes().CreateMany(ctx, messagesIndexes)
	if err != nil {
		// Проверяем на специфическую ошибку (индексы уже существуют) и игнорируем ее
		cmdErr, ok := err.(mongo.CommandError)
		if !ok || !cmdErr.HasErrorCode(85) { // 85 = IndexOptionsConflict (при изменении опций существующего)
			// 86 = IndexKeySpecsConflict (при изменении ключей существующего)
			// Error codes могут отличаться в версиях MongoDB, но 85/86 частые для конфликтов индексов
			writeErr, okWrite := err.(mongo.WriteException)
			alreadyExists := false
			if okWrite {
				for _, we := range writeErr.WriteErrors {
					if we.Code == 11000 { // Код ошибки дубликата ключа, может возникать при гонке создания уникального индекса
						alreadyExists = true
						break
					}
				}
			}
			if !alreadyExists { // Если это не ошибка конфликта или дубликата, возвращаем ее
				return fmt.Errorf("ошибка создания индексов для коллекции сообщений: %w", err)
			}
		}
		// Если ошибка связана с конфликтом или дубликатом, просто логируем как debug
		if ms.debug {
			log.Printf("[DEBUG] Индексы для сообщений: конфликт или уже существуют (%v)", err)
		}
	}
	log.Println("Индексы для коллекции сообщений MongoDB проверены/созданы.")

	// --- Индексы для коллекции профилей пользователей ---
	_, err = ms.userProfilesCollection.Indexes().CreateMany(ctx, profilesIndexes)
	if err != nil {
		// Аналогичная проверка на ошибки конфликта/дубликата
		cmdErr, ok := err.(mongo.CommandError)
		if !ok || !cmdErr.HasErrorCode(85) {
			writeErr, okWrite := err.(mongo.WriteException)
			alreadyExists := false
			if okWrite {
				for _, we := range writeErr.WriteErrors {
					if we.Code == 11000 {
						alreadyExists = true
						break
					}
				}
			}
			if !alreadyExists {
				return fmt.Errorf("ошибка создания индексов для коллекции профилей: %w", err)
			}
		}
		if ms.debug {
			log.Printf("[DEBUG] Индексы для профилей: конфликт или уже существуют (%v)", err)
		}
	}
	log.Println("Индексы для коллекции профилей MongoDB проверены/созданы.")

	// Создаем индексы для настроек
	_, err = ms.settingsCollection.Indexes().CreateMany(ctx, settingsIndexes)
	if err != nil {
		// Аналогичная проверка на ошибки конфликта/дубликата
		cmdErr, ok := err.(mongo.CommandError)
		if !ok || !cmdErr.HasErrorCode(85) {
			writeErr, okWrite := err.(mongo.WriteException)
			alreadyExists := false
			if okWrite {
				for _, we := range writeErr.WriteErrors {
					if we.Code == 11000 {
						alreadyExists = true
						break
					}
				}
			}
			if !alreadyExists {
				return fmt.Errorf("ошибка создания индексов для коллекции настроек: %w", err)
			}
		}
		if ms.debug {
			log.Printf("[DEBUG] Индексы для настроек: конфликт или уже существуют (%v)", err)
		}
	}
	log.Println("Индексы для коллекции настроек MongoDB проверены/созданы.")

	return nil
}

// Close закрывает соединение с MongoDB.
func (ms *MongoStorage) Close() error {
	if ms.client != nil {
		log.Println("Закрытие соединения с MongoDB...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return ms.client.Disconnect(ctx)
	}
	return nil
}

// === Методы для истории сообщений (перенесены в mongo_messages.go) ===

// Commenting out moved method AddMessage
/*
func (ms *MongoStorage) AddMessage(chatID int64, message *tgbotapi.Message) {
	// ... implementation ...
}
*/

// Commenting out moved method GetMessages
/*
func (ms *MongoStorage) GetMessages(chatID int64, limit int) ([]*tgbotapi.Message, error) {
	// ... implementation ...
}
*/

// Commenting out moved method GetMessagesSince
/*
func (ms *MongoStorage) GetMessagesSince(chatID int64, since time.Time) ([]*tgbotapi.Message, error) {
	// ... implementation ...
}
*/

// Commenting out moved method LoadChatHistory
/*
func (ms *MongoStorage) LoadChatHistory(chatID int64) ([]*tgbotapi.Message, error) {
	// ... implementation ...
}
*/

// Commenting out moved method SaveChatHistory
/*
func (ms *MongoStorage) SaveChatHistory(chatID int64) error {
	// ... implementation ...
}
*/

// Commenting out moved method ClearChatHistory
/*
func (ms *MongoStorage) ClearChatHistory(chatID int64) error {
	// ... implementation ...
}
*/

// Commenting out moved method AddMessagesToContext
/*
func (ms *MongoStorage) AddMessagesToContext(chatID int64, messages []*tgbotapi.Message) {
	// ... implementation ...
}
*/

// Commenting out moved method GetAllChatIDs
/*
func (ms *MongoStorage) GetAllChatIDs() ([]int64, error) {
	// ... implementation ...
}
*/

// === Методы для настроек чатов (перенесены в mongo_settings.go) ===

// GetStatus возвращает статус хранилища MongoDB
func (ms *MongoStorage) GetStatus(chatID int64) string {
	status := "Хранилище: MongoDB. "
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	count, err := ms.messagesCollection.CountDocuments(ctx, bson.M{"chat_id": chatID})
	if err != nil {
		log.Printf("[Mongo GetStatus WARN] Чат %d: Ошибка получения количества сообщений: %v", chatID, err)
		status += "Состояние: Ошибка подсчета сообщений."
	} else {
		status += fmt.Sprintf("Сообщений в базе: %d", count)
	}

	// Проверка подключения (Ping)
	ctxPing, cancelPing := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelPing()
	err = ms.client.Ping(ctxPing, nil)
	if err != nil {
		log.Printf("[Mongo GetStatus WARN] Чат %d: Ошибка Ping: %v", chatID, err)
		status += " Подключение: Ошибка."
	} else {
		status += " Подключение: ОК."
	}

	return status
}

// === Методы для профилей пользователей (перенесены в mongo_profiles.go) ===

// === Методы для работы с эмбеддингами и долгосрочной памятью (реализация в mongo_embeddings.go) ===

// GetTotalMessagesCount вызывает реализацию из mongo_embeddings.go.
func (ms *MongoStorage) GetTotalMessagesCount(chatID int64) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Вызываем функцию из mongo_embeddings.go
	return GetTotalMessagesCount(ctx, ms.messagesCollection, chatID)
}

// FindMessagesWithoutEmbedding вызывает реализацию из mongo_embeddings.go.
func (ms *MongoStorage) FindMessagesWithoutEmbedding(chatID int64, limit int, skipMessageIDs []int) ([]MongoMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // Увеличим таймаут
	defer cancel()
	// Вызываем функцию из mongo_embeddings.go
	return FindMessagesWithoutEmbedding(ctx, ms.messagesCollection, chatID, limit, skipMessageIDs)
}

// UpdateMessageEmbedding вызывает реализацию из mongo_embeddings.go.
func (ms *MongoStorage) UpdateMessageEmbedding(chatID int64, messageID int, vector []float32) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Вызываем функцию из mongo_embeddings.go, передавая ms.debug
	return UpdateMessageEmbedding(ctx, ms.messagesCollection, chatID, messageID, vector, ms.debug)
}

// SearchRelevantMessages вызывает реализацию из mongo_embeddings.go.
func (ms *MongoStorage) SearchRelevantMessages(chatID int64, queryText string, k int) ([]*tgbotapi.Message, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second) // Увеличим таймаут для поиска
	defer cancel()
	// Вызываем функцию из mongo_embeddings.go, передавая ms.debug
	// Убедимся, что передаем правильный embeddingClient
	return SearchRelevantMessages(ctx, ms.cfg, ms.messagesCollection, ms.embeddingClient, chatID, queryText, k, ms.debug)
}

// --- Вспомогательная функция для усечения строки (перенесена в utils) ---

// --- Конец реализации интерфейса ChatHistoryStorage ---
