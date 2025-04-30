package gemini

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"github.com/Henry-Case-dev/rofloslav/internal/config"
	"github.com/Henry-Case-dev/rofloslav/internal/utils"
)

var botUserID int64

// markdownInstructions содержит инструкции по форматированию Markdown для LLM.
// Обновлено для стандартного Markdown (не V2).
const markdownInstructions = `\n\nИнструкции по форматированию ответа (Стандартный Markdown):\n- Используй *жирный текст* для выделения важных слов или фраз (одинарные звездочки).\n- Используй _курсив_ для акцентов или названий (одинарные подчеркивания).\n- Используй 'моноширинный текст' для кода, команд или технических терминов (одинарные кавычки).\n- НЕ используй зачеркивание (~~текст~~).\n- НЕ используй спойлеры (||текст||).\n- НЕ используй подчеркивание (__текст__).\n- Ссылки оформляй как [текст ссылки](URL).\n- Блоки кода оформляй тремя обратными кавычками:\n'''\nкод\n'''\nили\n'''go\nкод\n'''\n- Нумерованные списки начинай с \"1. \", \"2. \" и т.д.\n- Маркированные списки начинай с \"- \" или \"* \".\n- Для цитат используй \"> \".\n- Не нужно экранировать символы вроде '.', '-', '!', '(', ')', '+', '#'. Стандартный Markdown менее строгий.\n- Используй ТОЛЬКО указанный Markdown. Не используй HTML.\n`

// Client для взаимодействия с Gemini API
type Client struct {
	genaiClient        *genai.Client
	cfg                *config.Config // Ссылка на конфигурацию
	modelName          string
	embeddingModelName string
	debug              bool
	keyMutex           sync.Mutex // Мьютекс для безопасного переключения ключей
}

// New создает и инициализирует новый клиент Gemini.
// Принимает API ключ, имя основной модели, имя модели для эмбеддингов и флаг отладки.
func New(cfg *config.Config, modelName, embeddingModelName string, debug bool) (*Client, error) {
	if cfg.GeminiAPIKey == "" {
		return nil, fmt.Errorf("API ключ Gemini не предоставлен")
	}
	if modelName == "" {
		return nil, fmt.Errorf("имя модели Gemini не предоставлено")
	}

	// Получаем ID бота из переменной окружения
	botIDStr := os.Getenv("BOT_USER_ID")
	if botIDStr != "" {
		var err error
		botUserID, err = strconv.ParseInt(botIDStr, 10, 64)
		if err != nil {
			log.Printf("[WARN] Не удалось преобразовать BOT_USER_ID ('%s') в int64: %v", botIDStr, err)
		} else {
			log.Printf("[INFO] ID бота загружен из переменной окружения: %d", botUserID)
		}
	} else {
		log.Printf("[WARN] Переменная окружения BOT_USER_ID не установлена. Определение сообщений бота может быть неточным.")
	}

	// Выбираем API ключ в зависимости от флага использования резервного ключа
	apiKey := cfg.GeminiAPIKey
	if cfg.GeminiUsingReserveKey && cfg.GeminiAPIKeyReserve != "" {
		apiKey = cfg.GeminiAPIKeyReserve
		log.Printf("[INFO] Gemini: Используется резервный ключ API.")
	}

	ctx := context.Background()
	genaiClient, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		return nil, fmt.Errorf("ошибка создания клиента genai: %w", err)
	}

	log.Printf("Клиент Gemini инициализирован для модели: %s", modelName)

	return &Client{
		genaiClient:        genaiClient,
		cfg:                cfg,
		modelName:          modelName,
		embeddingModelName: embeddingModelName,
		debug:              debug,
		keyMutex:           sync.Mutex{},
	}, nil
}

// Close закрывает клиент Gemini
func (c *Client) Close() error {
	c.keyMutex.Lock()
	defer c.keyMutex.Unlock()

	if c.genaiClient != nil {
		return c.genaiClient.Close()
	}
	return nil
}

