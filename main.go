package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

// This is a mock of an external payment provider. It intentionally keeps state
// in memory: the goal is to exercise the payment service's HTTP and webhook
// handling, not to be a real gateway.

// CreatePaymentRequest mirrors schema.CreateGatewayPayment from the core service
type CreatePaymentRequest struct {
	PaymentID  string          `json:"payment_id"`  // Our internal DB record ID
	OrderID    string          `json:"order_id"`    // Commercial order ID
	Amount     decimal.Decimal `json:"amount"`      // Amount in minor units (cents/tiyn)
	Currency   string          `json:"currency"`    // e.g., "KZT"
	WebhookURL string          `json:"webhook_url"` // Target URL to send callback notifications
}

// GatewayResponse mirrors schema.GatewayResponse expected by the core service
type GatewayResponse struct {
	TransactionID    string `json:"transaction_id"`
	RedirectURL      string `json:"redirect_url"`
	IsRejected       bool   `json:"is_rejected"`
	IsInstantSuccess bool   `json:"is_instant_success"`
	ErrorMessage     string `json:"error_message,omitempty"`
}

// WebhookNotification represents the payload sent asynchronously to the webhooks controller
type WebhookNotification struct {
	PaymentID string `json:"payment_id"`
	OrderID   string `json:"order_id"`
	Status    string `json:"status"` // SUCCESSFUL or FAILED
}

const (
	// defaultPort keeps `go run .` / ./bin/payment-gateway usable without any
	// environment; PORT or the first CLI argument override it.
	defaultPort          = "8082"
	defaultWebhookSecret = "dev-webhook-secret"

	// Simulation modes. "random" keeps the original mock behaviour (5%
	// synchronous rejection, 20% asynchronous failure); the other two make the
	// outcome deterministic, which is what an end-to-end check wants.
	simulationRandom  = "random"
	simulationSuccess = "success"
	simulationFailed  = "failed"
	webhookAttempts   = 3
	webhookTimeout    = 10 * time.Second
)

var (
	// idempotency remembers the answer we gave for an X-Idempotency-Key. A
	// real gateway persists this; for the mock a map is enough to show why the
	// header exists: retrying a charge must not create a second transaction.
	idempotencyMu sync.Mutex
	idempotency   = make(map[string]GatewayResponse)

	webhookSecret = envOr("PAYMENT_WEBHOOK_SECRET", defaultWebhookSecret)
	simulation    = envOr("GATEWAY_SIMULATION", simulationRandom)
)

func main() {
	port := os.Getenv("PORT")
	if len(os.Args) > 1 {
		port = os.Args[1]
	}
	if port == "" {
		port = defaultPort
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/charges", handleCreatePayment)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	if simulation != simulationRandom && simulation != simulationSuccess && simulation != simulationFailed {
		log.Fatalf("invalid GATEWAY_SIMULATION %q: use random, success or failed", simulation)
	}

	log.Printf("Payment Gateway Mock started on port %s (simulation: %s)", port, simulation)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
	}
}

func handleCreatePayment(w http.ResponseWriter, r *http.Request) {
	var req CreatePaymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	idempotencyKey := r.Header.Get("X-Idempotency-Key")

	if idempotencyKey != "" {
		idempotencyMu.Lock()
		stored, ok := idempotency[idempotencyKey]
		idempotencyMu.Unlock()

		if ok {
			log.Printf("[Gateway Idempotent Replay] key %s -> transaction %s", idempotencyKey, stored.TransactionID)
			writeJSON(w, http.StatusOK, stored)
			return
		}
	}

	// Generate a unique mock transaction ID representing the gateway asset
	mockTxID := fmt.Sprintf("ch_%d", rand.Intn(1000000))
	mockRedirectURL := fmt.Sprintf("https://mock-checkout.gateway.com/pay/%s", mockTxID)

	resp := GatewayResponse{
		TransactionID:    mockTxID,
		RedirectURL:      mockRedirectURL,
		IsRejected:       false,
		IsInstantSuccess: false,
	}

	// Simulation: 5% chance the gateway rejects the transaction immediately (e.g., blacklisted card)
	// This helps test the immediate structural 'IsRejected' handling block.
	if simulation == simulationRandom && rand.Float32() < 0.05 {
		resp.IsRejected = true
		resp.RedirectURL = ""
		resp.ErrorMessage = "Card security validation failed or merchant account restricted"

		// The rejection is part of the protocol, so it is cached as well:
		// retrying the same charge must not flip a rejected payment.
		remember(idempotencyKey, resp)

		writeJSON(w, http.StatusOK, resp)
		log.Printf("[Gateway Sync Rejection] Payment rejected immediately for Internal ID: %s, Order ID: %s", req.PaymentID, req.OrderID)
		return
	}

	remember(idempotencyKey, resp)

	// Send an immediate successful response containing the checkout dynamic link
	writeJSON(w, http.StatusCreated, resp)

	log.Printf("[Gateway Session Created] Reference ID: %s | Order: %s | Idempotency Key: %s | Amount: %s %s",
		req.PaymentID, req.OrderID, idempotencyKey, req.Amount.String(), req.Currency)

	if req.WebhookURL == "" {
		log.Printf("[Gateway Async Event] no webhook_url supplied for payment %s; skipping callback", req.PaymentID)
		return
	}

	// Asynchronously trigger customer interaction delay & webhook dispatching thread
	go simulateUserPayment(req.PaymentID, req.OrderID, mockTxID, req.WebhookURL)
}

