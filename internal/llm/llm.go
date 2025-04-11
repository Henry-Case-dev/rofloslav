package llm

import (
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// LLMClient определяет общий интерфейс для взаимодействия с различными LLM.
type LLMClient interface {
	// GenerateResponse генерирует ответ на основе истории сообщений и системного промпта.
	// history - это сообщения ДО текущего lastMessage.
	// lastMessage - это последнее сообщение пользователя, на которое нужно сгенерировать ответ.
	// DEPRECATED: Используйте GenerateResponseFromTextContext для включения профилей.
	GenerateResponse(systemPrompt string, history []*tgbotapi.Message, lastMessage *tgbotapi.Message) (string, error)

	// GenerateResponseFromTextContext генерирует ответ на основе системного промпта и предварительно отформатированного текстового контекста.
	// contextText должен содержать всю необходимую информацию, включая историю сообщений и данные профилей.
	GenerateResponseFromTextContext(systemPrompt string, contextText string) (string, error)

	// GenerateArbitraryResponse генерирует ответ на основе системного промпта и произвольного текстового контекста.
	// Используется для задач, не требующих истории чата (например, анализ срача, саммари без профилей).
	GenerateArbitraryResponse(systemPrompt string, contextText string) (string, error)

	// Close освобождает ресурсы, связанные с клиентом (если необходимо).
	Close() error
}
