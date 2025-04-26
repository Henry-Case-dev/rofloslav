package storage

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	// Нужен для debug

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// === Методы для истории сообщений ===

// AddMessage добавляет сообщение в MongoDB.
// Также генерирует и сохраняет эмбеддинг для текстового контента.
func (ms *MongoStorage) AddMessage(chatID int64, message *tgbotapi.Message) {
	if message == nil {
		log.Printf("[Mongo AddMessage WARN] Попытка добавить nil сообщение в чат %d", chatID)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second) // Увеличим таймаут из-за эмбеддингов
	defer cancel()

	// Конвертируем в структуру для MongoDB
	mongoMsg := convertAPIToMongoMessage(chatID, message)

	// --- Генерация эмбеддинга ---
	var textToEmbed string
	if mongoMsg.Text != "" {
		textToEmbed = mongoMsg.Text
	} else if mongoMsg.Caption != "" {
		textToEmbed = mongoMsg.Caption
	}

	if textToEmbed != "" {
		if ms.debug {
			log.Printf("[Mongo AddMessage DEBUG] Чат %d: Попытка генерации эмбеддинга для сообщения ID %d...", chatID, mongoMsg.MessageID)
		}
		// Используем КЛИЕНТ ДЛЯ ЭМБЕДДИНГОВ (Gemini)!
		vector, err := ms.embeddingClient.EmbedContent(textToEmbed)
		if err != nil {
			log.Printf("[Mongo AddMessage ERROR] Чат %d: Ошибка генерации эмбеддинга (Gemini) для сообщения ID %d: %v", chatID, mongoMsg.MessageID, err)
			// Не прерываем добавление сообщения, просто вектор будет пустым
		} else {
			mongoMsg.MessageVector = vector
			if ms.debug {
				log.Printf("[Mongo AddMessage DEBUG] Чат %d: Эмбеддинг для сообщения ID %d успешно сгенерирован.", chatID, mongoMsg.MessageID)
			}
		}
	}
	// --- Конец генерации эмбеддинга ---

	// Получаем *правильную* коллекцию для этого чата
	coll := ms.getMessagesCollection(chatID)

	// Используем coll вместо ms.messagesCollection
	filter := bson.M{"chat_id": chatID, "message_id": message.MessageID}
	update := bson.M{"$set": mongoMsg} // Перезаписываем весь документ, если он существует
	ops := options.Update().SetUpsert(true)

	result, err := coll.UpdateOne(ctx, filter, update, ops)
	if err != nil {
		log.Printf("[Mongo AddMessage ERROR] Чат %d: Ошибка сохранения сообщения ID %d в коллекцию '%s': %v", chatID, message.MessageID, coll.Name(), err)
		return
	}

	if ms.debug {
		if result.UpsertedCount > 0 {
			log.Printf("[Mongo AddMessage DEBUG] Чат %d: Сообщение ID %d успешно вставлено в MongoDB с ID: %v.", chatID, message.MessageID, result.UpsertedID)
		} else if result.ModifiedCount > 0 {
			log.Printf("[Mongo AddMessage DEBUG] Чат %d: Сообщение ID %d успешно обновлено в MongoDB.", chatID, message.MessageID)
		} else {
			// Это может произойти, если сообщение идентично существующему
			log.Printf("[Mongo AddMessage DEBUG] Чат %d: Сообщение ID %d не было изменено в MongoDB (возможно, идентично).", chatID, message.MessageID)
		}
	}
}

