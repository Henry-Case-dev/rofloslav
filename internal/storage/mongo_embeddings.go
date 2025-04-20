package storage

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/Henry-Case-dev/rofloslav/internal/config"
	"github.com/Henry-Case-dev/rofloslav/internal/llm"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// === Методы для работы с эмбеддингами и долгосрочной памятью ===

// SearchRelevantMessages ищет сообщения, семантически близкие к queryText,
// используя векторный поиск в MongoDB Atlas.
// Возвращает до k наиболее релевантных сообщений.
func SearchRelevantMessages(ctx context.Context, cfg *config.Config, messagesCollection *mongo.Collection, embeddingClient llm.LLMClient, chatID int64, queryText string, k int, debug bool) ([]*tgbotapi.Message, error) {
	if !cfg.LongTermMemoryEnabled {
		if debug {
			log.Printf("[SearchRelevantMessages DEBUG] Chat %d: Долгосрочная память отключена.", chatID)
		}
		return nil, nil // Не ошибка, просто ничего не ищем
	}
	if k <= 0 {
		return nil, nil // Нечего искать
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second) // Таймаут для векторного поиска
	defer cancel()

	// 1. Генерируем вектор для поискового запроса
	queryVector, err := embeddingClient.EmbedContent(queryText)
	if err != nil {
		log.Printf("[SearchRelevantMessages ERROR] Chat %d: Ошибка генерации эмбеддинга для запроса: %v", chatID, err)
		return nil, fmt.Errorf("ошибка генерации эмбеддинга запроса: %w", err)
	}

	// 2. Формируем пайплайн агрегации для векторного поиска
	// Используем $vectorSearch оператор MongoDB Atlas
	pipeline := mongo.Pipeline{
		bson.D{ // Стадия $vectorSearch
			{Key: "$vectorSearch", Value: bson.D{
				{Key: "index", Value: cfg.MongoVectorIndexName},
				{Key: "queryVector", Value: queryVector},
				{Key: "path", Value: "message_vector"},
				{Key: "numCandidates", Value: int64(k * 10)}, // Увеличиваем количество кандидатов и приводим к int64
				{Key: "limit", Value: int64(k)},              // Приводим k к int64
				// Фильтр по chat_id ДО векторного поиска для эффективности
				{Key: "filter", Value: bson.D{
					{Key: "chat_id", Value: chatID},
				}},
			}},
		},
		bson.D{ // Стадия $project
			{Key: "$project", Value: bson.D{
				{Key: "_id", Value: 1},
				{Key: "chat_id", Value: 1},
				{Key: "message_id", Value: 1},
				{Key: "user_id", Value: 1},
				{Key: "username", Value: 1},
				{Key: "first_name", Value: 1},
				{Key: "last_name", Value: 1},
				{Key: "is_bot", Value: 1},
				{Key: "date", Value: 1},
				{Key: "text", Value: 1},
				{Key: "reply_to_message_id", Value: 1},
				{Key: "entities", Value: 1},
				{Key: "caption", Value: 1},
				{Key: "caption_entities", Value: 1},
				{Key: "has_media", Value: 1},
				{Key: "is_voice", Value: 1},
				// Добавляем поля пересылки в проекцию
				{Key: "is_forward", Value: 1},
				{Key: "forwarded_from_user_id", Value: 1},
				{Key: "forwarded_from_chat_id", Value: 1},
				{Key: "forwarded_from_message_id", Value: 1},
				{Key: "forwarded_date", Value: 1},
				// Вычисляем score
				{Key: "score", Value: bson.D{{Key: "$meta", Value: "vectorSearchScore"}}},
			}},
		},
	}

	if debug {
		log.Printf("[SearchRelevantMessages DEBUG] Chat %d: Выполняю векторный поиск для запроса: \"%s...\"", chatID, queryText[:min(50, len(queryText))])
		// Можно логировать сам пайплайн, если нужно
		// log.Printf("[SearchRelevantMessages DEBUG] Pipeline: %+v", pipeline)
	}

	// 3. Выполняем агрегацию
	cursor, err := messagesCollection.Aggregate(ctx, pipeline)
	if err != nil {
		log.Printf("[SearchRelevantMessages ERROR] Chat %d: Ошибка выполнения векторного поиска: %v", chatID, err)
		return nil, fmt.Errorf("ошибка выполнения векторного поиска: %w", err)
	}
	defer cursor.Close(ctx)

	// 4. Обрабатываем результаты
	// Определяем структуру для декодирования результата агрегации
	type AggregateResult struct {
		MongoMessage `bson:",inline"` // Встраиваем поля MongoMessage
		Score        float64          `bson:"score"`
	}

	var results []*tgbotapi.Message
	for cursor.Next(ctx) {
		var result AggregateResult
		if err := cursor.Decode(&result); err != nil {
			log.Printf("[SearchRelevantMessages WARN] Chat %d: Ошибка декодирования результата векторного поиска: %v", chatID, err)
			continue
		}
		if debug {
			log.Printf("[SearchRelevantMessages DEBUG] Chat %d: Найдено сообщение ID %d с релевантностью %.4f", chatID, result.MessageID, result.Score)
		}
		// Конвертируем найденное сообщение в формат API
		apiMsg := convertMongoToAPIMessage(&result.MongoMessage)
		results = append(results, apiMsg)
	}

	if err := cursor.Err(); err != nil {
		log.Printf("[SearchRelevantMessages ERROR] Chat %d: Ошибка курсора после векторного поиска: %v", chatID, err)
		// Продолжаем с тем, что успели декодировать
	}

	if debug {
		log.Printf("[SearchRelevantMessages DEBUG] Chat %d: Векторный поиск завершен. Найдено %d релевантных сообщений.", chatID, len(results))
	}

	// Векторный поиск обычно возвращает наиболее релевантные первыми.
	// Если нужен хронологический порядок, можно дополнительно отсортировать results по дате.
	// sort.SliceStable(results, func(i, j int) bool {
	// 	return results[i].Date < results[j].Date
	// })

	return results, nil
}

