package storage

import (
	"context"
	"fmt"
	"log"
	"strings"
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
	client   *mongo.Client
	database *mongo.Database
	// messagesCollection     *mongo.Collection // УДАЛЕНО: Коллекция для сообщений по умолчанию не нужна
	userProfilesCollection *mongo.Collection // Коллекция для профилей пользователей
	settingsCollection     *mongo.Collection // Коллекция для настроек чатов
	cfg                    *config.Config    // Добавлена ссылка на конфиг
	debug                  bool              // Сохраняем флаг debug из конфига
	llmClient              llm.LLMClient     // Клиент LLM для генерации эмбеддингов
	embeddingClient        *gemini.Client    // Клиент Gemini ИМЕННО для эмбеддингов
	renamedChats           map[int64]bool

	// Мьютексы
	settingsMutex sync.RWMutex
	renameMutex   sync.RWMutex // Новый мьютекс для защиты renamedChats

	// Кэш статуса переименования/индексации коллекций
	indexedChats map[string]bool // true если для коллекции с таким именем созданы индексы
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
	// MessagesCollection := db.Collection(messagesCollectionName) // УДАЛЕНО: Коллекция для сообщений по умолчанию не нужна
	userProfilesColl := db.Collection(userProfilesCollectionName)
	settingsColl := db.Collection(cfg.MongoDbSettingsCollection)

	ms := &MongoStorage{
		client:   client,
		database: db,
		// MessagesCollection:     db.Collection(messagesCollectionName), // УДАЛЕНО: Коллекция для сообщений по умолчанию не нужна
		userProfilesCollection: userProfilesColl,
		settingsCollection:     settingsColl,
		cfg:                    cfg,
		debug:                  cfg.Debug,             // Берем из конфига
		llmClient:              llmClient,             // Сохраняем LLM клиент
		embeddingClient:        embeddingClient,       // Сохраняем правильный клиент
		renameMutex:            sync.RWMutex{},        // Инициализация мьютекса
		renamedChats:           make(map[int64]bool),  // Инициализация карты
		indexedChats:           make(map[string]bool), // Инициализация карты
	}

	// --- Логика однократного переименования старой коллекции ---
	go func(mongoStore *MongoStorage) {
		oldName := "chat_messages"             // Имя старой общей коллекции
		specialChatID := int64(-1002661910336) // ID чата, для которого переименовываем
		targetName := fmt.Sprintf("chat_messages_%d", specialChatID)

		ctxCheck, cancelCheck := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelCheck()

		// 1. Проверяем, существует ли старая коллекция 'chat_messages'
		collections, errList := mongoStore.database.ListCollectionNames(ctxCheck, bson.M{"name": oldName})
		if errList != nil {
			log.Printf("[Startup Rename WARN] Ошибка проверки существования коллекции '%s': %v", oldName, errList)
			return // Выходим, если не можем проверить
		}

		if len(collections) > 0 {
			log.Printf("[Startup Rename INFO] Обнаружена старая коллекция '%s'. Попытка переименования в '%s'...", oldName, targetName)

			// 2. Проверяем, не существует ли уже целевая коллекция
			targetCollections, errTargetList := mongoStore.database.ListCollectionNames(ctxCheck, bson.M{"name": targetName})
			if errTargetList != nil {
				log.Printf("[Startup Rename WARN] Ошибка проверки существования целевой коллекции '%s': %v", targetName, errTargetList)
				// Продолжаем попытку переименования на всякий случай
			}

			if len(targetCollections) > 0 {
				log.Printf("[Startup Rename INFO] Целевая коллекция '%s' уже существует. Переименование не требуется.", targetName)
				// Можно было бы удалить старую, но пока не будем для безопасности
				// mongoStore.database.Collection(oldName).Drop(context.Background())
				return
			}

			// 3. Пытаемся переименовать
			ctxRename, cancelRename := context.WithTimeout(context.Background(), 20*time.Second) // Даем больше времени
			defer cancelRename()
			renameCmd := bson.D{
				{"renameCollection", mongoStore.database.Name() + "." + oldName},
				{"to", mongoStore.database.Name() + "." + targetName},
				{"dropTarget", false}, // Не удалять целевую, если вдруг существует
			}
			var result bson.M
			errRename := mongoStore.client.Database("admin").RunCommand(ctxRename, renameCmd).Decode(&result)

			if errRename != nil {
				log.Printf("[Startup Rename ERROR] Не удалось переименовать коллекцию '%s' в '%s': %v.", oldName, targetName, errRename)
			} else {
				log.Printf("[Startup Rename SUCCESS] Коллекция '%s' успешно переименована в '%s'.", oldName, targetName)
				// Отмечаем, что переименование для этого чата условно "выполнено"
				mongoStore.renameMutex.Lock()
				mongoStore.renamedChats[specialChatID] = true
				mongoStore.renameMutex.Unlock()
			}
		} else {
			// Старая коллекция не найдена, ничего делать не нужно
			log.Printf("[Startup Rename INFO] Старая коллекция '%s' не найдена. Переименование не требуется.", oldName)
			// Отмечаем, что проверка выполнена (старой коллекции нет)
			mongoStore.renameMutex.Lock()
			mongoStore.renamedChats[specialChatID] = true
			mongoStore.renameMutex.Unlock()
		}
	}(ms) // Запускаем в горутине, чтобы не блокировать старт бота
	// --- Конец логики переименования ---

	// Создание индексов для основных коллекций (settings, profiles)
	if err := ms.ensureIndexes(); err != nil {
		log.Printf("[WARN] Ошибка при создании основных индексов в MongoDB: %v", err)
	}

	return ms, nil
}