// convertAPIToMongoMessage конвертирует *tgbotapi.Message в *MongoMessage.
func convertAPIToMongoMessage(chatID int64, apiMsg *tgbotapi.Message) *MongoMessage {
	if apiMsg == nil {
		return nil
	}
	m := &MongoMessage{
		ChatID:           chatID,
		MessageID:        apiMsg.MessageID,
		Date:             time.Unix(int64(apiMsg.Date), 0),
		Text:             apiMsg.Text,
		ReplyToMessageID: 0, // Инициализируем нулем
		Entities:         apiMsg.Entities,
		Caption:          apiMsg.Caption,
		CaptionEntities:  apiMsg.CaptionEntities,
		HasMedia:         apiMsg.Photo != nil || apiMsg.Video != nil || apiMsg.Audio != nil || apiMsg.Document != nil || apiMsg.Sticker != nil,
		IsVoice:          apiMsg.Voice != nil,
	}
	if apiMsg.From != nil {
		m.UserID = apiMsg.From.ID
		m.Username = apiMsg.From.UserName
		m.FirstName = apiMsg.From.FirstName
		m.LastName = apiMsg.From.LastName
		m.IsBot = apiMsg.From.IsBot
	}
	if apiMsg.ReplyToMessage != nil {
		m.ReplyToMessageID = apiMsg.ReplyToMessage.MessageID
	}

	// --- Заполняем поля пересылки ---
	if apiMsg.ForwardDate > 0 {
		m.IsForward = true
		m.ForwardedDate = time.Unix(int64(apiMsg.ForwardDate), 0)
		m.ForwardedFromMessageID = apiMsg.ForwardFromMessageID // ID сообщения в исходном чате

		if apiMsg.ForwardFrom != nil { // Переслано от пользователя
			m.ForwardedFromUserID = apiMsg.ForwardFrom.ID
		} else if apiMsg.ForwardFromChat != nil { // Переслано из чата/канала
			m.ForwardedFromChatID = apiMsg.ForwardFromChat.ID
		}
		// ForwardSenderName не сохраняем
	}

	return m
}

// GetMessages извлекает последние N сообщений для указанного чата из MongoDB.
func (ms *MongoStorage) GetMessages(chatID int64, limit int) ([]*tgbotapi.Message, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Получаем *правильную* коллекцию для этого чата
	coll := ms.getMessagesCollection(chatID)

	filter := bson.M{"chat_id": chatID}
	// Сортировка по дате в обратном порядке
	findOptions := options.Find().SetSort(bson.D{{Key: "date", Value: -1}}).SetLimit(int64(limit))

	// --- Добавляем проекцию для исключения message_vector ---
	projection := bson.D{{"message_vector", 0}}
	findOptions.SetProjection(projection)
	// --- Конец добавления проекции ---

	// Используем coll вместо ms.messagesCollection
	cursor, err := coll.Find(ctx, filter, findOptions)
	if err != nil {
		log.Printf("[GetMessages ERROR] Чат %d: Ошибка получения сообщений из MongoDB (коллекция '%s'): %v", chatID, coll.Name(), err)
		return nil, fmt.Errorf("ошибка получения сообщений: %w", err)
	}
	defer cursor.Close(ctx)

	var mongoMessages []*MongoMessage
	if err = cursor.All(ctx, &mongoMessages); err != nil {
		log.Printf("[GetMessages ERROR] Чат %d: Ошибка декодирования сообщений из MongoDB: %v", chatID, err)
		return nil, fmt.Errorf("ошибка декодирования сообщений: %w", err)
	}

	// Конвертируем обратно в формат tgbotapi.Message
	apiMessages := make([]*tgbotapi.Message, 0, len(mongoMessages))
	for _, mongoMsg := range mongoMessages {
		apiMessages = append(apiMessages, convertMongoToAPIMessage(mongoMsg))
	}

	// Так как мы получали в обратном порядке (новые сначала), нужно развернуть слайс
	// для правильного хронологического порядка
	sort.SliceStable(apiMessages, func(i, j int) bool {
		return apiMessages[i].Date < apiMessages[j].Date
	})

	if ms.debug {
		log.Printf("[GetMessages DEBUG] Чат %d: Успешно получено %d сообщений из MongoDB.", chatID, len(apiMessages))
	}
	return apiMessages, nil
}

