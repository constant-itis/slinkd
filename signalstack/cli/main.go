package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

var (
	apiKey string
	host   string
)

func main() {
	apiKey = os.Getenv("SIGNAL_API_KEY")
	host = os.Getenv("SIGNAL_HOST")
	if host == "" {
		host = "http://localhost:8080"
	}
	host = strings.TrimRight(host, "/")

	if len(os.Args) < 2 {
		usage()
	}

	switch os.Args[1] {
	case "tail":
		if len(os.Args) < 3 {
			fatal("usage: signal tail <channel>")
		}
		cmdTail(os.Args[2])
	case "send":
		if len(os.Args) < 3 {
			fatal("usage: signal send <channel> --type=<type> --text=<text>")
		}
		cmdSend(os.Args[2], os.Args[3:])
	case "channel", "channels":
		if len(os.Args) < 3 {
			fatal("usage: signal channel <create|list>")
		}
		switch os.Args[2] {
		case "list":
			cmdChannelsList()
		case "create":
			if len(os.Args) < 4 {
				fatal("usage: signal channel create <id> [--name=<name>]")
			}
			cmdChannelCreate(os.Args[3], os.Args[4:])
		default:
			fatal("usage: signal channel <create|list>")
		}
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `SignalStack CLI

Commands:
  signal tail <channel>                          Stream live events
  signal send <channel> --type=<type> --text=<text>  Publish an event
  signal channel create <id> [--name=<name>]      Create a channel
  signal channel list                             List all channels

Environment:
  SIGNAL_API_KEY   API key for authentication
  SIGNAL_HOST      Server URL (default: http://localhost:8080)
`)
	os.Exit(1)
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}

func cmdTail(channel string) {
	wsURL := strings.Replace(host, "http", "ws", 1) + "/ws?channel=" + url.QueryEscape(channel)
	header := http.Header{}
	header.Set("Authorization", "Bearer "+apiKey)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		fatal(fmt.Sprintf("websocket connect failed: %v", err))
	}
	defer conn.Close()

	fmt.Printf("tailing %s...\n", channel)
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			fatal(fmt.Sprintf("read error: %v", err))
		}
		var ev struct {
			Type      string `json:"type"`
			Author    string `json:"author"`
			Text      string `json:"text"`
			Timestamp int64  `json:"timestamp"`
		}
		json.Unmarshal(msg, &ev)
		ts := time.UnixMilli(ev.Timestamp).Format("15:04:05")
		fmt.Printf("[%s] %s (%s): %s\n", ts, ev.Type, ev.Author, ev.Text)
	}
}

func cmdSend(channel string, args []string) {
	var eventType, text, author string
	for _, arg := range args {
		switch {
		case strings.HasPrefix(arg, "--type="):
			eventType = strings.TrimPrefix(arg, "--type=")
		case strings.HasPrefix(arg, "--text="):
			text = strings.TrimPrefix(arg, "--text=")
		case strings.HasPrefix(arg, "--author="):
			author = strings.TrimPrefix(arg, "--author=")
		}
	}
	if eventType == "" {
		fatal("--type is required")
	}
	if text == "" {
		fatal("--text is required")
	}

	body, _ := json.Marshal(map[string]string{
		"type":   eventType,
		"text":   text,
		"author": author,
	})

	req, _ := http.NewRequest("POST", host+"/channels/"+url.PathEscape(channel)+"/events", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fatal(fmt.Sprintf("request failed: %v", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		fatal(fmt.Sprintf("error %d: %s", resp.StatusCode, string(b)))
	}

	fmt.Println("event sent")
}

func cmdChannelCreate(id string, args []string) {
	name := id
	for _, arg := range args {
		if strings.HasPrefix(arg, "--name=") {
			name = strings.TrimPrefix(arg, "--name=")
		}
	}

	body, _ := json.Marshal(map[string]string{"id": id, "name": name})
	req, _ := http.NewRequest("POST", host+"/channels", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fatal(fmt.Sprintf("request failed: %v", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		fatal(fmt.Sprintf("error %d: %s", resp.StatusCode, string(b)))
	}

	fmt.Printf("channel created: %s\n", id)
}

func cmdChannelsList() {
	req, _ := http.NewRequest("GET", host+"/channels", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fatal(fmt.Sprintf("request failed: %v", err))
	}
	defer resp.Body.Close()

	var channels []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	json.NewDecoder(resp.Body).Decode(&channels)

	if len(channels) == 0 {
		fmt.Println("no channels")
		return
	}

	for _, ch := range channels {
		fmt.Printf("  %s  (%s)\n", ch.ID, ch.Name)
	}
}
