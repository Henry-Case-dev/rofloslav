package storage

import (
	"context"
	"encoding/json"
	"errors" // Для проверки ошибок MongoDB
	"fmt"
	"log"
	"reflect"
	"strings"
	"time"

	"github.com/Henry-Case-dev/rofloslav/internal/config" // Исправлен путь импорта конфига
	"github.com/Henry-Case-dev/rofloslav/internal/llm"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Убедимся, что MongoStorage реализует интерфейс ChatHistoryStorage.
var _ ChatHistoryStorage = (*MongoStorage)(nil)

// Имя коллекции для настроек чата
const settingsCollectionName = "chat_settings"

// MongoMessage представляет структуру сообщения для хранения в MongoDB.
type MongoMessage struct {
	ID               primitive.ObjectID       `bson:"_id,omitempty"`
	ChatID           int64                    `bson:"chat_id"`
	MessageID        int                      `bson:"message_id"`
	UserID           int64                    `bson:"user_id,omitempty"`
	Username         string                   `bson:"username,omitempty"`
	FirstName        string                   `bson:"first_name,omitempty"`
	LastName         string                   `bson:"last_name,omitempty"`
	IsBot            bool                     `bson:"is_bot,omitempty"`
	Date             time.Time                `bson:"date"` // Используем time.Time для сортировки
	Text             string                   `bson:"text,omitempty"`
	ReplyToMessageID int                      `bson:"reply_to_message_id,omitempty"`
	Entities         []tgbotapi.MessageEntity `bson:"entities,omitempty"`
	// Новые поля:
	Caption         string                   `bson:"caption,omitempty"`          // Текст подписи к медиа
	CaptionEntities []tgbotapi.MessageEntity `bson:"caption_entities,omitempty"` // Форматирование подписи
	HasMedia        bool                     `bson:"has_media,omitempty"`        // Флаг наличия медиа
	IsVoice         bool                     `bson:"is_voice,omitempty"`         // Флаг, что сообщение из аудио
	MessageVector   []float32                `bson:"message_vector,omitempty"`   // Векторное представление сообщения
}

// convertMongoToAPIMessage преобразует MongoMessage обратно в *tgbotapi.Message
func convertMongoToAPIMessage(mongoMsg *MongoMessage) *tgbotapi.Message {
	if mongoMsg == nil {
		return nil
	}

	msg := &tgbotapi.Message{
		MessageID: mongoMsg.MessageID,
		From: &tgbotapi.User{
			ID:        mongoMsg.UserID,
			IsBot:     mongoMsg.IsBot,
			FirstName: mongoMsg.FirstName,
			LastName:  mongoMsg.LastName,
			UserName:  mongoMsg.Username,
		},
		Date:     int(mongoMsg.Date.Unix()),           // Преобразуем time.Time обратно в Unix timestamp
		Chat:     &tgbotapi.Chat{ID: mongoMsg.ChatID}, // Добавляем ChatID
		Text:     mongoMsg.Text,
		Entities: mongoMsg.Entities,
	}

	// Информацию об ответе не храним в MongoMessage, поэтому не восстанавливаем
	// msg.ReplyToMessage = ...

	// Добавляем префикс для голосовых сообщений
	if mongoMsg.IsVoice {
		msg.Text = fmt.Sprintf("[Голосовое]: %s", msg.Text)
	}

	return msg
}

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
}