// GenerateResponse генерирует ответ с использованием Gemini API
// history - сообщения ДО lastMessage
// lastMessage - сообщение, на которое отвечаем
func (c *Client) GenerateResponse(systemPrompt string, history []*tgbotapi.Message, lastMessage *tgbotapi.Message) (string, error) {
	// Попробуем вернуться к основному ключу, если используется резервный и прошло достаточно времени
	if c.cfg.GeminiUsingReserveKey {
		if err := c.tryRevertToMainKey(); err != nil {
			log.Printf("[WARN] Не удалось вернуться к основному ключу Gemini: %v", err)
		}
	}

	ctx := context.Background()
	model := c.genaiClient.GenerativeModel(c.modelName)

	// Настройки модели
	model.SetTemperature(1)
	model.SetTopP(0.95)
	model.SetMaxOutputTokens(8192)

	// Устанавливаем SystemInstruction
	if systemPrompt != "" {
		model.SystemInstruction = &genai.Content{
			Parts: []genai.Part{genai.Text(utils.SanitizeUTF8(systemPrompt))},
		}
		if c.debug {
			log.Printf("[DEBUG] Gemini Запрос: Установлен SystemInstruction: %s...", utils.TruncateString(systemPrompt, 100))
		}
	}

	// Начинаем чат сессию
	session := model.StartChat()

	// Формируем историю для API (только history, без lastMessage)
	preparedHistory := c.prepareChatHistory(history) // Функция теперь принимает только историю ДО последнего сообщения

	// Устанавливаем подготовленную историю
	session.History = preparedHistory

	// Формируем контент для отправки из lastMessage
	var contentToSend genai.Part
	lastMessageText := "" // Текст последнего сообщения
	if lastMessage != nil {
		lastMessageText = lastMessage.Text // Используем основной текст
		if lastMessageText == "" && lastMessage.Caption != "" {
			lastMessageText = lastMessage.Caption // Или caption
		}
	}

	if lastMessageText != "" {
		contentToSend = genai.Text(utils.SanitizeUTF8(lastMessageText))
		if c.debug {
			log.Printf("[DEBUG] Gemini Запрос: Текст lastMessage для отправки: %s...", utils.TruncateString(lastMessageText, 50))
		}
	} else {
		// Если lastMessage пустой (например, стикер или медиа без текста/caption), что отправлять?
		// Отправка пустого текста после истории все еще может вызвать 400.
		// Возможно, стоит передать плейсхолдер или описание медиа, если это важно.
		// Пока отправляем плейсхолдер, чтобы избежать пустой строки.
		contentToSend = genai.Text("[сообщение без текста]")
		if c.debug {
			log.Printf("[DEBUG] Gemini Запрос: lastMessage был пуст, отправляется плейсхолдер.")
		}
	}

	if c.debug {
		log.Printf("[DEBUG] Gemini Запрос: Подготовленная история содержит %d сообщений.", len(preparedHistory))
		log.Printf("[DEBUG] Gemini Запрос: Модель %s, Temp: %+v, TopP: %+v, MaxTokens: %+v",
			c.modelName, model.Temperature, model.TopP, model.MaxOutputTokens)
	}

	// Отправляем сообщение
	resp, err := session.SendMessage(ctx, contentToSend)
	if err != nil {
		if c.debug {
			log.Printf("[DEBUG] Gemini Ошибка отправки: %v", err)
		}

		// Обрабатываем ошибку и проверяем необходимость переключения ключа
		handledErr := c.handleAPIError(err)

		// Если произошло переключение ключа, пробуем запрос снова с новой сессией
		if handledErr != nil && handledErr.Error() == "ключ API Gemini был переключен на резервный, повторите запрос" {
			// Создаем новую модель с новым клиентом
			model = c.genaiClient.GenerativeModel(c.modelName)

			// Применяем те же настройки
			model.SetTemperature(1)
			model.SetTopP(0.95)
			model.SetMaxOutputTokens(8192)

			// Устанавливаем SystemInstruction
			if systemPrompt != "" {
				model.SystemInstruction = &genai.Content{
					Parts: []genai.Part{genai.Text(utils.SanitizeUTF8(systemPrompt))},
				}
			}

			// Создаем новую сессию
			session = model.StartChat()
			session.History = preparedHistory

			// Повторяем запрос
			resp, err = session.SendMessage(ctx, contentToSend)
			if err != nil {
				return "", fmt.Errorf("ошибка отправки сообщения в Gemini (после переключения ключа): %w", err)
			}
		} else if handledErr != nil {
			// Если ошибка не связана с переключением ключа или другая проблема
			return "", fmt.Errorf("ошибка отправки сообщения в Gemini: %w", handledErr)
		}
	}

	// Извлекаем ответ
	var responseText strings.Builder
	if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
		for _, part := range resp.Candidates[0].Content.Parts {
			responseText.WriteString(fmt.Sprintf("%s", part))
		}
	} else {
		if c.debug {
			log.Printf("[DEBUG] Gemini Ответ: Получен пустой ответ или нет кандидатов.")
		}
		return "", fmt.Errorf("Gemini не вернул валидный ответ")
	}

	finalResponse := responseText.String()
	if c.debug {
		log.Printf("[DEBUG] Gemini Ответ: %s...", utils.TruncateString(finalResponse, 100))
	}

	return finalResponse, nil
}

