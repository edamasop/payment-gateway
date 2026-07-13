package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"time"

	"github.com/shopspring/decimal"
)

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

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		log.Fatal("Port argument is required")
	}

	http.HandleFunc("/v1/charges", handleCreatePayment)

	fmt.Println("Payment Gateway Mock started on port:", args[0])
	log.Fatal(http.ListenAndServe(":"+args[0], nil))
}

func handleCreatePayment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req CreatePaymentRequest
	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Read Idempotency Key header if sent by the client
	idempotencyKey := r.Header.Get("X-Idempotency-Key")

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
	// This helps test your immediate structural 'IsRejected' handling block.
	if rand.Float32() < 0.05 {
		resp.IsRejected = true
		resp.RedirectURL = ""
		resp.ErrorMessage = "Card security validation failed or merchant account restricted"

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK) // Gateway responds successfully but contains rejection status payload
		json.NewEncoder(w).Encode(resp)

		log.Printf("[Gateway Sync Rejection] Payment rejected immediately for Internal ID: %s, Order ID: %s\n", req.PaymentID, req.OrderID)
		return
	}

	// Send an immediate successful response containing the checkout dynamic link
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)

	log.Printf("[Gateway Session Created] Reference ID: %s | Order: %s | Idempotency Key: %s | Amount: %d %s\n",
		req.PaymentID, req.OrderID, idempotencyKey, req.Amount, req.Currency)

	// Asynchronously trigger customer interaction delay & webhook dispatching thread
	go simulateUserPayment(req.PaymentID, req.OrderID, mockTxID, req.WebhookURL)
}

// simulateUserPayment mimics async credit card processor delays and callback execution
func simulateUserPayment(paymentID, orderID, txID, webhookURL string) {
	// Simulate delay: user takes between 2 and 5 seconds to pass 3D Secure / enter dynamic OTP code
	holdTime := time.Duration(rand.Intn(3)+2) * time.Second
	time.Sleep(holdTime)

	// Simulate authorization outcome: 80% success rate, 20% failure rate (e.g., insufficient funds)
	status := "SUCCESSFUL"
	if rand.Float32() < 0.20 {
		status = "FAILED"
	}

	notification := WebhookNotification{
		PaymentID: paymentID, // Passing back our core service internal record identity
		OrderID:   orderID,
		Status:    status,
	}

	payload, _ := json.Marshal(notification)

	h := hmac.New(sha256.New, []byte("my-secret-key"))
	h.Write(payload)
	signature := hex.EncodeToString(h.Sum(nil))

	log.Printf("[Gateway Async Event] Sending webhook payload back to PaymentService (%s) -> Status: %s\n", webhookURL, status)

	req, err := http.NewRequest("POST", webhookURL, bytes.NewBuffer(payload))
	if err != nil {
		log.Printf("Failed to create request: %v", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Signature", signature)

	client := &http.Client{Timeout: 10 * time.Second}

	resp, err := client.Do(req)
	defer resp.Body.Close()

	log.Printf("[Gateway Async Event] Webhook delivered securely, microservice response: %d\n", resp.StatusCode)
}