func remember(key string, resp GatewayResponse) {
	if key == "" {
		return
	}

	idempotencyMu.Lock()
	idempotency[key] = resp
	idempotencyMu.Unlock()
}

// simulateUserPayment mimics async credit card processor delays and callback execution
func simulateUserPayment(paymentID, orderID, txID, webhookURL string) {
	// Simulate delay: user takes between 2 and 5 seconds to pass 3D Secure / enter dynamic OTP code
	holdTime := time.Duration(rand.Intn(3)+2) * time.Second
	time.Sleep(holdTime)

	// Simulate authorization outcome: 80% success rate, 20% failure rate
	// (e.g., insufficient funds), unless GATEWAY_SIMULATION pins it.
	status := "SUCCESSFUL"
	switch simulation {
	case simulationSuccess:
		status = "SUCCESSFUL"
	case simulationFailed:
		status = "FAILED"
	default:
		if rand.Float32() < 0.20 {
			status = "FAILED"
		}
	}

	payload, err := json.Marshal(WebhookNotification{
		PaymentID: paymentID, // Passing back our core service internal record identity
		OrderID:   orderID,
		Status:    status,
	})
	if err != nil {
		log.Printf("[Gateway Async Event] failed to marshal webhook payload: %v", err)
		return
	}

	mac := hmac.New(sha256.New, []byte(webhookSecret))
	mac.Write(payload)
	signature := hex.EncodeToString(mac.Sum(nil))

	log.Printf("[Gateway Async Event] Sending webhook payload back to PaymentService (%s) -> Status: %s", webhookURL, status)

	// The gateway retries callbacks: the payment service must be idempotent.
	deliverWebhook(webhookURL, payload, signature)
}

func deliverWebhook(webhookURL string, payload []byte, signature string) {
	client := &http.Client{Timeout: webhookTimeout}

	var lastErr error

	for attempt := 1; attempt <= webhookAttempts; attempt++ {
		req, err := http.NewRequest(http.MethodPost, webhookURL, bytes.NewReader(payload))
		if err != nil {
			// A malformed URL cannot be fixed by retrying.
			log.Printf("[Gateway Async Event] failed to build webhook request: %v", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Signature", signature)

		resp, err := client.Do(req)
		switch {
		case err != nil:
			lastErr = err
			log.Printf("[Gateway Async Event] webhook attempt %d/%d failed: %v", attempt, webhookAttempts, err)
		default:
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 512))
			// The body must be closed before the response is discarded; a nil
			// response cannot be dereferenced (the previous code did exactly
			// that and panicked whenever the payment service was down).
			_ = resp.Body.Close()

			if readErr == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
				log.Printf("[Gateway Async Event] webhook delivered, payment service answered %d", resp.StatusCode)
				return
			}

			lastErr = fmt.Errorf("status %s: %s", resp.Status, string(bytes.TrimSpace(body)))
			log.Printf("[Gateway Async Event] webhook attempt %d/%d rejected: %v", attempt, webhookAttempts, lastErr)
		}

		if attempt < webhookAttempts {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
	}

	log.Printf("[Gateway Async Event] giving up after %d attempts: %v", webhookAttempts, lastErr)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}

	return fallback
}