// NewMongoStorage создает новый экземпляр MongoStorage.
// Принимает URI, имя БД, имена коллекций, конфиг и LLM клиент.
func NewMongoStorage(mongoURI, dbName, messagesCollectionName, userProfilesCollectionName string, cfg *config.Config, llmClient llm.LLMClient) (*MongoStorage, error) {
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
		debug:                  cfg.Debug, // Берем из конфига
		llmClient:              llmClient, // Сохраняем LLM клиент
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

// --- Реализация методов интерфейса ChatHistoryStorage ---

// AddMessage добавляет одно сообщение в историю чата MongoDB.
func (ms *MongoStorage) AddMessage(chatID int64, message *tgbotapi.Message) {
	// Проверяем, нужно ли вообще сохранять это сообщение (например, исключаем системные)
	if message == nil || message.Text == "" && message.Caption == "" && message.Voice == nil && message.Audio == nil && message.Video == nil && message.Photo == nil {
		// Пропускаем сообщения без текстового контента и не являющиеся голосовыми/медиа
		// (можно расширить логику, если нужно хранить другие типы)
		if ms.debug {
			log.Printf("[Mongo AddMessage SKIP] Чат %d: Сообщение ID %d пропущено (нет текста/caption/медиа).", chatID, message.MessageID)
		}
		return
	}

	// Конвертируем в формат MongoDB
	mongoMsg := convertAPIToMongoMessage(chatID, message)
	if mongoMsg == nil {
		log.Printf("[ERROR][Mongo AddMessage] Чат %d: Не удалось конвертировать сообщение ID %d в MongoMessage.", chatID, message.MessageID)
		return
	}

	// --- Генерация эмбеддинга перед сохранением ---
	if ms.cfg.LongTermMemoryEnabled && ms.llmClient != nil {
		textToEmbed := ""
		if mongoMsg.Text != "" {
			textToEmbed = mongoMsg.Text
		} else if mongoMsg.Caption != "" {
			textToEmbed = mongoMsg.Caption
		}

		if textToEmbed != "" {
			if ms.debug {
				log.Printf("[Mongo AddMessage DEBUG] Чат %d: Попытка генерации эмбеддинга для сообщения ID %d...", chatID, mongoMsg.MessageID)
			}
			vector, err := ms.llmClient.EmbedContent(textToEmbed)
			if err != nil {
				// Логируем ошибку, но НЕ прерываем сохранение сообщения
				log.Printf("[WARN][Mongo AddMessage] Чат %d, Msg %d: Ошибка генерации эмбеддинга при добавлении: %v. Сообщение будет сохранено без вектора.", chatID, mongoMsg.MessageID, err)
			} else {
				mongoMsg.MessageVector = vector // Сохраняем вектор
				if ms.debug {
					log.Printf("[Mongo AddMessage DEBUG] Чат %d, Msg %d: Эмбеддинг успешно сгенерирован (размерность %d).", chatID, mongoMsg.MessageID, len(vector))
				}
			}
		} else {
			if ms.debug {
				log.Printf("[Mongo AddMessage DEBUG] Чат %d, Msg %d: Нет текста для генерации эмбеддинга.", chatID, mongoMsg.MessageID)
			}
		}
	}
	// --- Конец генерации эмбеддинга ---

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second) // Таймаут на вставку
	defer cancel()

	_, err := ms.messagesCollection.InsertOne(ctx, mongoMsg)
	if err != nil {
		log.Printf("[ERROR][Mongo AddMessage] Чат %d: Ошибка вставки сообщения ID %d в MongoDB: %v", chatID, message.MessageID, err)
		// TODO: Обработка ошибок (например, повторная попытка?)
	} else if ms.debug {
		log.Printf("[Mongo AddMessage DEBUG] Чат %d: Сообщение ID %d успешно вставлено в MongoDB с ID: %v.", chatID, message.MessageID, mongoMsg.ID)
	}
}

// convertAPIToMongoMessage преобразует сообщение из Telegram API в формат для MongoDB
func convertAPIToMongoMessage(chatID int64, apiMsg *tgbotapi.Message) *MongoMessage {
	if apiMsg == nil {
		return nil
	}

	mongoMsg := &MongoMessage{
		ChatID:           chatID,
		MessageID:        apiMsg.MessageID,
		Text:             apiMsg.Text,
		Date:             apiMsg.Time(), // Используем time.Time
		ReplyToMessageID: 0,
		Entities:         apiMsg.Entities,
		// Caption и другие поля по аналогии
	}

	if apiMsg.From != nil {
		mongoMsg.UserID = apiMsg.From.ID
		mongoMsg.Username = apiMsg.From.UserName
		mongoMsg.FirstName = apiMsg.From.FirstName // Сохраняем оригинальные FirstName/LastName в сообщении
		mongoMsg.LastName = apiMsg.From.LastName
		mongoMsg.IsBot = apiMsg.From.IsBot
	}

	if apiMsg.ReplyToMessage != nil {
		mongoMsg.ReplyToMessageID = apiMsg.ReplyToMessage.MessageID
		// Можно также сохранять ReplyToMessage как вложенный документ, если нужно
	}

	// Добавляем обработку Caption для медиа
	if apiMsg.Caption != "" {
		mongoMsg.Caption = apiMsg.Caption
		mongoMsg.CaptionEntities = apiMsg.CaptionEntities
	}

	// Добавляем информацию о медиа (простой флаг)
	mongoMsg.HasMedia = mongoMsg.Caption != "" || (apiMsg.Photo != nil || apiMsg.Video != nil || apiMsg.Document != nil || apiMsg.Audio != nil || apiMsg.Voice != nil || apiMsg.Sticker != nil)
	mongoMsg.IsVoice = apiMsg.Voice != nil // Устанавливаем флаг, если поле Voice не nil

	return mongoMsg
}

