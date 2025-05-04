# История изменений проекта Rofloslav

Этот файл отслеживает основные изменения, внесенные в проект.

## Этап 1: Добавление модуля модерации (Начало)

*   **Дата:** [Текущая дата]
*   **Изменения:**
    *   **Конфигурация (`internal/config/`):**
        *   `types.go`: Добавлены:
            *   Перечисление `PunishmentType` (`mute`, `kick`, `ban`, `purge`, `none`).
            *   Структура `ModerationRule` для хранения правил.
            *   Поля `ModInterval`, `ModMuteTimeMin`, `ModBanTimeMin`, `ModPurgeDuration`, `ModCheckAdminRights`, `ModDefaultNotify`, `ModRules` в структуру `Config`.
        *   `load.go`: Добавлена загрузка новых переменных окружения (`MOD_*`), реализован парсинг JSON-строки `MOD_RULES` в срез `[]ModerationRule`.
        *   `validate.go`: Добавлена валидация новых полей конфигурации модерации и проверка корректности загруженных правил.
        *   `log.go`: Добавлено логирование новых параметров конфигурации модерации при старте бота.
    *   **Исправления:** Устранены ошибки линтера в `internal/config/load.go` и `internal/config/log.go`.
    *   **Модерация (`internal/bot/moderation.go`):**
        *   Создан файл и структура `ModerationService`.
        *   Реализован конструктор `NewModerationService`.
        *   Реализован метод `CheckAdminRightsAndActivate` для проверки прав админа и активации/деактивации модерации в чате.
        *   Реализован метод `ProcessIncomingMessage` для сбора сообщений в буфер и запуска обработки пакета.
        *   Реализована функция `matchKeywords` для проверки ключевых слов в тексте.
        *   Реализована функция `formatContextForLLM` для подготовки контекста для LLM.
        *   Реализована основная логика `processMessageBatch` (проверка правил, ключевых слов, вызов LLM).
        *   Реализован метод `applyPunishment` для применения наказаний (mute, kick, ban, purge, none).
        *   Реализован метод `purgeUserMessages` для асинхронного удаления сообщений.
        *   Реализован метод `StopPurge` для отмены активной задачи очистки.
    *   **Интеграция (`internal/bot/`):**
        *   `bot.go`: Добавлен сервис модерации в структуру `Bot`, инициализирован, добавлен вызов `CheckAdminRightsAndActivate` при изменении статуса бота.
        *   `message_handler.go`: Добавлен вызов `moderation.ProcessIncomingMessage` для обработки входящих сообщений.
        *   `telegram_helpers.go`: Добавлены функции `getBotMember` и `sendAutoDeleteMessage`.
        *   `command_handler.go`: Добавлена команда `/stop_purge` для администраторов.
    *   **Исправления:** Устранена ошибка линтера (`unterminated string literal`) в `internal/bot/moderation.go`.
    *   **Обновления поля очистки сообщений:**
        *   В `internal/config/types.go` переименовано поле `ModPurgeTimeMin` в `ModPurgeDuration` (тип `time.Duration`) для поддержки строкового формата длительности (например, "30s", "1m").
        *   В `internal/config/load.go` изменён парсинг `MOD_PURGE_TIME_MIN` на `time.ParseDuration`, поддерживающий суффиксы `s`, `m`.
        *   В `internal/config/validate.go` валидация обновлена для поля `ModPurgeDuration`.
        *   В `internal/config/log.go` логирование заменено на вывод `ModPurgeDuration`.
        *   В `internal/bot/moderation.go` при обработке правила PURGE используется новое поле `ModPurgeDuration` вместо вычисления из минут.
    *   **Исправления планировщика:**
        *   В `internal/bot/scheduler.go` заменён `log.Println` на `log.Printf` для корректного форматирования сообщений с параметром `%d`.

## Docker

В `Dockerfile` для локального тестирования мы копируем оба файла конфигурации:
```dockerfile
# Runtime Stage
COPY .env ./
# Копируем секреты локально для разработки
COPY .env.secrets ./
```
- Файл `.env.secrets` не хранится в репозитории и не включается в образ при деплое на Timeweb Cloud.
- В продакшн-окружении (Timeweb Cloud) секретные переменные передаются через встроенные настройки площадки, а не файлы. 