// GetMessagesSince извлекает сообщения из MongoDB, начиная с указанного времени,
// для конкретного пользователя и с ограничением по количеству.
// Возвращает сообщения в хронологическом порядке (старые -> новые).
func (ms *MongoStorage) GetMessagesSince(ctx context.Context, chatID int64, userID int64, since time.Time, limit int) ([]*tgbotapi.Message, error) {
	// Получаем *правильную* коллекцию для этого чата
	coll := ms.getMessagesCollection(chatID)

	filter := bson.M{
		"chat_id": chatID,
		"date":    bson.M{"$gte": since},
	}

	// Добавляем фильтр по userID только если он не равен 0
	if userID != 0 {
		filter["user_id"] = userID
	}

	// Сортируем по дате в ОБРАТНОМ порядке (новые сначала), чтобы применить лимит к последним сообщениям
	findOptions := options.Find().SetSort(bson.D{{Key: "date", Value: -1}})

	// Применяем лимит, если он > 0
	if limit > 0 {
		findOptions.SetLimit(int64(limit))
	}

	// --- Добавляем проекцию для исключения message_vector ---
	projection := bson.D{{"message_vector", 0}}
	findOptions.SetProjection(projection)
	// --- Конец добавления проекции ---

	cursor, err := coll.Find(ctx, filter, findOptions)
	if err != nil {
		log.Printf("[GetMessagesSince ERROR] Чат %d, User %d: Ошибка получения сообщений (since %v, limit %d) из MongoDB: %v", chatID, userID, since, limit, err)
		return nil, fmt.Errorf("ошибка получения сообщений пользователя: %w", err)
	}
	defer cursor.Close(ctx)

	var mongoMessages []*MongoMessage
	if err = cursor.All(ctx, &mongoMessages); err != nil {
		log.Printf("[GetMessagesSince ERROR] Чат %d, User %d: Ошибка декодирования сообщений (since %v, limit %d) из MongoDB: %v", chatID, userID, since, limit, err)
		return nil, fmt.Errorf("ошибка декодирования сообщений: %w", err)
	}

	// Конвертируем обратно в формат tgbotapi.Message
	apiMessages := make([]*tgbotapi.Message, 0, len(mongoMessages))
	for _, mongoMsg := range mongoMessages {
		apiMessages = append(apiMessages, convertMongoToAPIMessage(mongoMsg))
	}

	// Так как мы получали в обратном порядке (новые сначала),
	// нужно развернуть слайс для возврата в хронологическом порядке (старые -> новые).
	sort.SliceStable(apiMessages, func(i, j int) bool {
		return apiMessages[i].Date < apiMessages[j].Date
	})

	if ms.debug {
		log.Printf("[GetMessagesSince DEBUG] Чат %d, User %d: Успешно получено %d сообщений (since %v, limit %d) из MongoDB.", chatID, userID, len(apiMessages), since, limit)
	}
	return apiMessages, nil
}

// LoadChatHistory - Заглушка для MongoDB, история всегда загружена.
func (ms *MongoStorage) LoadChatHistory(chatID int64) ([]*tgbotapi.Message, error) {
	if ms.debug {
		log.Printf("[LoadChatHistory DEBUG] Чат %d: Вызов LoadChatHistory для MongoDB (операция не требуется).", chatID)
	}
	// Просто возвращаем текущие сообщения из БД, ограниченные окном контекста
	return ms.GetMessages(chatID, ms.cfg.ContextWindow)
}

// SaveChatHistory - Заглушка для MongoDB, сохранение происходит при добавлении.
func (ms *MongoStorage) SaveChatHistory(chatID int64) error {
	if ms.debug {
		log.Printf("[SaveChatHistory DEBUG] Чат %d: Вызов SaveChatHistory для MongoDB (операция не требуется).", chatID)
	}
	return nil // Нет необходимости в явном сохранении для MongoDB
}

// ClearChatHistory удаляет все сообщения для указанного чата из MongoDB.
// ОСТОРОЖНО: Эта операция необратима!
func (ms *MongoStorage) ClearChatHistory(chatID int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Получаем *правильную* коллекцию для этого чата
	coll := ms.getMessagesCollection(chatID)

	// Удаляем все документы из коллекции для данного chatID
	// ВАЖНО: Теперь просто удаляем все из коллекции coll, т.к. она уже специфична для чата
	_, err := coll.DeleteMany(ctx, bson.M{})
	if err != nil {
		log.Printf("[ClearChatHistory ERROR] Чат %d: Ошибка очистки истории в MongoDB (коллекция '%s'): %v", chatID, coll.Name(), err)
		return fmt.Errorf("ошибка очистки истории: %w", err)
	}

	if ms.debug {
		log.Printf("[ClearChatHistory DEBUG] Чат %d: История в MongoDB (коллекция '%s') успешно очищена.", chatID, coll.Name())
	}
	return nil
}