// GetMessages возвращает последние сообщения из MongoDB для указанного chatID
func (ms *MongoStorage) GetMessages(chatID int64) []*tgbotapi.Message {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	filter := bson.M{"chat_id": chatID}
	findOptions := options.Find().
		SetSort(bson.D{{"date", -1}}).        // Сортируем по дате, новые первыми
		SetLimit(int64(ms.cfg.ContextWindow)) // Используем cfg.ContextWindow

	cursor, err := ms.messagesCollection.Find(ctx, filter, findOptions)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			if ms.debug {
				log.Printf("[Mongo GetMessages DEBUG] Сообщения для chatID %d не найдены.", chatID)
			}
			return []*tgbotapi.Message{}
		}
		log.Printf("[Mongo GetMessages ERROR] Ошибка поиска сообщений для chatID %d: %v", chatID, err)
		return nil // Возвращаем nil в случае ошибки запроса
	}
	defer cursor.Close(ctx)

	var results []*tgbotapi.Message
	for cursor.Next(ctx) {
		var mongoMsg MongoMessage
		if err := cursor.Decode(&mongoMsg); err != nil {
			log.Printf("[Mongo GetMessages ERROR] Ошибка декодирования сообщения для chatID %d: %v", chatID, err)
			continue // Пропускаем поврежденное сообщение
		}
		apiMsg := convertMongoToAPIMessage(&mongoMsg)
		if apiMsg != nil {
			results = append(results, apiMsg)
		}
	}

	if err := cursor.Err(); err != nil {
		log.Printf("[Mongo GetMessages ERROR] Ошибка курсора после итерации для chatID %d: %v", chatID, err)
	}

	// Разворачиваем срез, т.к. сортировали от новых к старым
	for i, j := 0, len(results)-1; i < j; i, j = i+1, j-1 {
		results[i], results[j] = results[j], results[i]
	}

	if ms.debug {
		log.Printf("[Mongo GetMessages DEBUG] Запрошено %d сообщений для chatID %d.", len(results), chatID)
	}

	return results
}

func (ms *MongoStorage) GetMessagesSince(chatID int64, since time.Time) []*tgbotapi.Message {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	filter := bson.M{
		"chat_id": chatID,
		"date":    bson.M{"$gt": since}, // Исправлено: убран ненужный \ перед $
	}

	// Сортируем по дате, старые первыми, лимит не нужен
	findOptions := options.Find().SetSort(bson.D{{"date", 1}})

	cursor, err := ms.messagesCollection.Find(ctx, filter, findOptions)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			if ms.debug {
				log.Printf("[Mongo GetMessagesSince DEBUG] Новые сообщения для chatID %d с %s не найдены.", chatID, since)
			}
			return []*tgbotapi.Message{}
		}
		log.Printf("[Mongo GetMessagesSince ERROR] Ошибка поиска сообщений для chatID %d с %s: %v", chatID, since, err)
		return nil // Возвращаем nil в случае ошибки запроса
	}
	defer cursor.Close(ctx)

	var results []*tgbotapi.Message
	for cursor.Next(ctx) {
		var mongoMsg MongoMessage
		if err := cursor.Decode(&mongoMsg); err != nil {
			log.Printf("[Mongo GetMessagesSince ERROR] Ошибка декодирования сообщения для chatID %d: %v", chatID, err)
			continue // Пропускаем поврежденное сообщение
		}
		apiMsg := convertMongoToAPIMessage(&mongoMsg)
		if apiMsg != nil {
			results = append(results, apiMsg)
		}
	}

	if err := cursor.Err(); err != nil {
		log.Printf("[Mongo GetMessagesSince ERROR] Ошибка курсора после итерации для chatID %d: %v", chatID, err)
	}

	if ms.debug {
		log.Printf("[Mongo GetMessagesSince DEBUG] Найдено %d новых сообщений для chatID %d с %s.", len(results), chatID, since)
	}

	return results
}

func (ms *MongoStorage) LoadChatHistory(chatID int64) ([]*tgbotapi.Message, error) {
	// MongoDB не требует предварительной загрузки всей истории в память.
	log.Printf("[Mongo LoadChatHistory STUB] ChatID: %d (неприменимо)", chatID)
	return nil, nil // Возвращаем nil, nil, как и для PostgreSQL
}

func (ms *MongoStorage) SaveChatHistory(chatID int64) error {
	// Сохранение происходит при каждом AddMessage.
	log.Printf("[Mongo SaveChatHistory STUB] ChatID: %d (неприменимо)", chatID)
	return nil
}

func (ms *MongoStorage) ClearChatHistory(chatID int64) error {
	// TODO: Реализовать удаление истории чата из MongoDB
	log.Printf("[Mongo ClearChatHistory STUB] ChatID: %d", chatID)
	return nil
}