// prepareChatHistory подготавливает историю сообщений для Gemini API.
// Конвертирует и объединяет роли.
// НЕ включает последнее сообщение, т.к. оно передается отдельно.
func (c *Client) prepareChatHistory(messages []*tgbotapi.Message) []*genai.Content {
	if len(messages) == 0 {
		return []*genai.Content{}
	}

	// 1. Конвертируем все сообщения в genai.Content с ролями
	fullHistoryWithRoles := []*genai.Content{}
	for _, msg := range messages {
		content := c.convertMessageToGenaiContent(msg)
		if content != nil {
			fullHistoryWithRoles = append(fullHistoryWithRoles, content)
		}
	}

	if len(fullHistoryWithRoles) == 0 {
		return []*genai.Content{}
	}

	// 2. Объединяем последовательные сообщения с одинаковой ролью
	mergedHistory := []*genai.Content{fullHistoryWithRoles[0]}
	for i := 1; i < len(fullHistoryWithRoles); i++ {
		lastMerged := mergedHistory[len(mergedHistory)-1]
		current := fullHistoryWithRoles[i]

		if current.Role == lastMerged.Role {
			// Объединяем текст
			var combinedText strings.Builder
			for _, p := range lastMerged.Parts {
				combinedText.WriteString(fmt.Sprintf("%s", p))
			}
			combinedText.WriteString("\n") // Добавляем разделитель
			for _, p := range current.Parts {
				combinedText.WriteString(fmt.Sprintf("%s", p))
			}
			// Обновляем Parts последнего элемента в mergedHistory
			lastMerged.Parts = []genai.Part{genai.Text(combinedText.String())}
		} else {
			// Если роли разные, просто добавляем
			mergedHistory = append(mergedHistory, current)
		}
	}

	// 3. Убедимся, что история не заканчивается сообщением модели (если она не пустая)
	//    Если заканчивается, API может ожидать сообщение пользователя.
	//    Однако, передавая lastMessage отдельно, это должно решиться.
	//    Просто возвращаем объединенную историю.

	if c.debug {
		log.Printf("[DEBUG][prepareChatHistory] Исходных сообщений для истории: %d", len(messages))
		log.Printf("[DEBUG][prepareChatHistory] Сообщений после конвертации: %d", len(fullHistoryWithRoles))
		log.Printf("[DEBUG][prepareChatHistory] Финальная история для API после слияния содержит %d сообщений.", len(mergedHistory))
	}

	return mergedHistory
}

// convertMessageToGenaiContent преобразует одно сообщение Telegram
func (c *Client) convertMessageToGenaiContent(msg *tgbotapi.Message) *genai.Content {
	if msg == nil {
		return nil
	}
	// Считаем и пустые сообщения, если они не от бота (важно для сохранения чередования)
	// if msg.Text == "" && (msg.From == nil || !msg.From.IsBot){
	// 	return nil // Пропускаем пустые сообщения от пользователей (или без отправителя)
	// }

	// Определяем текст сообщения, учитывая подписи к медиа
	textContent := msg.Text
	if textContent == "" && msg.Caption != "" {
		textContent = msg.Caption // Используем подпись, если текста нет
	}

	// Если текста все равно нет, пропускаем сообщение (или можно вернуть пустой контент?)
	// Пока пропускаем, чтобы не отправлять пустоту в API
	if textContent == "" {
		return nil
	}

	role := "user"
	// Проверяем ID бота, если он загружен
	if botUserID != 0 {
		if msg.From != nil && msg.From.ID == botUserID {
			role = "model"
		}
	} else if msg.From != nil && msg.From.IsBot {
		// Fallback на IsBot, если ID не загружен
		role = "model"
	}

	return &genai.Content{
		Parts: []genai.Part{genai.Text(utils.SanitizeUTF8(textContent))},
		Role:  role,
	}
}