// AddMessagesToContext - Заглушка для MongoDB, контекст управляется через GetMessages.
func (ms *MongoStorage) AddMessagesToContext(chatID int64, messages []*tgbotapi.Message) {
	if ms.debug {
		log.Printf("[AddMessagesToContext DEBUG] Чат %d: Вызов AddMessagesToContext для MongoDB (операция не требуется). Сообщений передано: %d", chatID, len(messages))
	}
	// Для MongoDB нет необходимости явно добавлять сообщения в контекст в памяти,
	// так как GetMessages всегда запрашивает актуальные данные из БД.
	// Можно добавить логирование для отладки, если нужно.
}

// GetAllChatIDs возвращает список всех уникальных chatID, для которых существуют коллекции сообщений.
// Для MongoDB это реализовано через перечисление коллекций с префиксом.
func (ms *MongoStorage) GetAllChatIDs() ([]int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	collectionPrefix := "chat_messages_"
	filter := bson.M{"name": bson.M{"$regex": "^" + collectionPrefix}}

	cursor, err := ms.database.ListCollections(ctx, filter)
	if err != nil {
		log.Printf("[GetAllChatIDs ERROR] Ошибка получения списка коллекций: %v", err)
		return nil, fmt.Errorf("ошибка получения списка коллекций: %w", err)
	}
	defer cursor.Close(ctx)

	var chatIDs []int64
	seenIDs := make(map[int64]bool) // Для дедупликации на всякий случай

	for cursor.Next(ctx) {
		var result struct {
			Name string `bson:"name"`
		}
		if err := cursor.Decode(&result); err != nil {
			log.Printf("[GetAllChatIDs WARN] Ошибка декодирования имени коллекции: %v", err)
			continue // Пропускаем эту коллекцию
		}

		if strings.HasPrefix(result.Name, collectionPrefix) {
			chatIDStr := strings.TrimPrefix(result.Name, collectionPrefix)
			chatID, err := strconv.ParseInt(chatIDStr, 10, 64)
			if err != nil {
				log.Printf("[GetAllChatIDs WARN] Не удалось распарсить chatID из имени коллекции '%s': %v", result.Name, err)
				continue // Пропускаем
			}

			if !seenIDs[chatID] {
				chatIDs = append(chatIDs, chatID)
				seenIDs[chatID] = true
			}
		}
	}

	if err := cursor.Err(); err != nil {
		log.Printf("[GetAllChatIDs ERROR] Ошибка итерации по курсору коллекций: %v", err)
		// Возвращаем то, что успели собрать, и ошибку
		return chatIDs, fmt.Errorf("ошибка итерации по курсору коллекций: %w", err)
	}

	if ms.debug {
		log.Printf("[GetAllChatIDs DEBUG] Успешно получено %d уникальных chat_id из имен коллекций MongoDB.", len(chatIDs))
	}

	return chatIDs, nil
}

// convertMongoToAPIMessage конвертирует *MongoMessage в *tgbotapi.Message.
func convertMongoToAPIMessage(mongoMsg *MongoMessage) *tgbotapi.Message {
	if mongoMsg == nil {
		return nil
	}

	apiMsg := &tgbotapi.Message{
		MessageID:       mongoMsg.MessageID,
		Date:            int(mongoMsg.Date.Unix()),           // Конвертируем time.Time обратно в Unix timestamp
		Chat:            &tgbotapi.Chat{ID: mongoMsg.ChatID}, // Создаем базовый объект Chat
		From:            nil,                                 // Инициализируем From как nil
		Text:            mongoMsg.Text,
		Entities:        mongoMsg.Entities,
		Caption:         mongoMsg.Caption,
		CaptionEntities: mongoMsg.CaptionEntities,
		Voice:           nil, // Инициализируем nil, т.к. Voice данные не храним
		Photo:           nil, // И т.д. для других медиа
		ReplyToMessage:  nil,
		// --- Восстанавливаем информацию о пересылке ---
		ForwardDate: 0, // Инициализируем 0
	}

	// Восстанавливаем From, если есть UserID
	if mongoMsg.UserID != 0 {
		apiMsg.From = &tgbotapi.User{
			ID:        mongoMsg.UserID,
			IsBot:     mongoMsg.IsBot,
			FirstName: mongoMsg.FirstName,
			LastName:  mongoMsg.LastName,
			UserName:  mongoMsg.Username,
		}
	}

	// Восстанавливаем ReplyToMessage, если есть ID
	if mongoMsg.ReplyToMessageID != 0 {
		apiMsg.ReplyToMessage = &tgbotapi.Message{MessageID: mongoMsg.ReplyToMessageID}
	}

	// Восстанавливаем Voice, если это было голосовое сообщение
	if mongoMsg.IsVoice {
		// Мы не храним сам файл, поэтому создаем пустой объект Voice,
		// чтобы вызывающий код знал, что это было голосовое сообщение.
		apiMsg.Voice = &tgbotapi.Voice{}
	}

	// Восстанавливаем данные о пересылке
	if mongoMsg.IsForward {
		apiMsg.ForwardDate = int(mongoMsg.ForwardedDate.Unix())
		apiMsg.ForwardFromMessageID = mongoMsg.ForwardedFromMessageID
		if mongoMsg.ForwardedFromUserID != 0 {
			apiMsg.ForwardFrom = &tgbotapi.User{ID: mongoMsg.ForwardedFromUserID}
		} else if mongoMsg.ForwardedFromChatID != 0 {
			apiMsg.ForwardFromChat = &tgbotapi.Chat{ID: mongoMsg.ForwardedFromChatID}
		}
	}

	return apiMsg
}