// AddMessagesToContext добавляет массив сообщений в контекст чата MongoDB.
// Используется в основном для загрузки истории.
func (ms *MongoStorage) AddMessagesToContext(chatID int64, messages []*tgbotapi.Message) {
	if len(messages) == 0 {
		return // Нечего добавлять
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second) // Увеличим таймаут для возможной долгой вставки
	defer cancel()

	var mongoMessages []interface{} // InsertMany ожидает []interface{}
	for _, apiMsg := range messages {
		if apiMsg == nil {
			continue
		}
		// Конвертируем в MongoMessage
		mongoMsg := MongoMessage{
			ChatID:           chatID, // Используем chatID из аргумента функции
			MessageID:        apiMsg.MessageID,
			Text:             apiMsg.Text,
			Date:             apiMsg.Time(), // Используем time.Time
			ReplyToMessageID: 0,             // Инициализируем нулем
			Entities:         apiMsg.Entities,
		}
		if apiMsg.From != nil {
			mongoMsg.UserID = apiMsg.From.ID
			mongoMsg.Username = apiMsg.From.UserName
			mongoMsg.FirstName = apiMsg.From.FirstName
			mongoMsg.LastName = apiMsg.From.LastName
			mongoMsg.IsBot = apiMsg.From.IsBot
		}
		if apiMsg.ReplyToMessage != nil {
			mongoMsg.ReplyToMessageID = apiMsg.ReplyToMessage.MessageID
		}
		mongoMessages = append(mongoMessages, mongoMsg)
	}

	if len(mongoMessages) == 0 {
		if ms.debug {
			log.Printf("[Mongo AddToContext DEBUG] Чат %d: Нет валидных сообщений для добавления после конвертации.", chatID)
		}
		return
	}

	_, err := ms.messagesCollection.InsertMany(ctx, mongoMessages)
	if err != nil {
		log.Printf("[Mongo AddToContext ERROR] Чат %d: Ошибка вставки %d сообщений: %v", chatID, len(mongoMessages), err)
	} else {
		if ms.debug {
			log.Printf("[Mongo AddToContext DEBUG] Чат %d: Успешно добавлено %d сообщений в MongoDB.", chatID, len(mongoMessages))
		}
	}
}

// GetAllChatIDs возвращает все уникальные chatID из MongoDB.
func (ms *MongoStorage) GetAllChatIDs() ([]int64, error) {
	// TODO: Реализовать получение уникальных chat_id из MongoDB
	log.Printf("[Mongo GetAllChatIDs STUB]")
	return nil, nil
}

// --- Методы для профилей пользователей ---

// GetUserProfile возвращает профиль пользователя из MongoDB.
func (ms *MongoStorage) GetUserProfile(chatID int64, userID int64) (*UserProfile, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	filter := bson.M{"chat_id": chatID, "user_id": userID}
	var profile UserProfile

	err := ms.userProfilesCollection.FindOne(ctx, filter).Decode(&profile)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			if ms.debug {
				log.Printf("DEBUG: Профиль пользователя userID %d в чате %d не найден в MongoDB.", userID, chatID)
			}
			return nil, nil // Не найдено - не ошибка
		}
		log.Printf("ERROR: Ошибка получения профиля пользователя из MongoDB (чат %d, user %d): %v", chatID, userID, err)
		return nil, fmt.Errorf("ошибка запроса профиля пользователя: %w", err)
	}

	if ms.debug {
		log.Printf("DEBUG: Профиль пользователя userID %d в чате %d успешно получен из MongoDB.", userID, chatID)
	}
	return &profile, nil
}

