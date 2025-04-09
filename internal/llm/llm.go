package llm

import (
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// LLMClient определяет общий интерфейс для взаимодействия с различными LLM.
type LLMClient interface {
	// GenerateResponse генерирует ответ на основе истории сообщений и системного промпта.
	// history - это сообщения ДО текущего lastMessage.
	// lastMessage - это последнее сообщение пользователя, на которое нужно сгенерировать ответ.
	GenerateResponse(systemPrompt string, history []*tgbotapi.Message, lastMessage *tgbotapi.Message) (string, error)

	// GenerateArbitraryResponse генерирует ответ на основе системного промпта и произвольного текстового контекста.
	// Используется для задач, не требующих истории чата (например, анализ срача).
	GenerateArbitraryResponse(systemPrompt string, contextText string) (string, error)

	// Close освобождает ресурсы, связанные с клиентом (если необходимо).
	Close() error
}