// GenerateArbitraryResponse генерирует ответ по произвольному текстовому контексту с минимальной обработкой
func (c *Client) GenerateArbitraryResponse(systemPrompt string, contextText string) (string, error) {
	// Попробуем вернуться к основному ключу, если используется резервный и прошло достаточно времени
	if c.cfg.GeminiUsingReserveKey {
		if err := c.tryRevertToMainKey(); err != nil {
			log.Printf("[WARN] Не удалось вернуться к основному ключу Gemini: %v", err)
		}
	}

	ctx := context.Background()
	model := c.genaiClient.GenerativeModel(c.modelName)

	// Установка параметров генерации
	model.SetTemperature(1.0)
	model.SetTopP(0.95)
	model.SetMaxOutputTokens(8192)

	// Подготовка сообщения
	sanitizedSystemPrompt := utils.SanitizeUTF8(systemPrompt)
	sanitizedContextText := utils.SanitizeUTF8(contextText)

	// Устанавливаем системный промпт
	if sanitizedSystemPrompt != "" {
		model.SystemInstruction = &genai.Content{
			Parts: []genai.Part{genai.Text(sanitizedSystemPrompt)},
		}
	}

	// Отправляем контекст для генерации
	resp, err := model.GenerateContent(ctx, genai.Text(sanitizedContextText))
	if err != nil {
		// Обрабатываем ошибку и проверяем необходимость переключения ключа
		handledErr := c.handleAPIError(err)

		// Если произошло переключение ключа, пробуем запрос снова
		if handledErr != nil && handledErr.Error() == "ключ API Gemini был переключен на резервный, повторите запрос" {
			// Получаем новую модель с обновленным клиентом
			model = c.genaiClient.GenerativeModel(c.modelName)

			// Применяем те же настройки к новой модели
			model.SetTemperature(1.0)
			model.SetTopP(0.95)
			model.SetMaxOutputTokens(8192)

			// Устанавливаем системный промпт для новой модели
			if sanitizedSystemPrompt != "" {
				model.SystemInstruction = &genai.Content{
					Parts: []genai.Part{genai.Text(sanitizedSystemPrompt)},
				}
			}

			// Повторяем запрос с новым ключом
			resp, err = model.GenerateContent(ctx, genai.Text(sanitizedContextText))
			if err != nil {
				// Если снова ошибка, возвращаем её
				return "", fmt.Errorf("ошибка Gemini API при генерации произвольного ответа (после переключения ключа): %w", err)
			}
		} else if handledErr != nil {
			// Если ошибка не связана с переключением ключа, возвращаем обработанную ошибку
			return "", fmt.Errorf("ошибка Gemini API при генерации произвольного ответа: %w", handledErr)
		}
	}

	// Извлекаем и возвращаем ответ
	var responseText strings.Builder
	if resp != nil && len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
		for _, part := range resp.Candidates[0].Content.Parts {
			responseText.WriteString(fmt.Sprintf("%s", part))
		}
	} else {
		return "", fmt.Errorf("Gemini не вернул валидный ответ")
	}

	finalResponse := responseText.String()
	if c.debug {
		log.Printf("[DEBUG] Gemini Ответ (Arbitrary): %s...", utils.TruncateString(finalResponse, 100))
	}

	return finalResponse, nil
}

// GenerateResponseFromTextContext генерирует ответ на основе предварительно отформатированного контекста
func (c *Client) GenerateResponseFromTextContext(systemPrompt string, contextText string) (string, error) {
	// Попробуем вернуться к основному ключу, если используется резервный и прошло достаточно времени
	if c.cfg.GeminiUsingReserveKey {
		if err := c.tryRevertToMainKey(); err != nil {
			log.Printf("[WARN] Не удалось вернуться к основному ключу Gemini: %v", err)
		}
	}

	ctx := context.Background()
	model := c.genaiClient.GenerativeModel(c.modelName)

	// Настройки модели
	model.SetTemperature(1)
	model.SetTopP(0.95)
	model.SetMaxOutputTokens(8192)

	// Устанавливаем SystemInstruction (новый формат)
	if systemPrompt != "" {
		model.SystemInstruction = &genai.Content{
			Parts: []genai.Part{genai.Text(utils.SanitizeUTF8(systemPrompt))},
		}
		if c.debug {
			log.Printf("[DEBUG] Gemini Text Запрос: Установлен SystemInstruction: %s...", utils.TruncateString(systemPrompt, 100))
		}
	}

	sanitizedContext := utils.SanitizeUTF8(contextText)
	if c.debug {
		log.Printf("[DEBUG] Gemini Text Запрос: Контекст: %s...", utils.TruncateString(sanitizedContext, 100))
	}

	// Создаем контент запроса с заранее отформатированным контекстом
	content := []genai.Part{genai.Text(sanitizedContext)}

	// Отправляем запрос
	resp, err := model.GenerateContent(ctx, content...)
	if err != nil {
		if c.debug {
			log.Printf("[DEBUG] Gemini Text Ошибка API: %v", err)
		}

		// Обрабатываем ошибку и проверяем необходимость переключения ключа
		handledErr := c.handleAPIError(err)

		// Если произошло переключение ключа, пробуем запрос снова
		if handledErr != nil && handledErr.Error() == "ключ API Gemini был переключен на резервный, повторите запрос" {
			// Получаем новую модель с обновленным клиентом
			model = c.genaiClient.GenerativeModel(c.modelName)

			// Применяем те же настройки к новой модели
			model.SetTemperature(1)
			model.SetTopP(0.95)
			model.SetMaxOutputTokens(8192)

			// Устанавливаем системный промпт для новой модели
			if systemPrompt != "" {
				model.SystemInstruction = &genai.Content{
					Parts: []genai.Part{genai.Text(utils.SanitizeUTF8(systemPrompt))},
				}
			}

			// Повторяем запрос с новым ключом
			resp, err = model.GenerateContent(ctx, content...)
			if err != nil {
				// Если снова ошибка, возвращаем её
				return "", fmt.Errorf("ошибка Gemini API при генерации ответа из текстового контекста (после переключения ключа): %w", err)
			}
		} else if handledErr != nil {
			// Если ошибка не связана с переключением ключа, возвращаем обработанную ошибку
			return "", fmt.Errorf("ошибка Gemini API при генерации ответа из текстового контекста: %w", handledErr)
		}
	}

	// Извлекаем ответ
	var responseText strings.Builder
	if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
		for _, part := range resp.Candidates[0].Content.Parts {
			responseText.WriteString(fmt.Sprintf("%s", part))
		}
	} else {
		if c.debug {
			log.Printf("[DEBUG] Gemini Text Ответ: Получен пустой ответ или нет кандидатов.")
		}
		return "", fmt.Errorf("Gemini API не вернул валидный ответ")
	}

	finalResponse := responseText.String()
	if c.debug {
		log.Printf("[DEBUG] Gemini Text Ответ: %s...", utils.TruncateString(finalResponse, 100))
	}

	return finalResponse, nil
}

