package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

func main() {
	token := requireEnv("TELEGRAM_BOT_TOKEN")
	chatID := requireEnv("TELEGRAM_CHAT_ID")
	apiKey := requireEnv("SIGNAL_API_KEY")
	host := os.Getenv("SIGNAL_HOST")
	if host == "" {
		host = "http://localhost:8080"
	}
	host = strings.TrimRight(host, "/")
	channel := os.Getenv("SIGNAL_CHANNEL")
	if channel == "" {
		channel = "alerts"
	}

	telegramURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	wsURL := strings.Replace(host, "http", "ws", 1) + "/ws?channel=" + url.QueryEscape(channel)

	for {
		log.Printf("connecting to %s (channel: %s)", host, channel)
		err := stream(wsURL, apiKey, telegramURL, chatID)
		log.Printf("disconnected: %v — reconnecting in 3s", err)
		time.Sleep(3 * time.Second)
	}
}

func stream(wsURL, apiKey, telegramURL, chatID string) error {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+apiKey)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	log.Println("connected — watching for alerts")

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		var ev struct {
			Type      string `json:"type"`
			Author    string `json:"author"`
			Text      string `json:"text"`
			Timestamp int64  `json:"timestamp"`
		}
		if err := json.Unmarshal(msg, &ev); err != nil {
			log.Printf("bad message: %s", msg)
			continue
		}

		// only forward alerts
		if ev.Type != "alert" {
			continue
		}

		ts := time.UnixMilli(ev.Timestamp).Format("15:04:05")
		text := fmt.Sprintf("🚨 ALERT [%s]\n%s", ts, ev.Text)
		if ev.Author != "" {
			text = fmt.Sprintf("🚨 ALERT [%s] from %s\n%s", ts, ev.Author, ev.Text)
		}

		if err := sendTelegram(telegramURL, chatID, text); err != nil {
			log.Printf("telegram send failed: %v", err)
		} else {
			log.Printf("alert forwarded: %s", ev.Text)
		}
	}
}

func sendTelegram(apiURL, chatID, text string) error {
	body, _ := json.Marshal(map[string]string{
		"chat_id": chatID,
		"text":    text,
	})
	resp, err := http.Post(apiURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram returned %d", resp.StatusCode)
	}
	return nil
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("%s is required", key)
	}
	return v
}
