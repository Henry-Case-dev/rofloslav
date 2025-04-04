# ---- Build Stage ----
# Используем официальный образ Go 1.23 на Alpine для сборки (соответствует go.mod)
FROM golang:1.23-alpine AS builder

# Устанавливаем рабочую директорию внутри контейнера
WORKDIR /

# Копируем файлы модулей Go
COPY go.mod go.sum ./

# Загружаем зависимости
# Используем go mod download для кеширования, если зависимости не менялись
RUN echo "--- Running go mod download ---" && \
    go mod download -x
RUN echo "--- Go mod download finished ---"

# Копируем остальной исходный код
COPY . .

RUN echo "--- Listing files before build ---"
# RUN ls -lR / # Показываем структуру файлов в корне (ЗАКОММЕНТИРОВАНО - слишком долгий вывод)
RUN echo "--- Running go build ---"
# Собираем приложение
# CGO_ENABLED=0 - для статической линковки, важно для Alpine
# -ldflags="-w -s" - уменьшает размер бинарника
# -v - verbose output
# Выходной файл будет называться 'main'
# Используем флаг -mod=vendor, чтобы брать зависимости из папки vendor/
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -v -ldflags="-w -s" -o main .
RUN echo "--- Go build finished ---"

# ---- Runtime Stage ----
# Используем конкретную версию Alpine для предсказуемости
FROM alpine:3.19

# Устанавливаем рабочую директорию
WORKDIR /

# Копируем скомпилированный бинарник из стадии сборки
COPY --from=builder /main .

# Копируем файл .env из контекста сборки в финальный образ
# Убедитесь, что .env НЕ содержит секретов!
COPY .env ./

# Устанавливаем данные часовых поясов (необходимо для TIME_ZONE=Asia/Yekaterinburg)
RUN apk add --no-cache tzdata

# Открываем порт 80, который слушает наш HTTP-сервер
EXPOSE 80

# Команда для запуска приложения при старте контейнера
CMD ["./main"] 