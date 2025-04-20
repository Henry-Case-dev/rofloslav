package storage

import (
	"context"
	"errors"
	"fmt"
	"log"
	"reflect"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// === Методы для настроек чатов ===

// GetChatSettings возвращает настройки для указанного чата из MongoDB
func (ms *MongoStorage) GetChatSettings(chatID int64) (*ChatSettings, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var settings ChatSettings
	filter := bson.M{"chat_id": chatID}

	err := ms.settingsCollection.FindOne(ctx, filter).Decode(&settings)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			// Настройки не найдены, создаем и возвращаем дефолтные
			log.Printf("[GetChatSettings WARN] Chat %d: Настройки не найдены в MongoDB, создаю дефолтные.", chatID)
			return ms.ensureChatSettings(ctx, chatID) // ensureChatSettings вернет настройки с дефолтами
		}
		// Другая ошибка базы данных
		log.Printf("[GetChatSettings ERROR] Chat %d: Ошибка получения настроек из MongoDB: %v", chatID, err)
		return nil, fmt.Errorf("ошибка получения настроек из MongoDB: %w", err)
	}

	// Применяем дефолты, если какие-то поля nil
	// Это нужно, чтобы последующий код мог безопасно разыменовывать указатели
	modified := ms.applyDefaultsToSettings(&settings)
	if modified {
		// Если дефолты были применены, стоит ли сохранять их обратно в БД?
		// Пока не будем, чтобы не создавать лишнюю нагрузку.
		// Просто вернем настройки с примененными дефолтами.
		if ms.debug {
			log.Printf("[GetChatSettings DEBUG] Chat %d: Применены дефолтные значения к загруженным настройкам.", chatID)
		}
	}

	if ms.debug {
		log.Printf("[GetChatSettings DEBUG] Chat %d: Настройки успешно загружены из MongoDB.", chatID)
	}
	return &settings, nil
}

// applyDefaultsToSettings применяет значения по умолчанию из config к полям ChatSettings, которые равны nil.
// Возвращает true, если хотя бы одно поле было изменено.
func (ms *MongoStorage) applyDefaultsToSettings(settings *ChatSettings) bool {
	if settings == nil || ms.cfg == nil {
		return false // Нечего применять
	}

	modified := false

	if settings.VoiceTranscriptionEnabled == nil {
		settings.VoiceTranscriptionEnabled = new(bool)
		*settings.VoiceTranscriptionEnabled = ms.cfg.VoiceTranscriptionEnabledDefault
		modified = true
	}
	if settings.DirectReplyLimitEnabled == nil {
		settings.DirectReplyLimitEnabled = new(bool)
		*settings.DirectReplyLimitEnabled = ms.cfg.DirectReplyLimitEnabledDefault
		modified = true
	}
	if settings.DirectReplyLimitCount == nil {
		settings.DirectReplyLimitCount = new(int)
		*settings.DirectReplyLimitCount = ms.cfg.DirectReplyLimitCountDefault
		modified = true
	}
	if settings.DirectReplyLimitDuration == nil {
		durationMinutes := int(ms.cfg.DirectReplyLimitDurationDefault.Minutes())
		settings.DirectReplyLimitDuration = &durationMinutes
		modified = true
	}
	if settings.SrachAnalysisEnabled == nil {
		settings.SrachAnalysisEnabled = new(bool)
		*settings.SrachAnalysisEnabled = ms.cfg.SrachAnalysisEnabled
		modified = true
	}

	// Можно добавить дефолты и для других полей, если они появятся
	// Например:
	// if settings.Temperature == nil {
	// 	settings.Temperature = new(float64)
	// 	*settings.Temperature = ms.cfg.DefaultTemperature
	// 	modified = true
	// }

	return modified
}