// TranscribeAudio транскрибирует аудиоданные с помощью Gemini API
func (c *Client) TranscribeAudio(audioData []byte, mimeType string) (string, error) {
	// Попробуем вернуться к основному ключу, если используется резервный и прошло достаточно времени
	if c.cfg.GeminiUsingReserveKey {
		if err := c.tryRevertToMainKey(); err != nil {
			log.Printf("[WARN] Не удалось вернуться к основному ключу Gemini: %v", err)
		}
	}

	// Проверяем, поддерживает ли модель аудио (хотя бы по названию)
	if !strings.Contains(c.modelName, "1.5") && !strings.Contains(c.modelName, "flash") { // Обновил проверку, flash тоже должен работать
		log.Printf("[WARN][TranscribeAudio] Текущая модель '%s' может не поддерживать аудио. Для транскрибации рекомендуется Gemini 1.5/2.0 Flash/Pro.", c.modelName)
		// Можно либо вернуть ошибку, либо попытаться использовать 1.5 Flash по умолчанию
		// return "", fmt.Errorf("модель %s не поддерживает транскрибацию аудио", c.modelName)
	}

	ctx := context.Background()
	model := c.genaiClient.GenerativeModel(c.modelName) // Используем основную модель клиента

	if c.debug {
		log.Printf("[DEBUG][TranscribeAudio] Используется модель: %s, MIME-тип: %s, Размер данных: %d байт", c.modelName, mimeType, len(audioData))
		log.Printf("[DEBUG][TranscribeAudio PRE-CALL] Вызов GenerateContent с моделью: %s", c.modelName)
	}

	// Формируем запрос СОГЛАСНО ДОКУМЕНТАЦИИ для транскрипции:
	// Промпт для запроса транскрипции
	prompt := genai.Text("Транскрибируй текст аудио как есть") // Исправленный промпт
	// Аудиоданные как genai.Blob (возвращаемся к Blob)
	audioPart := genai.Blob{MIMEType: mimeType, Data: audioData}

	// Отправляем запрос только с промптом и аудио
	resp, err := model.GenerateContent(ctx, prompt, audioPart)
	if err != nil {
		if c.debug {
			log.Printf("[DEBUG][TranscribeAudio] Ошибка API Gemini: %v", err)
		}

		// Обрабатываем ошибку и проверяем необходимость переключения ключа
		handledErr := c.handleAPIError(err)

		// Если произошло переключение ключа, пробуем запрос снова
		if handledErr != nil && handledErr.Error() == "ключ API Gemini был переключен на резервный, повторите запрос" {
			// Получаем новую модель с обновленным клиентом
			model = c.genaiClient.GenerativeModel(c.modelName)

			// Повторяем запрос с новым ключом
			resp, err = model.GenerateContent(ctx, prompt, audioPart)
			if err != nil {
				// Если снова ошибка, возвращаем её
				return "", fmt.Errorf("ошибка транскрибации аудио в Gemini (после переключения ключа): %w", err)
			}
		} else if handledErr != nil {
			// Если ошибка не связана с переключением ключа, возвращаем обработанную ошибку
			return "", fmt.Errorf("ошибка транскрибации аудио в Gemini: %w", handledErr)
		}
	}

	// Извлекаем текст из ответа
	var transcript strings.Builder
	if resp != nil && len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
		for _, part := range resp.Candidates[0].Content.Parts {
			if textPart, ok := part.(genai.Text); ok {
				transcript.WriteString(string(textPart))
			}
		}
	} else {
		if c.debug {
			log.Println("[DEBUG][TranscribeAudio] Gemini не вернул валидный ответ или текст.")
		}
		// Возвращаем пустую строку, если транскрипции нет, но нет и ошибки API
		// Это может случиться, если аудио пустое или содержит только тишину/шум
		log.Printf("[WARN][TranscribeAudio] Gemini вернул пустой ответ без ошибки API. Аудио могло быть пустым.")
		return "", nil // Не считаем это ошибкой приложения
	}

	finalTranscript := transcript.String()
	if c.debug {
		log.Printf("[DEBUG][TranscribeAudio] Успешная транскрипция: %s...", utils.TruncateString(finalTranscript, 100))
	}

	return finalTranscript, nil
}