// GetTotalMessagesCount возвращает общее количество сообщений в чате из MongoDB.
func GetTotalMessagesCount(ctx context.Context, messagesCollection *mongo.Collection, chatID int64) (int64, error) {
	filter := bson.M{"chat_id": chatID}
	count, err := messagesCollection.CountDocuments(ctx, filter)
	if err != nil {
		log.Printf("[GetTotalMessagesCount ERROR] Chat %d: Ошибка подсчета сообщений: %v", chatID, err)
		return 0, fmt.Errorf("ошибка подсчета сообщений: %w", err)
	}
	return count, nil
}

// FindMessagesWithoutEmbedding ищет сообщения без эмбеддингов в MongoDB,
// исключая указанные ID.
func FindMessagesWithoutEmbedding(ctx context.Context, messagesCollection *mongo.Collection, chatID int64, limit int, skipMessageIDs []int) ([]MongoMessage, error) {
	// Фильтр: ищем сообщения в чате, где поле message_vector не существует или равно null,
	// и текст или подпись не пустые, и message_id не входит в skipMessageIDs.
	filter := bson.M{
		"chat_id": chatID,
		"$or": []bson.M{
			{"message_vector": bson.M{"exists": false}},
			{"message_vector": nil},
		},
		"$and": []bson.M{
			{"$or": []bson.M{
				{"text": bson.M{"exists": true, "$ne": ""}},
				{"caption": bson.M{"exists": true, "$ne": ""}},
			}},
		},
	}

	// Добавляем условие для пропуска ID, если список не пуст
	if len(skipMessageIDs) > 0 {
		filter["message_id"] = bson.M{"$nin": skipMessageIDs}
	}

	// Опции: сортируем по дате (старые сначала) и берем лимит
	findOptions := options.Find().SetSort(bson.D{{Key: "date", Value: 1}}).SetLimit(int64(limit))

	cursor, err := messagesCollection.Find(ctx, filter, findOptions)
	if err != nil {
		log.Printf("[FindMessagesWithoutEmbedding ERROR] Chat %d: Ошибка поиска сообщений: %v", chatID, err)
		return nil, fmt.Errorf("ошибка поиска сообщений без эмбеддинга: %w", err)
	}
	defer cursor.Close(ctx)

	var messages []MongoMessage
	if err = cursor.All(ctx, &messages); err != nil {
		log.Printf("[FindMessagesWithoutEmbedding ERROR] Chat %d: Ошибка декодирования сообщений: %v", chatID, err)
		return nil, fmt.Errorf("ошибка декодирования сообщений: %w", err)
	}

	return messages, nil
}

// UpdateMessageEmbedding обновляет или добавляет эмбеддинг для конкретного сообщения в MongoDB.
func UpdateMessageEmbedding(ctx context.Context, messagesCollection *mongo.Collection, chatID int64, messageID int, vector []float32, debug bool) error {
	filter := bson.M{"chat_id": chatID, "message_id": messageID}
	update := bson.M{"$set": bson.M{"message_vector": vector}}

	result, err := messagesCollection.UpdateOne(ctx, filter, update)
	if err != nil {
		log.Printf("[UpdateMessageEmbedding ERROR] Chat %d, Msg %d: Ошибка обновления эмбеддинга: %v", chatID, messageID, err)
		return fmt.Errorf("ошибка обновления эмбеддинга: %w", err)
	}

	if result.MatchedCount == 0 {
		log.Printf("[UpdateMessageEmbedding WARN] Chat %d, Msg %d: Сообщение не найдено для обновления эмбеддинга.", chatID, messageID)
		return errors.New("сообщение не найдено для обновления")
	}
	if result.ModifiedCount == 0 {
		// Это может означать, что вектор уже был таким же или документ был удален между поиском и обновлением
		log.Printf("[UpdateMessageEmbedding WARN] Chat %d, Msg %d: Документ найден, но не модифицирован при обновлении эмбеддинга.", chatID, messageID)
		// Возвращаем специальную ошибку, чтобы вызывающий код мог ее обработать
		return errors.New("документ не модифицирован")
	}

	if debug {
		log.Printf("[UpdateMessageEmbedding DEBUG] Chat %d, Msg %d: Эмбеддинг успешно обновлен.", chatID, messageID)
	}
	return nil // Успешное обновление
}

// Вспомогательная функция min
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
