package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"time"

	"github.com/shopspring/decimal"
)

// CreatePaymentRequest represents the request from our PaymentService to the gateway
type CreatePaymentRequest struct {
	OrderID    string          `json:"order_id"`
	Amount     decimal.Decimal `json:"amount"`
	Currency   string          `json:"currency"`
	WebhookURL string          `json:"webhook_url"` // Target URL to send the payment result callback
}

// CreatePaymentResponse represents the response from the gateway to our PaymentService
type CreatePaymentResponse struct {
	PaymentID  string `json:"payment_id"`
	PaymentURL string `json:"payment_url"`
	Status     string `json:"status"`
}

// WebhookNotification represents the webhook payload sent back after user interaction
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

	// Initialize the random generator to simulate payment variations and failures
	rand.Seed(time.Now().UnixNano())

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

	// Generate a mock transaction ID on the gateway side
	mockPaymentID := fmt.Sprintf("ch_%d", rand.Intn(1000000))
	// Mock URL where the user would theoretically input their card details
	mockPaymentURL := fmt.Sprintf("https://mock-checkout.gateway.com/pay/%s", mockPaymentID)

	resp := CreatePaymentResponse{
		PaymentID:  mockPaymentID,
		PaymentURL: mockPaymentURL,
		Status:     "PENDING",
	}

	// Send an immediate response back to our internal PaymentService
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)

	log.Printf("[Gateway] Payment session created for Order %s. Amount: %.2f %s\n", req.OrderID, req.Amount, req.Currency)

	// Asynchronously trigger user payment simulation
	go simulateUserPayment(req.OrderID, mockPaymentID, req.WebhookURL)
}

// simulateUserPayment mimics user delay and webhook callback execution
func simulateUserPayment(orderID, paymentID, webhookURL string) {
	// Simulate delay: user takes between 3 and 5 seconds to enter details
	holdTime := time.Duration(rand.Intn(3)+3) * time.Second
	time.Sleep(holdTime)

	// Simulate payment outcome: 85% success rate, 15% failure rate (e.g., insufficient funds)
	status := "SUCCESSFUL"
	if rand.Float32() < 0.15 {
		status = "FAILED"
	}

	notification := WebhookNotification{
		PaymentID: paymentID,
		OrderID:   orderID,
		Status:    status,
	}

	payload, _ := json.Marshal(notification)

	log.Printf("[Gateway] Sending webhook back to PaymentService (%s) -> Status: %s\n", webhookURL, status)

	// Send HTTP POST request to the WebhookURL of our actual PaymentService
	resp, err := http.Post(webhookURL, "application/json", bytes.NewBuffer(payload))
	if err != nil {
		log.Printf("[Gateway ERROR] Failed to reach PaymentService: %v\n", err)
		return
	}
	defer resp.Body.Close()

	log.Printf("[Gateway] Webhook successfully delivered, server response: %d\n", resp.StatusCode)
}