// EmbedContent генерирует векторное представление (эмбеддинг) для текста с использованием Gemini API.
func (c *Client) EmbedContent(text string) ([]float32, error) {
	// Попробуем вернуться к основному ключу, если используется резервный и прошло достаточно времени
	if c.cfg.GeminiUsingReserveKey {
		if err := c.tryRevertToMainKey(); err != nil {
			log.Printf("[WARN] Не удалось вернуться к основному ключу Gemini: %v", err)
		}
	}

	ctx := context.Background()
	em := c.genaiClient.EmbeddingModel(c.embeddingModelName) // Используем модель из конфига
	if em == nil {
		return nil, fmt.Errorf("модель эмбеддингов '%s' не найдена или не инициализирована", c.embeddingModelName)
	}

	sanitizedText := utils.SanitizeUTF8(text)

	if c.debug {
		log.Printf("[DEBUG] Gemini Embed Запрос: Модель %s, Текст: %s...", c.embeddingModelName, utils.TruncateString(sanitizedText, 50))
	}

	res, err := em.EmbedContent(ctx, genai.Text(sanitizedText))
	if err != nil {
		if c.debug {
			log.Printf("[DEBUG] Gemini Embed Ошибка API: %v", err)
		}

		// Обрабатываем ошибку и проверяем необходимость переключения ключа
		handledErr := c.handleAPIError(err)

		// Если произошло переключение ключа, пробуем запрос снова
		if handledErr != nil && handledErr.Error() == "ключ API Gemini был переключен на резервный, повторите запрос" {
			// Получаем новую модель эмбеддингов с обновленным клиентом
			em = c.genaiClient.EmbeddingModel(c.embeddingModelName)
			if em == nil {
				return nil, fmt.Errorf("модель эмбеддингов '%s' не найдена после переключения ключа", c.embeddingModelName)
			}

			// Повторяем запрос с новым ключом
			res, err = em.EmbedContent(ctx, genai.Text(sanitizedText))
			if err != nil {
				// Если снова ошибка, возвращаем её
				return nil, fmt.Errorf("ошибка API Gemini при генерации эмбеддинга (после переключения ключа): %w", err)
			}
		} else if handledErr != nil {
			// Если ошибка не связана с переключением ключа, возвращаем обработанную ошибку
			return nil, handledErr
		}
	}

	if res.Embedding == nil || len(res.Embedding.Values) == 0 {
		if c.debug {
			log.Printf("[DEBUG] Gemini Embed Ответ: Получен пустой эмбеддинг.")
		}
		return nil, fmt.Errorf("API Gemini вернул пустой эмбеддинг")
	}

	if c.debug {
		log.Printf("[DEBUG] Gemini Embed Ответ: Получен эмбеддинг размерности %d", len(res.Embedding.Values))
	}

	return res.Embedding.Values, nil
}

