package storage

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/Henry-Case-dev/rofloslav/internal/config"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// CleanupOldMessagesForChat проверяет размер коллекции для конкретного чата и, если он превышает заданный лимит,
// находит самое старое сообщение, вычисляет порог (oldestMessage.Date + MONGO_CLEANUP_CHUNK_DURATION_HOURS)
// и удаляет все сообщения с датой меньше порога.
func (ms *MongoStorage) CleanupOldMessagesForChat(chatID int64, cfg *config.Config) error {
	coll := ms.getMessagesCollection(chatID)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	statsCmd := bson.D{{"collStats", coll.Name()}}
	var statsResult bson.M
	if err := ms.database.RunCommand(ctx, statsCmd).Decode(&statsResult); err != nil {
		log.Printf("[Cleanup ERROR] Чат %d: Ошибка получения статистики коллекции %s: %v", chatID, coll.Name(), err)
		return fmt.Errorf("ошибка получения статистики коллекции: %w", err)
	}

	// Пытаемся получить размер как целое число (int64 или int32), что более вероятно
	var sizeBytes int64
	var okSize bool
	sizeRaw, sizeExists := statsResult["size"]
	if sizeExists {
		switch v := sizeRaw.(type) {
		case int32:
			sizeBytes = int64(v)
			okSize = true
		case int64:
			sizeBytes = v
			okSize = true
		case float64: // Добавим проверку float64 на всякий случай
			sizeBytes = int64(v)
			okSize = true
		default:
			log.Printf("[Cleanup WARN] Чат %d: Неожиданный тип для 'size': %T", chatID, v)
		}
	}

	// Аналогично для storageSize
	var storageSizeBytes int64
	storageSizeRaw, storageSizeExists := statsResult["storageSize"]
	if storageSizeExists {
		switch v := storageSizeRaw.(type) {
		case int32:
			storageSizeBytes = int64(v)
		case int64:
			storageSizeBytes = v
		case float64:
			storageSizeBytes = int64(v)
		}
	}

	// Аналогично для totalIndexSize
	var totalIndexSizeBytes int64
	totalIndexSizeRaw, totalIndexSizeExists := statsResult["totalIndexSize"]
	if totalIndexSizeExists {
		switch v := totalIndexSizeRaw.(type) {
		case int32:
			totalIndexSizeBytes = int64(v)
		case int64:
			totalIndexSizeBytes = v
		case float64:
			totalIndexSizeBytes = int64(v)
		}
	}

	if !okSize {
		log.Printf("[Cleanup ERROR] Чат %d: Не удалось получить или распознать 'size' из статистики коллекции %s. Результат collStats: %+v", chatID, coll.Name(), statsResult)
		return fmt.Errorf("не удалось получить 'size' из статистики коллекции")
	}

	limitBytes := int64(cfg.MongoCleanupSizeLimitMB) * 1024 * 1024

	if cfg.Debug {
		log.Printf("[Cleanup DEBUG] Чат %d (%s): Проверка лимита. \n\t\t  Размер (collStats.size, uncompressed in-mem): %.2f MB (%d bytes). \n\t\t  Размер на диске (storageSize): %.2f MB (%d bytes). \n\t\t  Размер индексов (totalIndexSize): %.2f MB (%d bytes). \n\t\t  Лимит (MONGO_CLEANUP_SIZE_LIMIT_MB): %d MB (%d bytes).",
			chatID, coll.Name(),
			float64(sizeBytes)/(1024*1024),
			sizeBytes,
			float64(storageSizeBytes)/(1024*1024),
			storageSizeBytes,
			float64(totalIndexSizeBytes)/(1024*1024),
			totalIndexSizeBytes,
			cfg.MongoCleanupSizeLimitMB,
			limitBytes,
		)
	}

	if sizeBytes <= limitBytes {
		if cfg.Debug {
			log.Printf("[Cleanup DEBUG] Чат %d: Текущий размер 'size' (%d bytes) НЕ превышает лимит (%d bytes). Очистка не требуется.", chatID, sizeBytes, limitBytes)
		}
		return nil
	}

	log.Printf("[Cleanup INFO] Чат %d: Текущий размер 'size' (%d bytes) ПРЕВЫШАЕТ лимит (%d bytes). Запуск очистки...", chatID, sizeBytes, limitBytes)

	var oldestMsg struct {
		Date time.Time `bson:"date"`
	}
	findOpts := options.FindOne().SetSort(bson.D{{"date", 1}})
	if err := coll.FindOne(ctx, bson.M{}, findOpts).Decode(&oldestMsg); err != nil {
		log.Printf("[Cleanup ERROR] Чат %d: Ошибка поиска самого старого сообщения в коллекции %s: %v", chatID, coll.Name(), err)
		return fmt.Errorf("ошибка поиска самого старого сообщения: %w", err)
	}

	threshold := oldestMsg.Date.Add(time.Duration(cfg.MongoCleanupChunkDurationHours) * time.Hour)
	delFilter := bson.M{"date": bson.M{"$lt": threshold}}
	delResult, err := coll.DeleteMany(ctx, delFilter)
	if err != nil {
		log.Printf("[Cleanup ERROR] Чат %d: Ошибка удаления сообщений в коллекции %s: %v", chatID, coll.Name(), err)
		return fmt.Errorf("ошибка удаления сообщений: %w", err)
	}
	log.Printf("[Cleanup INFO] Чат %d: В коллекции %s удалено %d сообщений старше %v", chatID, coll.Name(), delResult.DeletedCount, threshold)
	return nil
}