// SetUserProfile создает или обновляет профиль пользователя в MongoDB (UPSERT).
func (ms *MongoStorage) SetUserProfile(profile *UserProfile) error {
	if profile == nil || profile.ChatID == 0 || profile.UserID == 0 {
		log.Printf("[Mongo SetUserProfile WARN] Попытка сохранить невалидный профиль: ChatID=%d, UserID=%d", profile.ChatID, profile.UserID)
		return fmt.Errorf("невалидный профиль для сохранения (nil, chat_id=0 или user_id=0)")
	}

	if ms.debug {
		log.Printf("[Mongo SetUserProfile DEBUG] Попытка сохранения профиля: ChatID=%d, UserID=%d, Username=%s, Alias=%s, Gender=%s, RealName=%s, Bio=%s, LastSeen=%s",
			profile.ChatID, profile.UserID, profile.Username, profile.Alias, profile.Gender, profile.RealName, profile.Bio, profile.LastSeen)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Фильтр для поиска существующего документа
	filter := bson.M{"chat_id": profile.ChatID, "user_id": profile.UserID}

	// Данные для обновления или вставки
	// Используем $set для обновления всех полей, кроме chat_id и user_id, которые в фильтре
	// Используем $setOnInsert для полей, которые нужно установить только при создании документа
	update := bson.M{
		"$set": bson.M{
			// Обновляем основные данные из Telegram при каждом сохранении
			"username":  profile.Username, // Может меняться
			"alias":     profile.Alias,    // Обновляем Alias (бывший FirstName)
			"gender":    profile.Gender,   // Обновляем Gender (бывший LastName)
			"last_seen": profile.LastSeen, // Обновляем время последней активности
			// Обновляем кастомные поля, если они были изменены
			"real_name": profile.RealName,
			"bio":       profile.Bio,
		},
		"$setOnInsert": bson.M{
			"chat_id": profile.ChatID,
			"user_id": profile.UserID,
		},
	}

	// Опции для выполнения Upsert (Update or Insert)
	opts := options.Update().SetUpsert(true)

	if ms.debug {
		log.Printf("[Mongo SetUserProfile DEBUG] Выполнение Upsert для ChatID=%d, UserID=%d", profile.ChatID, profile.UserID)
	}

	// Выполняем операцию
	result, err := ms.userProfilesCollection.UpdateOne(ctx, filter, update, opts)
	if err != nil {
		log.Printf("[Mongo SetUserProfile ERROR] Ошибка при Upsert профиля ChatID=%d, UserID=%d: %v", profile.ChatID, profile.UserID, err)
		return fmt.Errorf("ошибка сохранения профиля пользователя: %w", err)
	}

	if ms.debug {
		if result.UpsertedCount > 0 {
			log.Printf("[Mongo SetUserProfile DEBUG] Профиль для ChatID=%d, UserID=%d успешно создан (UpsertedID: %v).", profile.ChatID, profile.UserID, result.UpsertedID)
		} else if result.ModifiedCount > 0 {
			log.Printf("[Mongo SetUserProfile DEBUG] Профиль для ChatID=%d, UserID=%d успешно обновлен.", profile.ChatID, profile.UserID)
		} else if result.MatchedCount > 0 {
			log.Printf("[Mongo SetUserProfile DEBUG] Профиль для ChatID=%d, UserID=%d найден, но не изменен (данные совпадают).", profile.ChatID, profile.UserID)
		} else {
			// Эта ветка не должна достигаться при upsert=true, если не было ошибки
			log.Printf("[Mongo SetUserProfile WARN] Upsert для ChatID=%d, UserID=%d завершился без ошибки, но результат неопределен: Matched=%d, Modified=%d, Upserted=%d",
				profile.ChatID, profile.UserID, result.MatchedCount, result.ModifiedCount, result.UpsertedCount)
		}
	}

	return nil
}

// GetAllUserProfiles возвращает все профили пользователей для указанного чата из MongoDB.
func (ms *MongoStorage) GetAllUserProfiles(chatID int64) ([]*UserProfile, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	filter := bson.M{"chat_id": chatID}
	// Сортируем по last_seen для релевантности
	findOptions := options.Find().SetSort(bson.D{{Key: "last_seen", Value: -1}})

	cursor, err := ms.userProfilesCollection.Find(ctx, filter, findOptions)
	if err != nil {
		log.Printf("ERROR: Ошибка получения всех профилей пользователей из MongoDB (чат %d): %v", chatID, err)
		return nil, fmt.Errorf("ошибка запроса профилей: %w", err)
	}
	defer cursor.Close(ctx)

	var profiles []*UserProfile
	if err = cursor.All(ctx, &profiles); err != nil {
		log.Printf("ERROR: Ошибка декодирования профилей пользователей из MongoDB (чат %d): %v", chatID, err)
		return nil, fmt.Errorf("ошибка декодирования профилей: %w", err)
	}

	// Важно: если профилей нет, cursor.All вернет пустой слайс и nil ошибку.
	// Проверка на profiles == nil не нужна.

	if ms.debug {
		log.Printf("DEBUG: Получено %d профилей пользователей из MongoDB для чата %d.", len(profiles), chatID)
	}
	return profiles, nil
}

// GetStatus для MongoStorage
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
	// --- Проверка дефолтов для лимита прямых обращений ---
	if settings.DirectReplyLimitEnabled == nil {
		enabled := ms.cfg.DirectReplyLimitEnabledDefault
		settings.DirectReplyLimitEnabled = &enabled
		needsUpdate = true
	}
	if settings.DirectReplyLimitCount == nil {
		count := ms.cfg.DirectReplyLimitCountDefault
		settings.DirectReplyLimitCount = &count
		needsUpdate = true
	}
	if settings.DirectReplyLimitDuration == nil {
		durationMinutes := int(ms.cfg.DirectReplyLimitDurationDefault.Minutes())
		settings.DirectReplyLimitDuration = &durationMinutes
		needsUpdate = true
	}
	return needsUpdate
}

