package bot

import (
	"log"
	"time"

	"github.com/Henry-Case-dev/rofloslav/internal/storage"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// updateUserProfileIfNeeded проверяет, нужно ли обновить профиль пользователя,
// и создает новый профиль, если он не существует.
// Не изменяет существующие профили.
// Эта функция была перенесена из message_handler.go.
func (b *Bot) updateUserProfileIfNeeded(chatID int64, user *tgbotapi.User, messageDate int) {
	if user == nil {
		log.Printf("[WARN][UpdateProfileIfNeeded] Chat %d: User is nil. Cannot update profile.", chatID)
		return
	}

	userID := user.ID

	if b.config.Debug {
		log.Printf("[DEBUG][UpdateProfileIfNeeded] Chat %d, User %d (@%s): Checking profile...", chatID, userID, user.UserName)
	}

	// 1. Проверяем, существует ли профиль в БД
	existingProfile, err := b.storage.GetUserProfile(chatID, userID)
	if err != nil {
		// Логируем ошибку получения профиля, но не считаем это критичным для создания нового
		log.Printf("[ERROR][UpdateProfileIfNeeded] Chat %d, User %d: Ошибка при проверке существования профиля: %v", chatID, userID, err)
		// Мы можем продолжить и попытаться создать профиль, если его нет
	}

	// 2. Если профиль существует, ничего не делаем
	if existingProfile != nil {
		if b.config.Debug {
			log.Printf("[DEBUG][UpdateProfileIfNeeded] Chat %d, User %d (@%s): Профиль уже существует, автоматическое обновление пропущено.", chatID, userID, existingProfile.Username)
		}
		return
	}

	// 3. Если профиль не существует, создаем новый
	if b.config.Debug {
		log.Printf("[DEBUG][UpdateProfileIfNeeded] Chat %d, User %d (@%s): Профиль не найден, создаю новый...", chatID, userID, user.UserName)
	}

	newProfile := &storage.UserProfile{
		ChatID:    chatID,
		UserID:    userID,
		Username:  user.UserName,
		Alias:     user.FirstName, // Используем FirstName как Alias по умолчанию
		Gender:    "",             // Пол пока неизвестен
		RealName:  "",             // Реальное имя пока неизвестно
		Bio:       "",             // Био пока пустое
		LastSeen:  time.Unix(int64(messageDate), 0),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	// 4. Сохраняем новый профиль
	err = b.storage.SetUserProfile(newProfile)
	if err != nil {
		log.Printf("[ERROR][UpdateProfileIfNeeded] Chat %d, User %d: Ошибка при сохранении нового профиля: %v", chatID, userID, err)
		return
	}

	if b.config.Debug {
		log.Printf("[DEBUG][UpdateProfileIfNeeded] Chat %d, User %d (@%s): Новый профиль успешно создан и сохранен.", chatID, userID, newProfile.Username)
	}
}
