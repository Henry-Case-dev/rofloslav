package storage

import (
	"context"
	"fmt"
	"log"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

// Убедимся, что MongoStorage реализует интерфейс ChatHistoryStorage.
var _ ChatHistoryStorage = (*MongoStorage)(nil)

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

	return msg
}

// MongoStorage реализует ChatHistoryStorage с использованием MongoDB.
type MongoStorage struct {
	client        *mongo.Client
	database      *mongo.Database
	collection    *mongo.Collection
	contextWindow int
	debug         bool
}

// NewMongoStorage создает новое хранилище MongoDB.
func NewMongoStorage(mongoURI, dbName, collectionName string, contextWindow int, debug bool) (*MongoStorage, error) {
	// Увеличиваем таймаут подключения
	ctxConnect, cancelConnect := context.WithTimeout(context.Background(), 30*time.Second) // Было 10*time.Second
	defer cancelConnect()

	clientOptions := options.Client().ApplyURI(mongoURI)
	client, err := mongo.Connect(ctxConnect, clientOptions)
	if err != nil {
		return nil, fmt.Errorf("ошибка подключения к MongoDB: %w", err)
	}

	// Проверяем соединение с увеличенным таймаутом
	ctxPing, cancelPing := context.WithTimeout(context.Background(), 30*time.Second) // Было 10*time.Second
	defer cancelPing()
	err = client.Ping(ctxPing, readpref.Primary())
	if err != nil {
		// Закрываем клиент, если пинг не прошел
		_ = client.Disconnect(context.Background()) // Используем новый контекст для Disconnect
		return nil, fmt.Errorf("ошибка проверки соединения с MongoDB (Ping): %w", err)
	}

	log.Println("Успешное подключение к MongoDB.")

	db := client.Database(dbName)
	collection := db.Collection(collectionName)

	storage := &MongoStorage{
		client:        client,
		database:      db,
		collection:    collection,
		contextWindow: contextWindow,
		debug:         debug,
	}

	// Создаем индексы (можно добавить обработку ошибок, если критично)
	err = storage.ensureIndexes() // Вызываем синхронно
	if err != nil {
		// Логируем ошибку, но не считаем фатальной для старта
		log.Printf("[WARN] Ошибка при создании индекса MongoDB при инициализации: %v", err)
	}

	return storage, nil
}

// ensureIndexes создает необходимые индексы в коллекции MongoDB.
// Возвращает ошибку, если создание не удалось.
func (ms *MongoStorage) ensureIndexes() error { // Изменяем сигнатуру для возврата ошибки
	// Индекс по chat_id и date для быстрой выборки сообщений чата в правильном порядке
	indexModel := mongo.IndexModel{
		Keys: bson.D{
			{"chat_id", 1}, // 1 для сортировки по возрастанию
			{"date", -1},   // -1 для сортировки по убыванию (новые первыми)
		},
	}

	// Увеличим таймаут для создания индекса, если коллекция большая
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Проверяем, что коллекция не nil
	if ms.collection == nil {
		return fmt.Errorf("коллекция MongoDB не инициализирована перед созданием индекса")
	}

	indexName, err := ms.collection.Indexes().CreateOne(ctx, indexModel)
	if err != nil {
		// Возвращаем ошибку для логирования в вызывающей функции
		return fmt.Errorf("не удалось создать индекс MongoDB (chat_id, date): %w", err)
	}

	log.Printf("Индекс MongoDB '%s' (chat_id, date) успешно создан или уже существует.", indexName)
	return nil // Успех
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
	if message == nil {
		log.Printf("[Mongo AddMessage WARN] Чат %d: Попытка добавить nil сообщение.", chatID)
		return
	}

	if ms.debug {
		log.Printf("[Mongo AddMessage DEBUG] Чат %d: Попытка добавления сообщения ID %d.", chatID, message.MessageID)
	}

	// Конвертируем сообщение в формат MongoDB
	mongoMsg := convertAPIToMongoMessage(chatID, message)
	if mongoMsg == nil { // Проверка после конвертации
		log.Printf("[Mongo AddMessage WARN] Чат %d: Сообщение ID %d не удалось конвертировать в MongoMessage.", chatID, message.MessageID)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second) // Короткий таймаут для вставки
	defer cancel()

	// Вставляем одно сообщение
	insertResult, err := ms.collection.InsertOne(ctx, mongoMsg)
	if err != nil {
		// Логируем ошибку более подробно
		log.Printf("[Mongo AddMessage ERROR] Чат %d: Ошибка вставки сообщения ID %d: %v", chatID, message.MessageID, err)
		// Попробуем вывести детали ошибки MongoDB, если они есть
		if mongoErr, ok := err.(mongo.WriteException); ok {
			for _, writeErr := range mongoErr.WriteErrors {
				log.Printf("[Mongo AddMessage ERROR Detail] Чат %d: MongoDB WriteError - Code: %d, Message: %s", chatID, writeErr.Code, writeErr.Message)
			}
		}
		return // Выходим при ошибке
	}

	if ms.debug {
		if insertResult != nil && insertResult.InsertedID != nil {
			log.Printf("[Mongo AddMessage DEBUG] Чат %d: Сообщение ID %d успешно вставлено в MongoDB с ID: %v.", chatID, message.MessageID, insertResult.InsertedID)
		} else {
			// Эта ветка маловероятна при отсутствии ошибки, но для полноты
			log.Printf("[Mongo AddMessage DEBUG] Чат %d: Сообщение ID %d вставлено, но InsertedID не получен (возможно, уже существовало?).", chatID, message.MessageID)
		}
	}

	// Обрезка старых сообщений (если нужно убрать лишнее, оставляем только N последних)
	// Это ресурсоемкая операция, выполняем ее не на каждое сообщение, а реже или вообще убираем,
	// полагаясь на TTL индекс или GetMessages с Limit.
	// Пока закомментируем, чтобы не замедлять вставку.
	/*
		if ms.contextWindow > 0 {
			go ms.trimOldMessages(chatID) // Запускаем в фоне
		}
	*/
}

// convertAPIToMongoMessage преобразует *tgbotapi.Message в *MongoMessage.
// Вынесено в отдельную функцию для ясности.
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
		mongoMsg.FirstName = apiMsg.From.FirstName
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
	if len(apiMsg.Photo) > 0 || apiMsg.Video != nil || apiMsg.Voice != nil || apiMsg.Document != nil || apiMsg.Sticker != nil || apiMsg.Audio != nil {
		mongoMsg.HasMedia = true
	}

	return mongoMsg
}

// GetMessages возвращает последние сообщения из MongoDB для указанного chatID
func (ms *MongoStorage) GetMessages(chatID int64) []*tgbotapi.Message {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	filter := bson.M{"chat_id": chatID}
	findOptions := options.Find().
		SetSort(bson.D{{"date", -1}}). // Сортируем по дате, новые первыми
		SetLimit(int64(ms.contextWindow))

	cursor, err := ms.collection.Find(ctx, filter, findOptions)
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

	cursor, err := ms.collection.Find(ctx, filter, findOptions)
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

	_, err := ms.collection.InsertMany(ctx, mongoMessages)
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