// GenerateContentWithImage генерирует ответ на основе изображения и текстового промпта
func (c *Client) GenerateContentWithImage(ctx context.Context, systemPrompt string, imageData []byte, caption string) (string, error) {
	// Попробуем вернуться к основному ключу, если используется резервный и прошло достаточно времени
	if c.cfg.GeminiUsingReserveKey {
		if err := c.tryRevertToMainKey(); err != nil {
			log.Printf("[WARN] Не удалось вернуться к основному ключу Gemini: %v", err)
		}
	}

	model := c.genaiClient.GenerativeModel(c.modelName)

	// Настройки модели
	model.SetTemperature(1)
	model.SetTopP(0.95)
	model.SetMaxOutputTokens(8192)

	// Устанавливаем системный промпт
	if systemPrompt != "" {
		model.SystemInstruction = &genai.Content{
			Parts: []genai.Part{genai.Text(utils.SanitizeUTF8(systemPrompt))},
		}
		if c.debug {
			log.Printf("[DEBUG] Gemini Image Запрос: Установлен SystemInstruction: %s...", utils.TruncateString(systemPrompt, 100))
		}
	}

	// Определяем MIME тип на основе начальных байтов изображения
	mimeType := detectMimeType(imageData)
	if mimeType == "" {
		mimeType = "image/jpeg" // По умолчанию
	}

	// Создаем части запроса: текст и изображение
	var parts []genai.Part

	// Сначала добавляем текст (если есть)
	if caption != "" {
		parts = append(parts, genai.Text(utils.SanitizeUTF8(caption)))
	}

	// Добавляем изображение
	parts = append(parts, genai.Blob{
		MIMEType: mimeType,
		Data:     imageData,
	})

	if c.debug {
		log.Printf("[DEBUG] Gemini Image Запрос: MIME: %s, Caption: %s, Image Size: %d bytes",
			mimeType, utils.TruncateString(caption, 50), len(imageData))
	}

	// Отправляем запрос
	resp, err := model.GenerateContent(ctx, parts...)
	if err != nil {
		if c.debug {
			log.Printf("[DEBUG] Gemini Image Ошибка: %v", err)
		}

		// Обрабатываем ошибку и проверяем необходимость переключения ключа
		handledErr := c.handleAPIError(err)

		// Если произошло переключение ключа, пробуем запрос снова
		if handledErr != nil && handledErr.Error() == "ключ API Gemini был переключен на резервный, повторите запрос" {
			// Получаем новую модель с обновленным клиентом
			model = c.genaiClient.GenerativeModel(c.modelName)

			// Применяем те же настройки к новой модели
			model.SetTemperature(1)
			model.SetTopP(0.95)
			model.SetMaxOutputTokens(8192)

			// Устанавливаем системный промпт для новой модели
			if systemPrompt != "" {
				model.SystemInstruction = &genai.Content{
					Parts: []genai.Part{genai.Text(utils.SanitizeUTF8(systemPrompt))},
				}
			}

			// Повторяем запрос с новым ключом
			resp, err = model.GenerateContent(ctx, parts...)
			if err != nil {
				// Если снова ошибка, возвращаем её
				return "", fmt.Errorf("ошибка генерации анализа изображения (после переключения ключа): %w", err)
			}
		} else if handledErr != nil {
			// Если ошибка не связана с переключением ключа, возвращаем обработанную ошибку
			return "", fmt.Errorf("ошибка генерации анализа изображения: %w", handledErr)
		}
	}

	// Извлекаем ответ
	var responseText strings.Builder
	if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
		for _, part := range resp.Candidates[0].Content.Parts {
			if textPart, ok := part.(genai.Text); ok {
				responseText.WriteString(string(textPart))
			}
		}
	} else {
		if c.debug {
			log.Printf("[DEBUG] Gemini Image Ответ: Получен пустой ответ или нет кандидатов.")
		}
		return "", fmt.Errorf("Gemini не вернул валидный ответ для изображения")
	}

	finalResponse := responseText.String()
	if c.debug {
		log.Printf("[DEBUG] Gemini Image Ответ: %s...", utils.TruncateString(finalResponse, 100))
	}

	return finalResponse, nil
}

// detectMimeType определяет MIME-тип изображения на основе его заголовка (magic bytes)
func detectMimeType(data []byte) string {
	if len(data) < 12 {
		return ""
	}

	// Определяем по первым байтам
	if data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "image/jpeg"
	} else if data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G' {
		return "image/png"
	} else if data[0] == 'G' && data[1] == 'I' && data[2] == 'F' {
		return "image/gif"
	} else if data[0] == 'R' && data[1] == 'I' && data[2] == 'F' && data[3] == 'F' &&
		data[8] == 'W' && data[9] == 'E' && data[10] == 'B' && data[11] == 'P' {
		return "image/webp"
	}

	return ""
}