func (ms *MongoStorage) getMessagesCollection(chatID int64) *mongo.Collection {
	targetName := fmt.Sprintf("chat_messages_%d", chatID) // Всегда используем формат chat_messages_<ID>

	// Получаем коллекцию
	coll := ms.database.Collection(targetName)

	// Проверяем и создаем индексы для этой коллекции, если еще не созданы
	ms.renameMutex.RLock() // Используем renameMutex также для защиты indexedChats
	indexed := ms.indexedChats[targetName]
	ms.renameMutex.RUnlock()

	if !indexed {
		ms.renameMutex.Lock()
		// Двойная проверка внутри блокировки
		if !ms.indexedChats[targetName] {
			if err := ms.ensureIndexesForCollection(coll); err != nil {
				log.Printf("[WARN] Чат %d: Не удалось создать/проверить индексы для коллекции %s: %v", chatID, coll.Name(), err)
				// Не фатально, продолжаем работу без индексов для этой коллекции
			} else {
				log.Printf("[INFO] Успешно созданы/проверены индексы для коллекции '%s'.", targetName)
				ms.indexedChats[targetName] = true // Отмечаем, что индексы созданы/проверены
			}
		}
		ms.renameMutex.Unlock()
	}

	return coll
}

// ensureIndexes создает необходимые индексы в основных коллекциях MongoDB (settings, profiles).
// Индексы для коллекций сообщений создаются в getMessagesCollection.
func (ms *MongoStorage) ensureIndexes() error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second) // Увеличим таймаут
	defer cancel()

	// УДАЛЕНО: Индексы для коллекции сообщений по умолчанию больше не создаются здесь
	/*
		// --- Индексы для коллекции сообщений ---
		messagesIndexes := []mongo.IndexModel{
			{ // Добавляем индекс для выборки и сортировки сообщений по дате
				Keys:    bson.D{{Key: "chat_id", Value: 1}, {Key: "date", Value: -1}},
				Options: options.Index().SetName("chat_id_date_desc"), // Имя индекса
			},
		}
	*/

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

	// --- Индексы для коллекции профилей пользователей ---
	_, errProfiles := ms.userProfilesCollection.Indexes().CreateMany(ctx, profilesIndexes)
	if errProfiles != nil {
		// Аналогичная проверка на ошибки конфликта/дубликата
		cmdErr, ok := errProfiles.(mongo.CommandError)
		if !ok || !cmdErr.HasErrorCode(85) {
			writeErr, okWrite := errProfiles.(mongo.WriteException)
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
				return fmt.Errorf("ошибка создания индексов для коллекции профилей: %w", errProfiles)
			}
		}
		if ms.debug {
			log.Printf("[DEBUG] Индексы для профилей: конфликт или уже существуют (%v)", errProfiles)
		}
	}
	log.Println("Индексы для коллекции профилей MongoDB проверены/созданы.")

	// Создаем индексы для настроек
	_, err := ms.settingsCollection.Indexes().CreateMany(ctx, settingsIndexes)
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

