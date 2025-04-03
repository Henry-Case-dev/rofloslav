# ---- Build Stage ----
# Используем официальный образ Go 1.22 на Alpine для сборки
FROM golang:1.22-alpine AS builder

# Устанавливаем рабочую директорию внутри контейнера
WORKDIR /

# Копируем файлы модулей Go
COPY go.mod go.sum ./

# Загружаем зависимости
# Используем go mod download для кеширования, если зависимости не менялись
RUN go mod download

# Копируем остальной исходный код
COPY . .

# Собираем приложение
# CGO_ENABLED=0 - для статической линковки, важно для Alpine
# -ldflags="-w -s" - уменьшает размер бинарника
# Выходной файл будет называться 'main'
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o main .

# ---- Runtime Stage ----
# Используем конкретную версию Alpine для предсказуемости
FROM alpine:3.19

# Устанавливаем рабочую директорию
WORKDIR /

# Копируем скомпилированный бинарник из стадии сборки
COPY --from=builder /main .

# Устанавливаем данные часовых поясов (необходимо для TIME_ZONE=Asia/Yekaterinburg)
RUN apk add --no-cache tzdata

# Открываем порт 80, который слушает наш HTTP-сервер
EXPOSE 80

# Команда для запуска приложения при старте контейнера
CMD ["./main"] 