// ensureChatSettings проверяет наличие настроек в БД и создает их с дефолтами, если их нет.
// Всегда возвращает актуальные настройки (либо найденные, либо только что созданные).
func (ms *MongoStorage) ensureChatSettings(ctx context.Context, chatID int64) (*ChatSettings, error) {
	filter := bson.M{"chat_id": chatID}
	var existingSettings ChatSettings

	err := ms.settingsCollection.FindOne(ctx, filter).Decode(&existingSettings)
	if err == nil {
		// Настройки найдены, применяем дефолты и возвращаем
		ms.applyDefaultsToSettings(&existingSettings)
		return &existingSettings, nil
	}

	if !errors.Is(err, mongo.ErrNoDocuments) {
		// Ошибка, не связанная с отсутствием документа
		log.Printf("[ensureChatSettings ERROR] Chat %d: Ошибка проверки настроек: %v", chatID, err)
		return nil, fmt.Errorf("ошибка проверки настроек: %w", err)
	}

	// Настроек нет, создаем новые с дефолтами из конфига
	log.Printf("[ensureChatSettings INFO] Chat %d: Создание настроек чата по умолчанию.", chatID)
	newSettings := ChatSettings{
		ChatID: chatID,
		// Явно устанавливаем значения из конфига, используя указатели
	}
	ms.applyDefaultsToSettings(&newSettings) // Применяем дефолты ко всем полям

	// Сохраняем новые настройки в БД
	_, insertErr := ms.settingsCollection.InsertOne(ctx, newSettings)
	if insertErr != nil {
		log.Printf("[ensureChatSettings ERROR] Chat %d: Ошибка сохранения дефолтных настроек: %v", chatID, insertErr)
		return nil, fmt.Errorf("ошибка сохранения дефолтных настроек: %w", insertErr)
	}

	log.Printf("[ensureChatSettings INFO] Chat %d: Дефолтные настройки успешно созданы и сохранены.", chatID)
	return &newSettings, nil
}

// SetChatSettings сохраняет настройки для указанного чата в MongoDB
func (ms *MongoStorage) SetChatSettings(settings *ChatSettings) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	filter := bson.M{"chat_id": settings.ChatID}

	// Используем $set для обновления только указанных полей или создания документа, если он не существует
	// Важно: Не используем omitempty в структуре ChatSettings для полей, которые могут быть false/0,
	// иначе они не будут сохранены при $set. Используем указатели.

	update := bson.M{
		"$set": bson.M{},
	}

	// Динамически добавляем поля в $set, только если они не nil
	// Это гарантирует, что мы обновляем только те поля, которые были изменены
	v := reflect.ValueOf(*settings)
	t := v.Type()

	setFields := update["$set"].(bson.M)

	for i := 0; i < v.NumField(); i++ {
		fieldValue := v.Field(i)
		fieldType := t.Field(i)
		bsonTag := fieldType.Tag.Get("bson")
		fieldName := strings.Split(bsonTag, ",")[0] // Получаем имя поля из bson тега

		if fieldName == "_id" || fieldName == "chat_id" || fieldName == "" {
			continue // Пропускаем ID и chat_id, они используются в фильтре
		}

		// Добавляем в $set только если поле не nil (для указателей)
		// или если это не указатель (для базовых типов типа chat_id, если бы он был в $set)
		if fieldValue.Kind() == reflect.Ptr {
			if !fieldValue.IsNil() {
				setFields[fieldName] = fieldValue.Interface()
			}
		} else {
			// Если это не указатель, добавляем всегда (например, для chat_id, если бы он был тут)
			// setFields[fieldName] = fieldValue.Interface()
			// В текущей структуре ChatSettings все изменяемые поля - указатели, поэтому else блок не нужен
		}
	}

	// Добавляем поле chat_id, если оно еще не добавлено (на случай, если $set пуст)
	if _, ok := setFields["chat_id"]; !ok {
		setFields["chat_id"] = settings.ChatID
	}

	ops := options.Update().SetUpsert(true) // Создать документ, если он не найден

	result, err := ms.settingsCollection.UpdateOne(ctx, filter, update, ops)
	if err != nil {
		log.Printf("[SetChatSettings ERROR] Chat %d: Ошибка сохранения/обновления настроек: %v", settings.ChatID, err)
		return fmt.Errorf("ошибка сохранения/обновления настроек: %w", err)
	}

	if ms.debug {
		if result.UpsertedCount > 0 {
			log.Printf("[SetChatSettings DEBUG] Chat %d: Настройки успешно созданы (UpsertedID: %v).", settings.ChatID, result.UpsertedID)
		} else if result.ModifiedCount > 0 {
			log.Printf("[SetChatSettings DEBUG] Chat %d: Настройки успешно обновлены (Matched: %d, Modified: %d).", settings.ChatID, result.MatchedCount, result.ModifiedCount)
		} else {
			log.Printf("[SetChatSettings DEBUG] Chat %d: Настройки не были изменены (Matched: %d, Modified: %d).", settings.ChatID, result.MatchedCount, result.ModifiedCount)
		}
	}

	return nil
}

