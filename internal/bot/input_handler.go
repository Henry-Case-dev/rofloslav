package bot

import (
	"fmt"
	"log"
	"time"

	"github.com/Henry-Case-dev/rofloslav/internal/storage"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Commenting out duplicate function
/*
func parseProfileArgs(args string) (username, alias, gender, realName, bio string, err error) {
	// Ищем @username в начале или конце строки (необязательно)
	reUsername := regexp.MustCompile(`^@(\w+)\s+|\s+@(\w+)$`)
	matchesUsername := reUsername.FindStringSubmatch(args)
	if len(matchesUsername) > 1 {
		if matchesUsername[1] != "" {
			username = matchesUsername[1]
		} else {
			username = matchesUsername[2]
		}
		// Удаляем найденный @username из строки для дальнейшего парсинга
		args = reUsername.ReplaceAllString(args, "")
	}

	// Регулярное выражение для парсинга аргументов вида {key=value}
	// Используем обычную строку с экранированием, а не raw string
	reArgs := regexp.MustCompile("{(\\w+)=([^}]+)}")
	matches := reArgs.FindAllStringSubmatch(args, -1)

	parsedKeys := make(map[string]string)
	for _, match := range matches {
		if len(match) == 3 {
			key := strings.ToLower(match[1])
			value := strings.TrimSpace(match[2])
			parsedKeys[key] = value
		}
	}

	// Извлекаем значения
	if val, ok := parsedKeys["пол"]; ok {
		gender = strings.ToUpper(val)
		if gender != "М" && gender != "Ж" {
			err = errors.New("неверный формат пола (должно быть М или Ж)")
			return
		}
	}

	alias = parsedKeys["alias"] // Псевдоним
	realName = parsedKeys["имя"] // Реальное имя
	bio = parsedKeys["био"]      // Био

	// Валидация: хотя бы одно поле, кроме username, должно быть заполнено
	if alias == "" && gender == "" && realName == "" && bio == "" {
		err = errors.New("необходимо указать хотя бы одно поле профиля (alias, пол, имя или био)")
		return
	}

	// Валидация: Username обязателен, если его не удалось найти
	if username == "" {
		err = errors.New("не указан @username пользователя")
		return
	}

	return
}
*/

// handleInput ожидает и обрабатывает ввод пользователя для ожидаемой настройки
func (b *Bot) handleInput(update tgbotapi.Update) bool {
	if update.Message == nil || update.Message.Text == "" {
		return false // Не сообщение или пустое сообщение
	}

	chatID := update.Message.Chat.ID
	userID := update.Message.From.ID
	username := update.Message.From.UserName
	text := update.Message.Text

	b.settingsMutex.RLock() // Используем RLock для проверки
	pendingSettingKey, waiting := b.pendingSettings[chatID]
	b.settingsMutex.RUnlock()

	if !waiting {
		return false // Не ожидаем ввода для этого чата
	}

	// Пользователь что-то ввел, пока мы ждали настройку
	log.Printf("[DEBUG][InputHandler] Chat %d: Получен ввод \"%s\" для ожидаемой настройки '%s' от пользователя %d (@%s)",
		chatID, text, pendingSettingKey, userID, username)

	// Вызываем обработчик для конкретной настройки
	err := b.handlePendingSettingInput(chatID, userID, username, pendingSettingKey, text, update.Message.MessageID)

	// Удаляем ключ ожидания ввода из карты В ЛЮБОМ СЛУЧАЕ (успех, ошибка или отмена)
	b.settingsMutex.Lock()
	delete(b.pendingSettings, chatID)
	if b.config.Debug {
		log.Printf("[DEBUG][InputHandler] Chat %d: Ключ ожидания ввода '%s' удален.", chatID, pendingSettingKey)
	}
	b.settingsMutex.Unlock()

	// Отправляем подтверждение или сообщение об ошибке
	if err != nil {
		log.Printf("[ERROR][InputHandler] Chat %d: Ошибка обработки ввода для '%s': %v", chatID, pendingSettingKey, err)
		b.sendReply(chatID, fmt.Sprintf("🚫 Ошибка при установке '%s': %v", pendingSettingKey, err))
	} else {
		// Успех (сообщение об успехе отправляется внутри handlePendingSettingInput)
		// b.sendReplyAndDeleteAfter(chatID, fmt.Sprintf("✅ Настройка '%s' успешно обновлена.", pendingSettingKey), 5*time.Second)
	}

	// Обновляем клавиатуру настроек после успешного ввода или ошибки
	go b.updateSettingsKeyboardAfterInput(chatID)

	return true // Ввод был обработан
}

// handlePendingSettingInput обрабатывает ввод пользователя для ожидаемой настройки.
// Вызывается из handleMessage, когда обнаружено, что сообщение от пользователя
// соответствует ожидаемому вводу (PendingSetting != "").
// Commenting out duplicate function
/*
func (b *Bot) handlePendingSettingInput(chatID int64, userID int64, username string, pendingSettingKey string, text string) error {
	log.Printf("[DEBUG][InputHandler] Chat %d User %d (%s): Обработка ввода для ключа \'%s\'. Текст: \'%s\'", chatID, userID, username, pendingSettingKey, text)

	// Сначала проверим на команду /cancel (в нижнем регистре для надежности)
	if strings.ToLower(text) == "/cancel" {
		log.Printf("[DEBUG][InputHandler] Chat %d User %d: Отмена ввода настройки \'%s\'.", chatID, userID, pendingSettingKey)
		b.settingsMutex.Lock()
		if settings, exists := b.chatSettings[chatID]; exists {
			settings.PendingSetting = "" // Сбрасываем ожидание
			settings.PendingSettingUserID = 0
			// LastInfoMessageID будет удален в handleMessage, здесь не трогаем
		}
		b.settingsMutex.Unlock()
		b.sendReplyAndDeleteAfter(chatID, "Ввод отменен.", 5*time.Second) // Отправляем подтверждение и удаляем через 5 сек

		// Обновляем клавиатуру настроек после отмены
		go b.updateSettingsKeyboardAfterInput(chatID)
		return nil // Успешно обработали отмену
	}

	// --- Обработка ввода для конкретных настроек ---

	var operationStatus string // Статус операции для сообщения пользователю
	var updateErr error        // Ошибка при обновлении настройки
	var needsKeyboardUpdate = true // Флаг, нужно ли обновлять клавиатуру настроек

	switch pendingSettingKey {
	case "profile_data":
		// --- Handle 'profile_data' input ---
		needsKeyboardUpdate = false // Не обновляем клавиатуру для профиля
		log.Printf("[DEBUG][InputHandler Profile] Чат %d: Обработка ввода профиля: %s", chatID, text)

		targetUsername, alias, gender, realName, bio, parseErr := parseProfileArgs(text)
		if parseErr != nil {
			log.Printf("[ERROR][InputHandler Profile] Чат %d: Ошибка парсинга данных профиля \'%s\': %v", chatID, text, parseErr)
			b.sendReply(chatID, fmt.Sprintf("🚫 Ошибка парсинга: %v\\nПопробуйте еще раз или введите /cancel для отмены.", parseErr))
			// Оставляем PendingSetting, чтобы пользователь мог попробовать еще раз
			return errors.New("ошибка парсинга данных профиля") // Возвращаем ошибку, но не сбрасываем PendingSetting
		}

		log.Printf("[DEBUG][InputHandler Profile] Чат %d: Распарсено: User=%s, Alias=%s, Gender=%s, RealName=%s, Bio=%s",
			chatID, targetUsername, alias, gender, realName, bio)

		// Пытаемся найти существующий профиль по username
		existingProfile, findErr := b.findUserProfileByUsername(chatID, targetUsername)
		if findErr != nil {
			log.Printf("[ERROR][InputHandler Profile] Чат %d: Ошибка поиска профиля по username \'%s\': %v", chatID, targetUsername, findErr)
			b.sendReply(chatID, "🚫 Произошла ошибка при поиске существующего профиля. Попробуйте позже.")
			// Сбрасываем ожидание, так как произошла ошибка бд
			b.settingsMutex.Lock()
			if settings, exists := b.chatSettings[chatID]; exists {
				settings.PendingSetting = ""
				settings.PendingSettingUserID = 0
			}
			b.settingsMutex.Unlock()
			return fmt.Errorf("ошибка поиска профиля: %w", findErr)
		}

		var profileToSave storage.UserProfile
		if existingProfile != nil {
			log.Printf("[DEBUG][InputHandler Profile] Чат %d: Найден существующий профиль для @%s (UserID: %d). Обновляем.", chatID, targetUsername, existingProfile.UserID)
			profileToSave = *existingProfile // Копируем существующий
			// Обновляем только те поля, которые были введены
			profileToSave.Alias = alias       // Всегда обновляем Alias
			profileToSave.Gender = gender     // Всегда обновляем Gender
			profileToSave.RealName = realName // Всегда обновляем RealName
			profileToSave.Bio = bio           // Всегда обновляем Bio
		} else {
			log.Printf("[DEBUG][InputHandler Profile] Чат %d: Профиль для @%s не найден. Создаем новый.", chatID, targetUsername)
			// Пытаемся получить ID пользователя по username (может быть неточным, если пользователя нет в чате)
			foundUserID, _ := b.getUserIDByUsername(chatID, targetUsername) // Ошибка здесь игнорируется намеренно
			if foundUserID == 0 {
				log.Printf("[WARN][InputHandler Profile] Чат %d: Не удалось определить UserID для @%s. Профиль будет создан без UserID.", chatID, targetUsername)
			}
			profileToSave = storage.UserProfile{
				ChatID:   chatID,
				UserID:   foundUserID, // Может быть 0
				Username: targetUsername,
				Alias:    alias,
				Gender:   gender,
				RealName: realName,
				Bio:      bio,
			}
		}

		// Устанавливаем время последнего обновления
		profileToSave.LastSeen = time.Now() // Используем текущее время как LastSeen при обновлении профиля

		// Сохраняем профиль
		updateErr = b.storage.SetUserProfile(&profileToSave)
		if updateErr != nil {
			log.Printf("[ERROR][InputHandler Profile] Чат %d: Ошибка сохранения профиля для @%s: %v", chatID, targetUsername, updateErr)
			operationStatus = "🚫 Произошла ошибка при сохранении профиля."
		} else {
			log.Printf("[INFO][InputHandler Profile] Чат %d: Профиль для @%s успешно сохранен/обновлен.", chatID, targetUsername)
			operationStatus = fmt.Sprintf("✅ Профиль для @%s успешно сохранен/обновлен.", targetUsername)
		}

	case "direct_limit_count":
		valueInt, err := strconv.Atoi(text)
		if err != nil {
			log.Printf("[WARN][InputHandler] Chat %d: Неверный ввод для %s: \'%s\' - не число.", chatID, pendingSettingKey, text)
			b.sendReply(chatID, "🚫 Введите числовое значение или /cancel для отмены.")
			return errors.New("неверный ввод: ожидалось число") // Возвращаем ошибку, не сбрасываем PendingSetting
		}
		if valueInt >= 0 { // 0 means unlimited
			updateErr = b.storage.UpdateDirectLimitCount(chatID, valueInt)
			if updateErr != nil {
				log.Printf("[ERROR][InputHandler] Chat %d: Не удалось сохранить direct_limit_count %d: %v", chatID, valueInt, updateErr)
				operationStatus = "🚫 Ошибка при сохранении лимита сообщений."
			} else {
				log.Printf("[INFO][InputHandler] Chat %d: Лимит прямых сообщений установлен: %d", chatID, valueInt)
				if valueInt == 0 {
					operationStatus = "✅ Лимит прямых сообщений снят (установлен в 0)."
				} else {
					operationStatus = fmt.Sprintf("✅ Лимит прямых сообщений установлен: %d", valueInt)
				}
			}
		} else {
			b.sendReply(chatID, "🚫 Ошибка: Количество сообщений должно быть 0 или больше. Попробуйте еще раз или /cancel.")
			return errors.New("неверный ввод: значение должно быть >= 0") // Возвращаем ошибку, не сбрасываем PendingSetting
		}

	case "direct_limit_duration":
		valueInt, err := strconv.Atoi(text)
		if err != nil {
			log.Printf("[WARN][InputHandler] Chat %d: Неверный ввод для %s: \'%s\' - не число.", chatID, pendingSettingKey, text)
			b.sendReply(chatID, "🚫 Введите числовое значение (в минутах) или /cancel для отмены.")
			return errors.New("неверный ввод: ожидалось число") // Возвращаем ошибку, не сбрасываем PendingSetting
		}
		if valueInt > 0 { // Duration must be positive
			duration := time.Duration(valueInt) * time.Minute
			updateErr = b.storage.UpdateDirectLimitDuration(chatID, duration)
			if updateErr != nil {
				log.Printf("[ERROR][InputHandler] Chat %d: Не удалось сохранить direct_limit_duration %d мин: %v", chatID, valueInt, updateErr)
				operationStatus = "🚫 Ошибка при сохранении периода лимита."
			} else {
				log.Printf("[INFO][InputHandler] Chat %d: Период лимита прямых сообщений установлен: %d минут", chatID, valueInt)
				operationStatus = fmt.Sprintf("✅ Период лимита прямых сообщений установлен: %d минут", valueInt)
			}
		} else {
			b.sendReply(chatID, "🚫 Ошибка: Период должен быть больше 0 минут. Попробуйте еще раз или /cancel.")
			return errors.New("неверный ввод: значение должно быть > 0") // Возвращаем ошибку, не сбрасываем PendingSetting
		}

	// Добавьте обработку других ключей здесь, если они появятся

	default:
		log.Printf("[WARN][InputHandler] Chat %d: Получен ввод \'%s\' для неизвестного или необработанного ключа \'%s\'", chatID, text, pendingSettingKey)
		operationStatus = fmt.Sprintf("Неизвестная настройка: %s", pendingSettingKey)
		updateErr = fmt.Errorf("неизвестный ключ настройки: %s", pendingSettingKey) // Устанавливаем ошибку
	}

	// --- Постобработка после switch ---

	// Отправляем сообщение о статусе пользователю (если оно есть)
	if operationStatus != "" {
		b.sendReplyAndDeleteAfter(chatID, operationStatus, 10*time.Second)
	}

	// Сбрасываем состояние ожидания только в случае успеха или необратимой ошибки
	if updateErr == nil || pendingSettingKey == "profile_data" { // Сбрасываем для профиля всегда после попытки
		b.settingsMutex.Lock()
		if settings, exists := b.chatSettings[chatID]; exists {
			settings.PendingSetting = ""
			settings.PendingSettingUserID = 0
		}
		b.settingsMutex.Unlock()
		log.Printf("[DEBUG][InputHandler] Chat %d: Сброшен PendingSetting для ключа \'%s\' после обработки.", chatID, pendingSettingKey)
	} else {
		log.Printf("[DEBUG][InputHandler] Chat %d: PendingSetting для ключа \'%s\' НЕ сброшен из-за ошибки: %v", chatID, pendingSettingKey, updateErr)
	}

	// Обновляем клавиатуру настроек асинхронно, если нужно
	if needsKeyboardUpdate && updateErr == nil { // Обновляем только при успехе и если требуется
		go b.updateSettingsKeyboardAfterInput(chatID)
	}

	return updateErr // Возвращаем ошибку, если она была при обновлении
}
*/

// updateSettingsKeyboardAfterInput обновляет сообщение с клавиатурой настроек
// после завершения ввода (успешного или с отменой/ошибкой).
func (b *Bot) updateSettingsKeyboardAfterInput(chatID int64) {
	// Небольшая задержка, чтобы дать время обработаться другим событиям (удалению сообщений)
	time.Sleep(1 * time.Second)

	b.settingsMutex.RLock()
	settings, exists := b.chatSettings[chatID]
	lastSettingsMsgID := 0
	if exists {
		lastSettingsMsgID = settings.LastSettingsMessageID
	}
	b.settingsMutex.RUnlock()

	if !exists {
		log.Printf("[WARN][updateSettingsKeyboardAfterInput] Chat %d: Настройки не найдены, не могу обновить клавиатуру.", chatID)
		return
	}

	if lastSettingsMsgID == 0 {
		log.Printf("[DEBUG][updateSettingsKeyboardAfterInput] Chat %d: Нет ID сообщения настроек для обновления. Отправляю новую клавиатуру.", chatID)
		b.sendSettingsKeyboard(chatID, 0) // Отправляем новую, если старой нет
		return
	}

	// Получаем актуальные настройки из БД для отображения
	dbSettings, errDb := b.storage.GetChatSettings(chatID)
	if errDb != nil {
		log.Printf("[ERROR][updateSettingsKeyboardAfterInput] Chat %d: Ошибка получения настроек из БД: %v. Клавиатура может быть неактуальна.", chatID, errDb)
		// Можно отправить сообщение об ошибке или использовать пустые настройки
		dbSettings = &storage.ChatSettings{} // Используем пустые, чтобы не паниковать
	}

	// Формируем новую клавиатуру
	newKeyboard := getSettingsKeyboard(dbSettings, b.config)
	newText := "⚙️ Настройки чата:" // Обновляем и текст на всякий случай

	// Создаем конфиг для редактирования
	editMsg := tgbotapi.NewEditMessageTextAndMarkup(chatID, lastSettingsMsgID, newText, newKeyboard)

	// Отправляем запрос на редактирование
	_, err := b.api.Send(editMsg)
	if err != nil {
		log.Printf("[WARN][updateSettingsKeyboardAfterInput] Chat %d: Не удалось обновить клавиатуру настроек (msg ID %d): %v. Возможно, сообщение было удалено.", chatID, lastSettingsMsgID, err)
		// Если не удалось обновить, попробуем отправить новую клавиатуру
		b.sendSettingsKeyboard(chatID, 0)
	}
}

/* Удаляем дубликат
// findUserProfileByUsername ищет профиль пользователя по его @username в данном чате.
// Возвращает найденный профиль или nil, если не найден, и ошибку, если произошла ошибка БД.
func (b *Bot) findUserProfileByUsername(chatID int64, username string) (*storage.UserProfile, error) {
	// Удаляем вызов несуществующего метода
	// profile, err := b.storage.FindUserProfileByUsername(chatID, username)
	// Заглушка:
	log.Printf("[WARN][findUserProfileByUsername DUPLICATE] Chat %d: Вызвана дублирующая функция в input_handler. Используйте из helpers.go.", chatID)
	return nil, fmt.Errorf("поиск профиля по username в input_handler.go не реализован")
}
*/

/* Удаляем дубликат
// getUserIDByUsername пытается найти UserID по @username.
// Возвращает UserID или 0, если не найден, и ошибку, если произошла ошибка.
func (b *Bot) getUserIDByUsername(chatID int64, username string) (int64, error) {
	// Удаляем ссылки на несуществующие поля
	// b.recentUsersMutex.RLock()
	// defer b.recentUsersMutex.RUnlock()
	// if chatUsers, ok := b.recentUsers[chatID]; ok {
	// 	for userID, userInfo := range chatUsers {
	// 		if strings.EqualFold(userInfo.Username, username) {
	// 			return userID, nil
	// 		}
	// 	}
	// }
	// Заглушка:
	log.Printf("[WARN][getUserIDByUsername DUPLICATE] Chat %d: Вызвана дублирующая функция в input_handler. Используйте из helpers.go.", chatID)
	return 0, fmt.Errorf("поиск ID по username в input_handler.go не реализован")
}
*/
