package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/Henry-Case-dev/rofloslav/internal/bot"
	"github.com/Henry-Case-dev/rofloslav/internal/config"
	"github.com/Henry-Case-dev/rofloslav/internal/gemini"
	"github.com/Henry-Case-dev/rofloslav/internal/storage"
)

// handleRoot - простой обработчик HTTP запросов
func handleRoot(w http.ResponseWriter, r *http.Request) {
	log.Printf("Received HTTP request: %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
	fmt.Fprintf(w, "Hello from Rofloslav Bot server!")
}

func main() {
	log.Println("=== Application Starting ===")
	log.Printf("Timestamp: %s", time.Now().UTC().Format(time.RFC3339))

	cfg, err := config.LoadConfig()
	if err != nil {
		log.Printf("!!! FATAL: Ошибка загрузки конфигурации: %v", err)
		time.Sleep(15 * time.Second)
		panic(fmt.Sprintf("Configuration error: %v", err))
	}
	log.Println("--- Configuration Loaded ---")

	// --- Инициализация клиентов и хранилищ ---
	ctx := context.Background()

	// Инициализация клиента Gemini
	geminiClient, err := gemini.NewClient(ctx, cfg.GeminiAPIKey, cfg.GeminiModelName, cfg.GeminiEmbeddingModelName)
	if err != nil {
		log.Printf("!!! FATAL: Ошибка инициализации клиента Gemini: %v", err)
		time.Sleep(15 * time.Second)
		panic(fmt.Sprintf("Gemini client initialization error: %v", err))
	}
	defer geminiClient.Close() // Закрываем клиент при завершении main
	log.Println("--- Gemini Client Initialized ---")

	// Инициализация основного хранилища
	primaryStorage, err := storage.NewHistoryStorage(cfg, geminiClient)
	if err != nil {
		log.Printf("!!! FATAL: Ошибка инициализации основного хранилища: %v", err)
		time.Sleep(15 * time.Second)
		panic(fmt.Sprintf("Primary storage initialization error: %v", err))
	}
	log.Println("--- Primary Storage Initialized ---")

	// Инициализация локального хранилища для саммари
	localHistoryStorage, err := storage.NewLocalStorage(cfg.ContextWindow)
	if err != nil {
		// Ошибка локального хранилища не фатальна, но логируем
		log.Printf("!!! WARNING: Ошибка инициализации локального хранилища: %v", err)
		// Можно продолжить без локального хранилища, установив его в nil
		localHistoryStorage = nil
	}
	log.Println("--- Local Summary Storage Initialized ---")

	// Инициализация бота
	botInstance, err := bot.NewBot(cfg, geminiClient, primaryStorage, localHistoryStorage)
	if err != nil {
		log.Printf("!!! FATAL: Ошибка создания бота: %v", err)
		time.Sleep(15 * time.Second)
		panic(fmt.Sprintf("Bot creation error: %v", err))
	}
	log.Println("--- Bot Initialized ---")

	// Запускаем бота в отдельной горутине
	go func() {
		botInstance.Run()
	}()
	log.Println("--- Bot Run Goroutine Launched ---")
	log.Println("Бот запущен.")

	// --- Запуск Dummy HTTP сервера ---
	http.HandleFunc("/", handleRoot) // Регистрируем обработчик
	serverAddr := ":80"              // Порт из amvera.yml
	log.Printf("--- Starting HTTP server on %s ---", serverAddr)

	go func() {
		if httpErr := http.ListenAndServe(serverAddr, nil); httpErr != nil {
			log.Printf("!!! HTTP Server Error: %v", httpErr)
		}
	}()
	// Добавляем лог сразу после запуска горутины
	log.Printf("--- HTTP Server Goroutine Launched on %s ---", serverAddr)
	// --- Конец HTTP сервера ---

	log.Printf("--- Application Ready. Waiting indefinitely. ---")

	// Ожидаем бесконечно, игнорируем сигналы завершения.
	// Это нужно для Amvera, чтобы контейнер оставался RUNNING.
	select {}

	// Этот код больше никогда не будет выполнен в Amvera.
	// Оставляем его закомментированным на случай локальных тестов.
	/*
		log.Println("Остановка бота (из main)...") // Добавляем лог для ясности
		bot.Stop()
		log.Println("Приложение остановлено")
	*/
}