// ensureChatSettings создает или получает настройки чата.
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
	defaultDirectLimitEnabled := ms.cfg.DirectReplyLimitEnabledDefault
	defaultDirectLimitCount := ms.cfg.DirectReplyLimitCountDefault
	directLimitDurationMinutes := int(ms.cfg.DirectReplyLimitDurationDefault.Minutes())

	newSettings := ChatSettings{
		ChatID:                    chatID,
		ConversationStyle:         ms.cfg.DefaultConversationStyle,
		Temperature:               &defaultTemp,
		Model:                     ms.cfg.DefaultModel,
		GeminiSafetyThreshold:     ms.cfg.DefaultSafetyThreshold,
		VoiceTranscriptionEnabled: &defaultVoiceEnabled,
		// --- Добавляем дефолтные значения для лимита ---
		DirectReplyLimitEnabled:  &defaultDirectLimitEnabled,
		DirectReplyLimitCount:    &defaultDirectLimitCount,
		DirectReplyLimitDuration: &directLimitDurationMinutes, // Сохраняем в БД как минуты
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
				// Обработка указателей (Temperature, VoiceTranscriptionEnabled, DirectReplyLimit*)
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
					// Обработка нулевых значений НЕ-указателей
					// Для ConversationStyle, Model, GeminiSafetyThreshold пустая строка/0 - валидное значение,
					// которое должно быть установлено.
					// Для других полей (если появятся) может понадобиться другая логика.
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

// --- Новые методы для обновления отдельных настроек лимитов ---

// UpdateDirectLimitEnabled обновляет только поле direct_reply_limit_enabled
func (ms *MongoStorage) UpdateDirectLimitEnabled(chatID int64, enabled bool) error {
	collection := ms.settingsCollection
	if collection == nil {
		return fmt.Errorf("коллекция настроек не инициализирована")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	filter := bson.M{"chat_id": chatID}
	update := bson.M{"$set": bson.M{"direct_reply_limit_enabled": enabled}}
	opts := options.Update().SetUpsert(true) // Upsert на случай, если настроек еще нет

	_, err := collection.UpdateOne(ctx, filter, update, opts)
	if err != nil {
		log.Printf("[ERROR][UpdateDirectLimitEnabled] Ошибка обновления настройки для чата %d: %v", chatID, err)
		return fmt.Errorf("ошибка обновления флага лимита: %w", err)
	}
	if ms.debug {
		log.Printf("[DEBUG][UpdateDirectLimitEnabled] Настройка лимита для чата %d установлена в %t.", chatID, enabled)
	}
	return nil
}

// UpdateDirectLimitCount обновляет только поле direct_reply_limit_count
func (ms *MongoStorage) UpdateDirectLimitCount(chatID int64, count int) error {
	if count < 0 {
		return fmt.Errorf("количество должно быть не отрицательным")
	}
	collection := ms.settingsCollection
	if collection == nil {
		return fmt.Errorf("коллекция настроек не инициализирована")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	filter := bson.M{"chat_id": chatID}
	update := bson.M{"$set": bson.M{"direct_reply_limit_count": count}}
	opts := options.Update().SetUpsert(true) // Upsert на случай, если настроек еще нет

	_, err := collection.UpdateOne(ctx, filter, update, opts)
	if err != nil {
		log.Printf("[ERROR][UpdateDirectLimitCount] Ошибка обновления настройки для чата %d: %v", chatID, err)
		return fmt.Errorf("ошибка обновления количества лимита: %w", err)
	}
	if ms.debug {
		log.Printf("[DEBUG][UpdateDirectLimitCount] Количество лимита для чата %d установлено в %d.", chatID, count)
	}
	return nil
}

// UpdateDirectLimitDuration обновляет только поле direct_reply_limit_duration_minutes
func (ms *MongoStorage) UpdateDirectLimitDuration(chatID int64, duration time.Duration) error {
	if duration <= 0 {
		return fmt.Errorf("длительность должна быть положительной")
	}
	durationMinutes := int(duration.Minutes()) // Сохраняем в минутах

	collection := ms.settingsCollection
	if collection == nil {
		return fmt.Errorf("коллекция настроек не инициализирована")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	filter := bson.M{"chat_id": chatID}
	update := bson.M{"$set": bson.M{"direct_reply_limit_duration_minutes": durationMinutes}}
	opts := options.Update().SetUpsert(true) // Upsert на случай, если настроек еще нет

	_, err := collection.UpdateOne(ctx, filter, update, opts)
	if err != nil {
		log.Printf("[ERROR][UpdateDirectLimitDuration] Ошибка обновления настройки для чата %d: %v", chatID, err)
		return fmt.Errorf("ошибка обновления длительности лимита: %w", err)
	}
	if ms.debug {
		log.Printf("[DEBUG][UpdateDirectLimitDuration] Длительность лимита для чата %d установлена в %d минут.", chatID, durationMinutes)
	}
	return nil
}

// SearchRelevantMessages ищет сообщения, семантически близкие к queryText,
// используя MongoDB Atlas Vector Search.
func (ms *MongoStorage) SearchRelevantMessages(chatID int64, queryText string, k int) ([]*tgbotapi.Message, error) {
	if ms.llmClient == nil {
		return nil, fmt.Errorf("LLM клиент не инициализирован в MongoStorage для поиска эмбеддингов")
	}
	if ms.cfg.MongoVectorIndexName == "" {
		return nil, fmt.Errorf("имя векторного индекса (MONGO_VECTOR_INDEX_NAME) не задано в конфигурации")
	}
	if queryText == "" {
		return []*tgbotapi.Message{}, nil // Если запрос пустой, возвращаем пустой результат без ошибки
	}

	if ms.debug {
		// Упрощаем лог, убирая truncateString
		log.Printf("[DEBUG][Mongo Vector Search] Chat %d: Запрос поиска %d релевантных сообщений для текста, начинающегося с: '%s...'", chatID, k, queryText[:min(len(queryText), 50)])
	}

	// 1. Генерируем вектор для запроса
	queryVector, err := ms.llmClient.EmbedContent(queryText)
	if err != nil {
		log.Printf("[ERROR][Mongo Vector Search] Chat %d: Ошибка генерации эмбеддинга для запроса: %v", chatID, err)
		return nil, fmt.Errorf("ошибка генерации эмбеддинга запроса: %w", err)
	}
	if len(queryVector) == 0 {
		log.Printf("[WARN][Mongo Vector Search] Chat %d: Получен пустой эмбеддинг для запроса.", chatID)
		return []*tgbotapi.Message{}, nil // Пустой эмбеддинг - нет результата
	}

	if ms.debug {
		log.Printf("[DEBUG][Mongo Vector Search] Chat %d: Эмбеддинг запроса сгенерирован (размерность %d).", chatID, len(queryVector))
	}

	// 2. Формируем pipeline для $vectorSearch
	// Используем официальную документацию: https://www.mongodb.com/docs/atlas/atlas-vector-search/vector-search-stage/
	pipeline := mongo.Pipeline{
		{
			{"$vectorSearch", bson.D{
				{"index", ms.cfg.MongoVectorIndexName},
				{"queryVector", queryVector},
				{"path", "message_vector"},       // Поле, содержащее векторы в документах
				{"numCandidates", int64(k * 10)}, // Искать среди большего числа кандидатов для точности (можно настроить)
				{"limit", int64(k)},              // Вернуть только top K результатов
				// Добавляем фильтр по chat_id, чтобы искать только в текущем чате
				{"filter", bson.D{{"chat_id", chatID}}},
			}},
		},
		// Опционально: добавляем $project для исключения поля message_vector из результата
		// (чтобы не передавать большие векторы обратно)
		{
			{"$project", bson.D{
				{"message_vector", 0}, // Исключаем поле message_vector
				{"score", 0},          // Также исключаем поле score, если оно не нужно
			}},
		},
	}

	if ms.debug {
		// Логируем pipeline (может быть полезно для отладки)
		pipelineBytes, _ := json.MarshalIndent(pipeline, "", "  ")
		log.Printf("[DEBUG][Mongo Vector Search] Chat %d: Pipeline:\n%s", chatID, string(pipelineBytes))
	}

	// 3. Выполняем агрегацию
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // Увеличим таймаут для поиска
	defer cancel()

	cursor, err := ms.messagesCollection.Aggregate(ctx, pipeline)
	if err != nil {
		log.Printf("[ERROR][Mongo Vector Search] Chat %d: Ошибка выполнения Aggregate: %v", chatID, err)
		return nil, fmt.Errorf("ошибка выполнения векторного поиска: %w", err)
	}
	defer cursor.Close(ctx)

	// 4. Декодируем результаты
	var mongoResults []MongoMessage
	if err = cursor.All(ctx, &mongoResults); err != nil {
		log.Printf("[ERROR][Mongo Vector Search] Chat %d: Ошибка декодирования результатов: %v", chatID, err)
		return nil, fmt.Errorf("ошибка декодирования результатов поиска: %w", err)
	}

	if ms.debug {
		log.Printf("[DEBUG][Mongo Vector Search] Chat %d: Найдено %d релевантных сообщений.", chatID, len(mongoResults))
	}

	// 5. Конвертируем в tgbotapi.Message
	apiResults := make([]*tgbotapi.Message, 0, len(mongoResults))
	for i := range mongoResults { // Используем индекс для взятия адреса
		apiMsg := convertMongoToAPIMessage(&mongoResults[i])
		if apiMsg != nil {
			apiResults = append(apiResults, apiMsg)
		}
	}

	return apiResults, nil
}

// --- Вспомогательные методы для бэкфилла эмбеддингов ---

// GetTotalMessagesCount возвращает примерное общее количество сообщений в чате.
func (ms *MongoStorage) GetTotalMessagesCount(chatID int64) (int64, error) {
	filter := bson.M{"chat_id": chatID}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	count, err := ms.messagesCollection.EstimatedDocumentCount(ctx, options.EstimatedDocumentCount().SetMaxTime(5*time.Second))
	if err != nil {
		// Попробуем CountDocuments как fallback (может быть медленнее)
		count, err = ms.messagesCollection.CountDocuments(ctx, filter)
		if err != nil {
			return -1, fmt.Errorf("ошибка подсчета документов: %w", err)
		}
	}
	return count, nil
}

// FindMessagesWithoutEmbedding ищет до `limit` сообщений в чате, у которых отсутствует поле `message_vector`,
// и либо поле `text`, либо `caption` существует и не является пустой строкой.
// Исключает сообщения с message_id из `skipMessageIDs`.
func (ms *MongoStorage) FindMessagesWithoutEmbedding(chatID int64, limit int, skipMessageIDs []int) ([]MongoMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	filter := bson.M{
		"chat_id":        chatID,
		"message_vector": bson.M{"$exists": false},
		"$or": []bson.M{
			{"text": bson.M{"$exists": true, "$ne": ""}},
			{"caption": bson.M{"$exists": true, "$ne": ""}},
		},
	}

	// Добавляем условие для исключения ID, если список не пустой
	if len(skipMessageIDs) > 0 {
		filter["message_id"] = bson.M{"$nin": skipMessageIDs}
	}

	opts := options.Find().SetLimit(int64(limit)).SetSort(bson.D{{Key: "date", Value: 1}}) // Сортируем по дате, чтобы обрабатывать старые

	cursor, err := ms.messagesCollection.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("ошибка поиска сообщений без эмбеддинга: %w", err)
	}
	defer cursor.Close(ctx)

	var results []MongoMessage // Возвращаем MongoMessage напрямую
	if err = cursor.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("ошибка декодирования сообщений без эмбеддинга: %w", err)
	}

	return results, nil
}

// UpdateMessageEmbedding обновляет поле message_vector для конкретного сообщения.
func (ms *MongoStorage) UpdateMessageEmbedding(chatID int64, messageID int, vector []float32) error {
	filter := bson.M{
		"chat_id":    chatID,
		"message_id": messageID,
	}
	update := bson.M{
		"$set": bson.M{"message_vector": vector},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := ms.messagesCollection.UpdateOne(ctx, filter, update)
	if err != nil {
		log.Printf("[UpdateEmbedding ERROR] Chat %d, Msg %d: Ошибка MongoDB при обновлении: %v", chatID, messageID, err)
		return fmt.Errorf("ошибка обновления эмбеддинга в MongoDB: %w", err)
	}

	// Проверяем результат операции
	if result.MatchedCount == 0 {
		// Документ с таким chat_id и message_id вообще не найден.
		log.Printf("[UpdateEmbedding ERROR] Chat %d, Msg %d: Документ не найден для обновления. MatchedCount=0", chatID, messageID)
		return fmt.Errorf("документ %d не найден в чате %d", messageID, chatID)
	}

	if result.ModifiedCount == 0 {
		// Документ найден, но не был изменен. Это может означать, что:
		// 1. Вектор уже был установлен точно таким же значением (маловероятно с float).
		// 2. Какая-то проблема с MongoDB.
		// Считаем это ошибкой для логики бэкфилла, так как обновление не прошло как ожидалось.
		log.Printf("[UpdateEmbedding WARN/ERROR] Chat %d, Msg %d: Документ найден, но не модифицирован. Возможно, вектор уже установлен или проблема с обновлением. MatchedCount=%d, ModifiedCount=0", chatID, messageID, result.MatchedCount)
		return fmt.Errorf("документ найден, но не модифицирован (vector уже может существовать)")
	}

	if ms.debug {
		log.Printf("[UpdateEmbedding OK] Chat %d, Msg %d: Эмбеддинг успешно обновлен. Matched: %d, Modified: %d", chatID, messageID, result.MatchedCount, result.ModifiedCount)
	}

	return nil // Успешное обновление
}
