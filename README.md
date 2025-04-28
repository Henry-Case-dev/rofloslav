# Rofloslav Bot - Остроумный Telegram-бот на Go и Gemini

Это Telegram-бот, написанный на Go, который использует Google Gemini API для генерации ответов в групповых чатах. Бот спроектирован быть остроумным, ироничным и адаптивным к стилю общения участников чата.

## 🚀 Основные возможности

*   **Участие в беседе:** Бот анализирует историю сообщений и отвечает в соответствии с заданным "характером" (основной промпт в `.env`).
*   **Прямые ответы:** Реагирует на упоминания (@botname) или ответы на свои сообщения в саркастической манере (промпт `DIRECT_PROMPT`).
*   **Тема дня:** Генерирует ежедневную провокационную тему для обсуждения (промпт `DAILY_TAKE_PROMPT`, настраиваемое время).
*   **Саммари чата:** Создает краткое изложение диалога за последние 24 часа по команде `/summary` или автоматически с заданным интервалом (промпт `SUMMARY_PROMPT`).
*   **Анализ "срачей":**
    *   Обнаруживает начало споров/конфликтов по ключевым словам, ответам или упоминаниям (промпт `SRACH_WARNING_PROMPT`).
    *   Пытается проанализировать завершившийся спор, выделить стороны, аргументы и вынести вердикт (промпт `SRACH_ANALYSIS_PROMPT`).
    *   Использует LLM для дополнительной проверки сообщений на принадлежность к конфликту (промпт `SRACH_CONFIRM_PROMPT`).
*   **Настройка:** Позволяет настраивать поведение через команды `/settings` (интервалы ответов, время темы дня, интервал саммари, включение/выключение анализа срачей).
*   **Сохранение истории:** Сохраняет историю сообщений в файл (`/data/chat_<ID>.json`) для поддержания контекста между перезапусками.

## 🛠 Технологический стек

*   **Язык:** Go (Golang)
*   **AI Модель:** Google Gemini API (через `google/generative-ai-go`) или DeepSeek API (через `sashabaranov/go-openai`)
*   **Telegram API:** Библиотека `go-telegram-bot-api/v5`
*   **Хранилище истории и профилей:**
    *   MongoDB (через `go.mongodb.org/mongo-driver`) (Рекомендуется)
    *   PostgreSQL (через `lib/pq`)
    *   Файлы JSON (`/data/chat_<ID>.json`) (Не рекомендуется для профилей)
*   **Конфигурация:** Файлы `.env` и `.env.secrets`, переменные окружения
*   **Развертывание:** Docker (ориентировано на платформу Amvera)

## ⚙️ Конфигурация

Основные настройки и промпты для AI задаются в файле `.env`.
Секретные токены (`TELEGRAM_TOKEN`, `GEMINI_API_KEY`, `DEEPSEEK_API_KEY`, пароли от БД, строка подключения MongoDB) следует помещать в файл `.env.secrets`. Этот файл добавлен в `.gitignore` и не должен попадать в репозиторий.

Приложение сначала загружает переменные из `.env.secrets`, затем из `.env`, и в последнюю очередь из переменных окружения системы. Это позволяет легко переопределять настройки при развертывании.

Ключевые переменные:

*   `TELEGRAM_TOKEN`: Токен вашего Telegram бота.
*   `LLM_PROVIDER`: Выбор AI провайдера (`gemini` или `deepseek`).
*   `GEMINI_API_KEY`: Ключ доступа к Google Gemini API (если `LLM_PROVIDER=gemini`).
*   `GEMINI_MODEL_NAME`: Используемая модель Gemini (например, `gemini-1.5-flash-latest`).
*   `DEEPSEEK_API_KEY`: Ключ доступа к DeepSeek API (если `LLM_PROVIDER=deepseek`).
*   `DEEPSEEK_MODEL_NAME`: Используемая модель DeepSeek (например, `deepseek-chat`).
*   `STORAGE_TYPE`: Тип хранилища (`file`, `postgres` или `mongo`). По умолчанию `mongo`.
*   `POSTGRESQL_HOST`, `POSTGRESQL_PORT`, `POSTGRESQL_USER`, `POSTGRESQL_PASSWORD`, `POSTGRESQL_DBNAME`: Параметры подключения к PostgreSQL (если `STORAGE_TYPE=postgres`).
*   `MONGODB_URI`: Строка подключения к MongoDB (если `STORAGE_TYPE=mongo`).
*   `CONTEXT_WINDOW`: Максимальное количество сообщений, хранимых в контексте для каждого чата.
*   `TIME_ZONE`: Часовой пояс для ежедневных задач (например, `Asia/Yekaterinburg`).
*   `DEBUG`: Включение/выключение режима отладки (`true`/`false`).
*   Промпты (`DEFAULT_PROMPT`, `DIRECT_PROMPT`, `DAILY_TAKE_PROMPT`, `SUMMARY_PROMPT`, `SRACH_*_PROMPT`): Определяют поведение AI в различных ситуациях.
*   `ADMIN_USERNAMES`: Список имен пользователей Telegram (без @), которые являются администраторами бота (через запятую).