// --- Методы для обновления отдельных настроек чата ---

// updateSingleChatSetting обновляет одно поле в документе настроек чата.
func (ms *MongoStorage) updateSingleChatSetting(chatID int64, fieldName string, value interface{}) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	filter := bson.M{"chat_id": chatID}
	update := bson.M{"$set": bson.M{fieldName: value}}
	ops := options.Update().SetUpsert(true) // Создаем настройки, если их нет

	result, err := ms.settingsCollection.UpdateOne(ctx, filter, update, ops)
	if err != nil {
		log.Printf("[UpdateSetting ERROR] Chat %d, Field %s: Ошибка обновления: %v", chatID, fieldName, err)
		return fmt.Errorf("ошибка обновления настройки %s: %w", fieldName, err)
	}

	if ms.debug {
		log.Printf("[UpdateSetting DEBUG] Chat %d, Field %s: Результат обновления (Matched: %d, Modified: %d, Upserted: %v)",
			chatID, fieldName, result.MatchedCount, result.ModifiedCount, result.UpsertedID)
	}
	return nil
}

// UpdateDirectLimitEnabled обновляет настройку включения лимита прямых ответов.
func (ms *MongoStorage) UpdateDirectLimitEnabled(chatID int64, enabled bool) error {
	return ms.updateSingleChatSetting(chatID, "direct_reply_limit_enabled", enabled)
}

// UpdateDirectLimitCount обновляет настройку количества сообщений для лимита прямых ответов.
func (ms *MongoStorage) UpdateDirectLimitCount(chatID int64, count int) error {
	if count < 0 {
		return errors.New("количество сообщений не может быть отрицательным")
	}
	return ms.updateSingleChatSetting(chatID, "direct_reply_limit_count", count)
}

// UpdateDirectLimitDuration обновляет настройку длительности периода для лимита прямых ответов.
func (ms *MongoStorage) UpdateDirectLimitDuration(chatID int64, duration time.Duration) error {
	if duration <= 0 {
		return errors.New("длительность периода должна быть положительной")
	}
	durationMinutes := int(duration.Minutes()) // Сохраняем в минутах
	return ms.updateSingleChatSetting(chatID, "direct_reply_limit_duration_minutes", durationMinutes)
}

// UpdateVoiceTranscriptionEnabled обновляет настройку включения транскрипции голоса.
func (ms *MongoStorage) UpdateVoiceTranscriptionEnabled(chatID int64, enabled bool) error {
	return ms.updateSingleChatSetting(chatID, "voice_transcription_enabled", enabled)
}

// UpdateSrachAnalysisEnabled обновляет настройку включения анализа срачей.
func (ms *MongoStorage) UpdateSrachAnalysisEnabled(chatID int64, enabled bool) error {
	return ms.updateSingleChatSetting(chatID, "srach_analysis_enabled", enabled)
}