// GetReplyChain извлекает цепочку сообщений, на которые отвечали, начиная с messageID,
// двигаясь вверх по цепочке до maxDepth или до сообщения без ReplyToMessageID.
// Возвращает сообщения в хронологическом порядке (старые -> новые).
func (ms *MongoStorage) GetReplyChain(ctx context.Context, chatID int64, startMessageID int, maxDepth int) ([]*tgbotapi.Message, error) {
	// Получаем *правильную* коллекцию для этого чата
	coll := ms.getMessagesCollection(chatID)

	replyChainMongo := make([]*MongoMessage, 0)
	currentMessageID := startMessageID
	currentDepth := 0

	for currentMessageID != 0 && currentDepth < maxDepth {
		// Создаем новый контекст для каждого запроса с таймаутом
		findCtx, cancel := context.WithTimeout(ctx, 5*time.Second)

		var msg MongoMessage
		filter := bson.M{"chat_id": chatID, "message_id": currentMessageID}
		err := coll.FindOne(findCtx, filter).Decode(&msg)

		cancel() // Освобождаем ресурсы контекста запроса

		if err != nil {
			if errors.Is(err, mongo.ErrNoDocuments) {
				// Сообщение не найдено, возможно, оно было удалено или не попало в БД
				log.Printf("[GetReplyChain WARN] Chat %d: Сообщение с ID %d (часть цепочки) не найдено.", chatID, currentMessageID)
				break // Прерываем цепочку, если сообщение не найдено
			} else if errors.Is(err, context.DeadlineExceeded) {
				log.Printf("[GetReplyChain ERROR] Chat %d: Таймаут при поиске сообщения ID %d: %v", chatID, currentMessageID, err)
				return nil, fmt.Errorf("таймаут при поиске сообщения в цепочке (ID: %d): %w", currentMessageID, err)
			}
			// Другая ошибка БД
			log.Printf("[GetReplyChain ERROR] Chat %d: Ошибка поиска сообщения ID %d: %v", chatID, currentMessageID, err)
			return nil, fmt.Errorf("ошибка поиска сообщения в цепочке (ID: %d): %w", currentMessageID, err)
		}

		// Добавляем найденное сообщение в начало среза (чтобы потом легко развернуть)
		replyChainMongo = append([]*MongoMessage{&msg}, replyChainMongo...)

		// Переходим к следующему сообщению в цепочке
		currentMessageID = msg.ReplyToMessageID
		currentDepth++
	}

	// Конвертируем MongoMessage в tgbotapi.Message
	replyChainAPI := make([]*tgbotapi.Message, 0, len(replyChainMongo))
	for _, mongoMsg := range replyChainMongo {
		apiMsg := convertMongoToAPIMessage(mongoMsg)
		if apiMsg != nil {
			replyChainAPI = append(replyChainAPI, apiMsg)
		}
	}

	if ms.debug {
		ids := make([]int, len(replyChainAPI))
		for i, m := range replyChainAPI {
			ids[i] = m.MessageID
		}
		log.Printf("[GetReplyChain DEBUG] Chat %d: Найдена цепочка ответов для ID %d (глубина %d): %v", chatID, startMessageID, len(replyChainAPI), ids)
	}

	return replyChainAPI, nil
}
