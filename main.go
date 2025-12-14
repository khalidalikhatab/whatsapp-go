package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/rs/cors"
	qrcode "github.com/skip2/go-qrcode"
	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

var (
	client           *whatsmeow.Client
	qrCodeData       string
	connectionStatus = "disconnected"
	serverLogs       []string
	logMutex         sync.Mutex
)

func addLog(message string) {
	logMutex.Lock()
	defer logMutex.Unlock()
	
	timestamp := time.Now().Format(time.RFC3339)
	logEntry := fmt.Sprintf("%s - %s", timestamp, message)
	log.Println(logEntry)
	
	serverLogs = append([]string{logEntry}, serverLogs...)
	if len(serverLogs) > 100 {
		serverLogs = serverLogs[:100]
	}
}

func eventHandler(evt interface{}) {
	switch v := evt.(type) {
	case *events.Message:
		if !v.Info.IsFromMe {
			text := ""
			if v.Message.Conversation != nil {
				text = *v.Message.Conversation
			} else if v.Message.ExtendedTextMessage != nil {
				text = *v.Message.ExtendedTextMessage.Text
			}
			
			if text != "" {
				addLog(fmt.Sprintf("Message from %s: %s", v.Info.Sender.String(), text))
				
				// Auto-reply
				reply := fmt.Sprintf("Hello! I am an AI assistant. I received your message: \"%s\"", text)
				_, err := client.SendMessage(context.Background(), v.Info.Chat, &waProto.Message{
					Conversation: &reply,
				})
				if err != nil {
					addLog(fmt.Sprintf("Error sending reply: %s", err.Error()))
				} else {
					addLog(fmt.Sprintf("Reply sent to %s", v.Info.Sender.String()))
				}
			}
		}
	case *events.Connected:
		addLog("Connected to WhatsApp!")
		connectionStatus = "connected"
		qrCodeData = ""
	case *events.Disconnected:
		addLog("Disconnected from WhatsApp")
		connectionStatus = "disconnected"
	case *events.LoggedOut:
		addLog("Logged out from WhatsApp")
		connectionStatus = "logged_out"
		qrCodeData = ""
	}
}

func startWhatsApp() {
	addLog("Starting WhatsApp connection...")
	
	// Create database for session storage
	dbLog := waLog.Stdout("Database", "WARN", true)
	container, err := sqlstore.New("sqlite3", "file:whatsapp.db?_foreign_keys=on", dbLog)
	if err != nil {
		addLog(fmt.Sprintf("Failed to create database: %s", err.Error()))
		return
	}

	deviceStore, err := container.GetFirstDevice()
	if err != nil {
		addLog(fmt.Sprintf("Failed to get device: %s", err.Error()))
		return
	}

	clientLog := waLog.Stdout("Client", "WARN", true)
	client = whatsmeow.NewClient(deviceStore, clientLog)
	client.AddEventHandler(eventHandler)

	if client.Store.ID == nil {
		// No session, need QR code
		addLog("No session found, generating QR code...")
		qrChan, _ := client.GetQRChannel(context.Background())
		err = client.Connect()
		if err != nil {
			addLog(fmt.Sprintf("Failed to connect: %s", err.Error()))
			return
		}

		for evt := range qrChan {
			if evt.Event == "code" {
				addLog("QR Code received - scan with WhatsApp")
				connectionStatus = "scanning"
				
				// Generate QR code as base64 image
				qr, err := qrcode.Encode(evt.Code, qrcode.Medium, 256)
				if err != nil {
					addLog(fmt.Sprintf("Failed to generate QR image: %s", err.Error()))
					continue
				}
				qrCodeData = "data:image/png;base64," + base64.StdEncoding.EncodeToString(qr)
			} else {
				addLog(fmt.Sprintf("QR event: %s", evt.Event))
			}
		}
	} else {
		// Session exists, just connect
		addLog("Session found, connecting...")
		err = client.Connect()
		if err != nil {
			addLog(fmt.Sprintf("Failed to connect: %s", err.Error()))
			return
		}
	}
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}

	r := mux.NewRouter()

	// Home page
	r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		html := fmt.Sprintf(`
			<html>
			<head><title>WhatsApp Bot (Go)</title></head>
			<body style="font-family: Arial; padding: 20px;">
				<h1>WhatsApp Bot Server (Go/Whatsmeow)</h1>
				<p>Status: <strong>%s</strong></p>
				<p><a href="/qr">Get QR Code API</a></p>
				<p><a href="/logs">View Logs</a></p>
				<p><a href="/reset">Reset Session</a></p>
			</body>
			</html>
		`, connectionStatus)
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(html))
	}).Methods("GET")

	// QR Code endpoint
	r.HandleFunc("/qr", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		
		response := map[string]interface{}{
			"status": connectionStatus,
			"qr":     nil,
		}
		
		if connectionStatus == "scanning" && qrCodeData != "" {
			response["qr"] = qrCodeData
		}
		
		json.NewEncoder(w).Encode(response)
	}).Methods("GET")

	// Logs endpoint
	r.HandleFunc("/logs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		logMutex.Lock()
		defer logMutex.Unlock()
		json.NewEncoder(w).Encode(map[string]interface{}{"logs": serverLogs})
	}).Methods("GET")

	// Send message endpoint
	r.HandleFunc("/send", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		
		var req struct {
			To   string `json:"to"`
			Text string `json:"text"`
		}
		
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		
		if client == nil {
			json.NewEncoder(w).Encode(map[string]string{"error": "Bot not initialized"})
			return
		}
		
		// TODO: Implement send message
		json.NewEncoder(w).Encode(map[string]bool{"success": true})
	}).Methods("POST")

	// Reset endpoint
	r.HandleFunc("/reset", func(w http.ResponseWriter, r *http.Request) {
		addLog("Manual reset requested...")
		
		if client != nil {
			client.Disconnect()
		}
		
		// Remove database to force new QR
		os.Remove("whatsapp.db")
		os.Remove("whatsapp.db-shm")
		os.Remove("whatsapp.db-wal")
		
		connectionStatus = "disconnected"
		qrCodeData = ""
		
		go func() {
			time.Sleep(2 * time.Second)
			startWhatsApp()
		}()
		
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": "Session reset. New QR will appear shortly.",
		})
	}).Methods("GET")

	// Health check
	r.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status":   "ok",
			"whatsapp": connectionStatus,
		})
	}).Methods("GET")

	// Start WhatsApp in background
	go startWhatsApp()

	// Start HTTP server
	handler := cors.AllowAll().Handler(r)
	addLog(fmt.Sprintf("Server running on port %s", port))
	log.Fatal(http.ListenAndServe(":"+port, handler))
}