// ensureIndexesForCollection создает индексы для конкретной коллекции
func (ms *MongoStorage) ensureIndexesForCollection(coll *mongo.Collection) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // Увеличим таймаут
	defer cancel()

	// Индекс для поиска и сортировки по дате
	dateIndex := mongo.IndexModel{
		Keys:    bson.D{{Key: "date", Value: -1}}, // Индекс только по дате для конкретной коллекции
		Options: options.Index().SetName("date_desc"),
	}

	// Индекс для reply_to_message_id (если часто используется)
	replyIndex := mongo.IndexModel{
		Keys:    bson.D{{Key: "reply_to_message_id", Value: 1}},
		Options: options.Index().SetName("reply_to_message_id_asc").SetSparse(true), // Sparse, т.к. поле может отсутствовать
	}

	// Индекс для user_id (если нужен поиск по пользователю)
	userIndex := mongo.IndexModel{
		Keys:    bson.D{{Key: "user_id", Value: 1}},
		Options: options.Index().SetName("user_id_asc").SetSparse(true),
	}

	// Векторный индекс (если включена долгосрочная память)
	var indexesToCreate []mongo.IndexModel
	indexesToCreate = append(indexesToCreate, dateIndex, replyIndex, userIndex)

	if ms.cfg.LongTermMemoryEnabled {
		log.Printf("[Index INFO] Chat (%s): LongTermMemoryEnabled=true. Векторный индекс типа Atlas Search должен быть создан вручную через UI/API Atlas, а не через код драйвера.", coll.Name())
		/*
			// --- ЭТОТ КОД НЕВЕРЕН ДЛЯ ATLAS И УДАЛЕН ---
			vectorIndex := mongo.IndexModel{
				Keys: bson.M{
					"message_vector": "cosmosSearch", // НЕПРАВИЛЬНО для Atlas
				},
				Options: options.Index().SetName(ms.cfg.MongoVectorIndexName).SetWeights(bson.M{
					"numDimensions": 1024,  // Зависит от модели
					"similarity":    "COS",
				}),
			}
			indexesToCreate = append(indexesToCreate, vectorIndex)
		*/
	}

	if len(indexesToCreate) > 0 {
		_, err := coll.Indexes().CreateMany(ctx, indexesToCreate)
		if err != nil {
			// Проверяем, не является ли ошибка "индекс уже существует"
			// Коды ошибок MongoDB могут отличаться, это примерная проверка
			if mongo.IsDuplicateKeyError(err) || strings.Contains(err.Error(), "index already exists") || strings.Contains(err.Error(), "IndexOptionsConflict") {
				// log.Printf("[DEBUG] Индексы для коллекции '%s' уже существуют.", coll.Name())
				return nil // Не считаем ошибкой, если индексы уже есть
			}
			return fmt.Errorf("ошибка создания индексов для коллекции %s: %w", coll.Name(), err)
		}
	}
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

	count, err := ms.getMessagesCollection(chatID).CountDocuments(ctx, bson.M{"chat_id": chatID})
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
	// Получаем правильную коллекцию
	coll := ms.getMessagesCollection(chatID)
	// Вызываем функцию из mongo_embeddings.go
	return GetTotalMessagesCount(ctx, coll, chatID) // Передаем coll
}

// FindMessagesWithoutEmbedding вызывает реализацию из mongo_embeddings.go.
func (ms *MongoStorage) FindMessagesWithoutEmbedding(chatID int64, limit int, skipMessageIDs []int) ([]MongoMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // Увеличим таймаут
	defer cancel()
	// Получаем правильную коллекцию
	coll := ms.getMessagesCollection(chatID)
	// Вызываем функцию из mongo_embeddings.go
	return FindMessagesWithoutEmbedding(ctx, coll, chatID, limit, skipMessageIDs) // Передаем coll
}

// UpdateMessageEmbedding вызывает реализацию из mongo_embeddings.go.
func (ms *MongoStorage) UpdateMessageEmbedding(chatID int64, messageID int, vector []float32) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Получаем правильную коллекцию
	coll := ms.getMessagesCollection(chatID)
	// Вызываем функцию из mongo_embeddings.go, передавая ms.debug
	return UpdateMessageEmbedding(ctx, coll, chatID, messageID, vector, ms.debug) // Передаем coll
}

// SearchRelevantMessages вызывает реализацию из mongo_embeddings.go.
func (ms *MongoStorage) SearchRelevantMessages(chatID int64, queryText string, k int) ([]*tgbotapi.Message, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second) // Увеличим таймаут для поиска
	defer cancel()
	// Получаем правильную коллекцию
	coll := ms.getMessagesCollection(chatID)
	// Вызываем функцию из mongo_embeddings.go, передавая ms.debug
	// Убедимся, что передаем правильный embeddingClient
	return SearchRelevantMessages(ctx, ms.cfg, coll, ms.embeddingClient, chatID, queryText, k, ms.debug) // Передаем coll
}

// --- Вспомогательная функция для усечения строки (перенесена в utils) ---

// --- Конец реализации интерфейса ChatHistoryStorage ---
