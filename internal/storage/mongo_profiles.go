package storage

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// === Методы для профилей пользователей ===

// GetUserProfile возвращает профиль пользователя для конкретного чата из MongoDB.
func (ms *MongoStorage) GetUserProfile(chatID int64, userID int64) (*UserProfile, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var profile UserProfile
	filter := bson.M{"chat_id": chatID, "user_id": userID}

	err := ms.userProfilesCollection.FindOne(ctx, filter).Decode(&profile)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			// Профиль не найден - это не ошибка, возвращаем nil
			if ms.debug {
				log.Printf("DEBUG: Профиль пользователя userID %d в чате %d не найден в MongoDB.", userID, chatID)
			}
			return nil, nil
		}
		// Другая ошибка базы данных
		log.Printf("ERROR: Ошибка получения профиля userID %d в чате %d из MongoDB: %v", userID, chatID, err)
		return nil, fmt.Errorf("ошибка получения профиля из MongoDB: %w", err)
	}

	if ms.debug {
		log.Printf("DEBUG: Профиль пользователя userID %d в чате %d успешно получен из MongoDB.", userID, chatID)
	}
	return &profile, nil
}

// SetUserProfile создает или обновляет профиль пользователя для конкретного чата в MongoDB.
func (ms *MongoStorage) SetUserProfile(profile *UserProfile) error {
	if profile == nil {
		return errors.New("нельзя сохранить nil профиль")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	filter := bson.M{"chat_id": profile.ChatID, "user_id": profile.UserID}

	// Устанавливаем время обновления
	profile.UpdatedAt = time.Now()

	// Используем опцию Upsert=true, чтобы создать профиль, если он не существует,
	// или обновить его, если существует.
	// $set оператор гарантирует, что мы обновим все поля, переданные в структуре profile.
	// $setOnInsert установит время создания только при первой вставке.
	update := bson.M{
		"$set": bson.M{
			"username":   profile.Username,
			"alias":      profile.Alias,
			"gender":     profile.Gender,
			"real_name":  profile.RealName,
			"bio":        profile.Bio,
			"last_seen":  profile.LastSeen, // Используем LastSeen из профиля (например, время последнего сообщения)
			"updated_at": profile.UpdatedAt,
		},
		"$setOnInsert": bson.M{
			"created_at": time.Now(), // Устанавливаем время создания только при вставке
		},
	}

	ops := options.Update().SetUpsert(true)

	result, err := ms.userProfilesCollection.UpdateOne(ctx, filter, update, ops)
	if err != nil {
		log.Printf("ERROR: Ошибка сохранения/обновления профиля userID %d в чате %d: %v", profile.UserID, profile.ChatID, err)
		return fmt.Errorf("ошибка сохранения/обновления профиля: %w", err)
	}

	if ms.debug {
		if result.UpsertedCount > 0 {
			log.Printf("DEBUG: Профиль пользователя userID %d в чате %d успешно создан.", profile.UserID, profile.ChatID)
		} else if result.ModifiedCount > 0 {
			log.Printf("DEBUG: Профиль пользователя userID %d в чате %d успешно обновлен.", profile.UserID, profile.ChatID)
		} else {
			// Это может произойти, если данные профиля не изменились
			log.Printf("DEBUG: Профиль пользователя userID %d в чате %d не был изменен (возможно, данные идентичны).", profile.UserID, profile.ChatID)
		}
	}

	return nil
}

// GetAllUserProfiles возвращает все профили пользователей для указанного чата из MongoDB.
func (ms *MongoStorage) GetAllUserProfiles(chatID int64) ([]*UserProfile, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	filter := bson.M{"chat_id": chatID}
	cursor, err := ms.userProfilesCollection.Find(ctx, filter)
	if err != nil {
		log.Printf("ERROR: Ошибка получения всех профилей для чата %d: %v", chatID, err)
		return nil, fmt.Errorf("ошибка получения всех профилей: %w", err)
	}
	defer cursor.Close(ctx)

	var profiles []*UserProfile
	if err = cursor.All(ctx, &profiles); err != nil {
		log.Printf("ERROR: Ошибка декодирования всех профилей для чата %d: %v", chatID, err)
		return nil, fmt.Errorf("ошибка декодирования всех профилей: %w", err)
	}

	if ms.debug {
		log.Printf("DEBUG: Успешно получено %d профилей для чата %d.", len(profiles), chatID)
	}

	return profiles, nil
}