// switchToReserveKey переключается на резервный ключ API
func (c *Client) switchToReserveKey() error {
	c.keyMutex.Lock()
	defer c.keyMutex.Unlock()

	// Проверяем, что у нас есть резервный ключ и мы еще не используем его
	if c.cfg.GeminiAPIKeyReserve == "" {
		return fmt.Errorf("резервный ключ API Gemini не предоставлен")
	}

	if c.cfg.GeminiUsingReserveKey {
		return nil // Уже используем резервный ключ
	}

	// Закрываем текущий клиент, если он существует
	if c.genaiClient != nil {
		if err := c.genaiClient.Close(); err != nil {
			log.Printf("[WARN] Ошибка при закрытии текущего клиента Gemini: %v", err)
		}
	}

	// Создаем новый клиент с резервным ключом
	ctx := context.Background()
	newClient, err := genai.NewClient(ctx, option.WithAPIKey(c.cfg.GeminiAPIKeyReserve))
	if err != nil {
		return fmt.Errorf("ошибка создания клиента genai с резервным ключом: %w", err)
	}

	// Обновляем клиент и флаг
	c.genaiClient = newClient
	c.cfg.GeminiUsingReserveKey = true
	c.cfg.GeminiLastKeyRotationTime = time.Now()

	log.Printf("[INFO] Gemini: Переключение на резервный ключ API выполнено. Следующая попытка основного ключа через %d часов.", c.cfg.GeminiKeyRotationTimeHours)

	return nil
}

// tryRevertToMainKey пытается вернуться к основному ключу API, если прошло достаточно времени
func (c *Client) tryRevertToMainKey() error {
	c.keyMutex.Lock()
	defer c.keyMutex.Unlock()

	// Если мы не используем резервный ключ, ничего делать не нужно
	if !c.cfg.GeminiUsingReserveKey {
		return nil
	}

	// Проверяем, прошло ли достаточно времени с последнего переключения
	timeSinceRotation := time.Since(c.cfg.GeminiLastKeyRotationTime)
	if timeSinceRotation < time.Duration(c.cfg.GeminiKeyRotationTimeHours)*time.Hour {
		return nil // Еще рано для переключения обратно
	}

	// Закрываем текущий клиент, если он существует
	if c.genaiClient != nil {
		if err := c.genaiClient.Close(); err != nil {
			log.Printf("[WARN] Ошибка при закрытии текущего клиента Gemini: %v", err)
		}
	}

	// Создаем новый клиент с основным ключом
	ctx := context.Background()
	newClient, err := genai.NewClient(ctx, option.WithAPIKey(c.cfg.GeminiAPIKey))
	if err != nil {
		// Если не удалось создать клиент с основным ключом, остаемся на резервном
		log.Printf("[WARN] Не удалось вернуться к основному ключу API: %v. Продолжаем использовать резервный.", err)

		// Пробуем восстановить клиент с резервным ключом
		reserveClient, reserveErr := genai.NewClient(ctx, option.WithAPIKey(c.cfg.GeminiAPIKeyReserve))
		if reserveErr != nil {
			return fmt.Errorf("критическая ошибка: не удалось создать клиента ни с основным, ни с резервным ключом: %w", reserveErr)
		}
		c.genaiClient = reserveClient
		c.cfg.GeminiLastKeyRotationTime = time.Now() // Обновляем время последнего переключения

		return err
	}

	// Обновляем клиент и флаг
	c.genaiClient = newClient
	c.cfg.GeminiUsingReserveKey = false
	c.cfg.GeminiLastKeyRotationTime = time.Time{} // Сбрасываем время последнего переключения

	log.Printf("[INFO] Gemini: Успешное возвращение к основному ключу API.")

	return nil
}

// handleAPIError обрабатывает ошибки API и переключается на резервный ключ при необходимости
func (c *Client) handleAPIError(err error) error {
	if err == nil {
		return nil
	}

	// Проверяем, связана ли ошибка с квотой
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		// Если ошибка связана с квотой (429 Too Many Requests)
		if gerr.Code == 429 {
			log.Printf("[WARN] Gemini API: Достигнут лимит запросов (429 Too Many Requests). Пробуем переключиться на резервный ключ.")

			// Проверяем, что у нас есть резервный ключ и мы еще не используем его
			if c.cfg.GeminiAPIKeyReserve != "" && !c.cfg.GeminiUsingReserveKey {
				if switchErr := c.switchToReserveKey(); switchErr != nil {
					log.Printf("[ERROR] Не удалось переключиться на резервный ключ Gemini: %v", switchErr)
					// Возвращаем оригинальную ошибку
					return fmt.Errorf("ошибка API Gemini (лимит запросов): %w", err)
				}
				// Ключ успешно переключен, возвращаем специальную ошибку для повторного запроса
				return fmt.Errorf("ключ API Gemini был переключен на резервный, повторите запрос")
			}
		}
	}

	// Для других типов ошибок или если нет резервного ключа, просто возвращаем исходную ошибку
	return err
}