**Команда `/profile_set` (только для администраторов):**
Позволяет вручную добавить или обновить профиль пользователя в базе данных.
Формат ввода в следующем сообщении после команды:
`@username - Прозвище - Пол (male/female/other) - Настоящее имя - Био`
*   `@username`: Обязательно.
*   `Прозвище`: Обязательно.
*   `Пол`: Необязательно (male, female, other, m, f).
*   `Настоящее имя`: Необязательно.
*   `Био`: Необязательно (может содержать символы `-`)

## ▶️ Запуск

Проект предназначен для запуска в Docker-контейнере, в частности на платформе Amvera.

1.  Создайте файлы `.env` и `.env.secrets` с вашими настройками и токенами.
2.  Соберите Docker-образ: `docker build -t rofloslav-bot .`
3.  Запустите контейнер. При использовании Amvera, конфигурация запуска определяется в `amvera.yml`. Локально можно запустить примерно так:
    ```bash
    # Убедитесь, что создана директория data для монтирования
    mkdir data
    docker run -d --env-file .env --env-file .env.secrets -p 8080:80 -v ./data:/data --name rofloslav rofloslav-bot
    ```

## 💡 Возможные улучшения

*   Более тонкая настройка ролей при формировании истории для Gemini/DeepSeek.
*   Динамическое обновление планировщиков (тейк, саммари) без перезапуска бота.
*   Улучшение алгоритма детекции и анализа срачей.
*   Добавление большего количества команд и настроек (например, управление профилями через меню).
*   Рефакторинг и оптимизация кода.

## Параметры конфигурации

Конфигурация загружается из переменных окружения или из файла `.env`. Основные параметры:

### Основные настройки

- `TELEGRAM_TOKEN` - токен Telegram бота
- `LLM_PROVIDER` - провайдер LLM: `Gemini`, `DeepSeek` или `OpenRouter`
- `DEFAULT_PROMPT` - промпт по умолчанию
- `DIRECT_PROMPT` - промпт для прямых обращений
- `DAILY_TAKE_PROMPT` - промпт для ежедневного тейка
- `SUMMARY_PROMPT` - промпт для саммари диалога
- `RATE_LIMIT_ERROR_MESSAGE` - сообщение об ошибке при превышении лимита

### Настройки времени и интервалов

- `TIME_ZONE` - часовой пояс (например, `Europe/Moscow`)
- `DAILY_TAKE_TIME` - час дня для отправки ежедневного тейка (0-23)
- `MIN_MESSAGES` - минимальное количество сообщений перед ответом
- `MAX_MESSAGES` - максимальное количество сообщений перед ответом
- `CONTEXT_WINDOW` - размер контекстного окна (в токенах)
- `SUMMARY_INTERVAL_HOURS` - интервал автоматического саммари в часах

### Настройки донатов

- `DONATE_PROMPT` - промпт для генерации сообщения о донате
- `DONATE_TIME_HOURS` - интервал отправки сообщений о донате в часах

### Настройки Gemini

- `GEMINI_API_KEY` - API ключ Gemini
- `GEMINI_MODEL_NAME` - название модели Gemini

### Настройки DeepSeek

- `DEEPSEEK_API_KEY` - API ключ DeepSeek
- `DEEPSEEK_MODEL_NAME` - название модели DeepSeek
- `DEEPSEEK_BASE_URL` - базовый URL для DeepSeek (опционально)

### Настройки OpenRouter

- `OPENROUTER_API_KEY` - API ключ OpenRouter
- `OPENROUTER_MODEL_NAME` - название модели OpenRouter
- `OPENROUTER_SITE_URL` - URL сайта для HTTP-Referer (опционально)
- `OPENROUTER_SITE_TITLE` - Заголовок сайта для X-Title (опционально)

### Настройки хранилища

- `STORAGE_TYPE` - тип хранилища: `file`, `postgres` или `mongo`

#### Настройки PostgreSQL

- `POSTGRESQL_HOST` - хост PostgreSQL
- `POSTGRESQL_PORT` - порт PostgreSQL
- `POSTGRESQL_USER` - пользователь PostgreSQL
- `POSTGRESQL_PASSWORD` - пароль PostgreSQL
- `POSTGRESQL_DBNAME` - имя базы данных PostgreSQL

#### Настройки MongoDB

- `MONGODB_URI` - URI подключения к MongoDB
- `MONGODB_DBNAME` - имя базы данных MongoDB
- `MONGODB_MESSAGES_COLLECTION` - коллекция сообщений
- `MONGODB_USER_PROFILES_COLLECTION` - коллекция профилей пользователей
- `MONGODB_SETTINGS_COLLECTION` - коллекция настроек

## Сборка и запуск

```bash
go build
./rofloslav
```

Или с использованием Docker:

```bash
docker build -t rofloslav .
docker run -p 80:80 --env-file .env rofloslav
```