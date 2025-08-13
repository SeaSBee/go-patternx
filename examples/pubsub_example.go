package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/SeaSBee/go-patternx/patternx/pubsub"
)

// Order represents an order message
type Order struct {
	ID       string  `json:"id"`
	UserID   string  `json:"user_id"`
	Amount   float64 `json:"amount"`
	Items    []Item  `json:"items"`
	Priority int     `json:"priority"`
}

// Item represents an order item
type Item struct {
	ProductID string  `json:"product_id"`
	Quantity  int     `json:"quantity"`
	Price     float64 `json:"price"`
}

// Payment represents a payment message
type Payment struct {
	ID      string  `json:"id"`
	OrderID string  `json:"order_id"`
	Amount  float64 `json:"amount"`
	Method  string  `json:"method"`
	Status  string  `json:"status"`
}

// Notification represents a notification message
type Notification struct {
	UserID  string `json:"user_id"`
	Type    string `json:"type"`
	Message string `json:"message"`
	Channel string `json:"channel"`
}

// MockStore implements a simple in-memory store for the example
type MockStore struct {
	data map[string][]byte
	mu   sync.RWMutex
}

func NewMockStore() *MockStore {
	return &MockStore{
		data: make(map[string][]byte),
	}
}

func (m *MockStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = value
	return nil
}

func (m *MockStore) Get(ctx context.Context, key string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if value, exists := m.data[key]; exists {
		return value, nil
	}
	return nil, fmt.Errorf("key not found: %s", key)
}

func (m *MockStore) Del(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *MockStore) Exists(ctx context.Context, key string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, exists := m.data[key]
	return exists, nil
}

// OrderProcessor handles order processing
type OrderProcessor struct {
	processedOrders []Order
	mu              sync.Mutex
}

func NewOrderProcessor() *OrderProcessor {
	return &OrderProcessor{
		processedOrders: make([]Order, 0),
	}
}

func (op *OrderProcessor) ProcessOrder(ctx context.Context, msg *pubsub.Message) error {
	var order Order
	if err := json.Unmarshal(msg.Data, &order); err != nil {
		return fmt.Errorf("failed to unmarshal order: %w", err)
	}

	// Simulate order processing
	log.Printf("Processing order: %s for user: %s, amount: $%.2f", order.ID, order.UserID, order.Amount)

	// Add some processing time
	time.Sleep(10 * time.Millisecond)

	op.mu.Lock()
	op.processedOrders = append(op.processedOrders, order)
	op.mu.Unlock()

	log.Printf("Order %s processed successfully", order.ID)
	return nil
}

func (op *OrderProcessor) GetProcessedOrders() []Order {
	op.mu.Lock()
	defer op.mu.Unlock()
	return append([]Order{}, op.processedOrders...)
}

// PaymentProcessor handles payment processing with retry logic
type PaymentProcessor struct {
	processedPayments []Payment
	attemptCount      int
	mu                sync.Mutex
}

func NewPaymentProcessor() *PaymentProcessor {
	return &PaymentProcessor{
		processedPayments: make([]Payment, 0),
	}
}

func (pp *PaymentProcessor) ProcessPayment(ctx context.Context, msg *pubsub.Message) error {
	var payment Payment
	if err := json.Unmarshal(msg.Data, &payment); err != nil {
		return fmt.Errorf("failed to unmarshal payment: %w", err)
	}

	pp.mu.Lock()
	pp.attemptCount++
	currentAttempt := pp.attemptCount
	pp.mu.Unlock()

	log.Printf("Processing payment: %s for order: %s, attempt: %d", payment.ID, payment.OrderID, currentAttempt)

	// Simulate payment processing that might fail initially
	if currentAttempt < 3 {
		log.Printf("Payment processing failed, will retry...")
		return fmt.Errorf("payment gateway temporarily unavailable")
	}

	// Simulate successful payment processing
	time.Sleep(20 * time.Millisecond)

	pp.mu.Lock()
	pp.processedPayments = append(pp.processedPayments, payment)
	pp.mu.Unlock()

	log.Printf("Payment %s processed successfully", payment.ID)
	return nil
}

func (pp *PaymentProcessor) GetProcessedPayments() []Payment {
	pp.mu.Lock()
	defer pp.mu.Unlock()
	return append([]Payment{}, pp.processedPayments...)
}

// NotificationSender handles notification sending
type NotificationSender struct {
	sentNotifications []Notification
	mu                sync.Mutex
}

func NewNotificationSender() *NotificationSender {
	return &NotificationSender{
		sentNotifications: make([]Notification, 0),
	}
}

func (ns *NotificationSender) SendNotification(ctx context.Context, msg *pubsub.Message) error {
	var notification Notification
	if err := json.Unmarshal(msg.Data, &notification); err != nil {
		return fmt.Errorf("failed to unmarshal notification: %w", err)
	}

	log.Printf("Sending %s notification to user: %s via %s", notification.Type, notification.UserID, notification.Channel)

	// Simulate notification sending
	time.Sleep(5 * time.Millisecond)

	ns.mu.Lock()
	ns.sentNotifications = append(ns.sentNotifications, notification)
	ns.mu.Unlock()

	log.Printf("Notification sent successfully to user: %s", notification.UserID)
	return nil
}

func (ns *NotificationSender) GetSentNotifications() []Notification {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	return append([]Notification{}, ns.sentNotifications...)
}

func main() {
	// Create a mock store for message persistence
	store := NewMockStore()

	// Create pub/sub system with high reliability configuration
	config := pubsub.HighReliabilityConfig(store)
	ps, err := pubsub.NewPubSub(config)
	if err != nil {
		log.Fatalf("Failed to create pub/sub system: %v", err)
	}
	defer ps.Close(context.Background())

	// Create topics
	topics := []string{"orders", "payments", "notifications"}
	for _, topic := range topics {
		if err := ps.CreateTopic(context.Background(), topic); err != nil {
			log.Fatalf("Failed to create topic %s: %v", topic, err)
		}
		log.Printf("Created topic: %s", topic)
	}

	// Create processors
	orderProcessor := NewOrderProcessor()
	paymentProcessor := NewPaymentProcessor()
	notificationSender := NewNotificationSender()

	// Subscribe to topics without filters for this example
	_, err = ps.Subscribe(context.Background(), "orders", "order-processor", orderProcessor.ProcessOrder, &pubsub.MessageFilter{})
	if err != nil {
		log.Fatalf("Failed to subscribe to orders: %v", err)
	}

	_, err = ps.Subscribe(context.Background(), "payments", "payment-processor", paymentProcessor.ProcessPayment, &pubsub.MessageFilter{})
	if err != nil {
		log.Fatalf("Failed to subscribe to payments: %v", err)
	}

	_, err = ps.Subscribe(context.Background(), "notifications", "notification-sender", notificationSender.SendNotification, &pubsub.MessageFilter{})
	if err != nil {
		log.Fatalf("Failed to subscribe to notifications: %v", err)
	}

	log.Println("All subscriptions created successfully")

	// Create sample data
	orders := []Order{
		{
			ID:       "order-001",
			UserID:   "user-123",
			Amount:   99.99,
			Priority: 10,
			Items: []Item{
				{ProductID: "prod-001", Quantity: 2, Price: 49.99},
			},
		},
		{
			ID:       "order-002",
			UserID:   "user-456",
			Amount:   149.99,
			Priority: 8,
			Items: []Item{
				{ProductID: "prod-002", Quantity: 1, Price: 149.99},
			},
		},
	}

	payments := []Payment{
		{
			ID:      "payment-001",
			OrderID: "order-001",
			Amount:  99.99,
			Method:  "credit_card",
			Status:  "pending",
		},
		{
			ID:      "payment-002",
			OrderID: "order-002",
			Amount:  149.99,
			Method:  "paypal",
			Status:  "pending",
		},
	}

	notifications := []Notification{
		{
			UserID:  "user-123",
			Type:    "order_confirmation",
			Message: "Your order has been confirmed",
			Channel: "email",
		},
		{
			UserID:  "user-456",
			Type:    "order_confirmation",
			Message: "Your order has been confirmed",
			Channel: "sms",
		},
	}

	// Publish messages concurrently
	var wg sync.WaitGroup

	// Publish orders
	for _, order := range orders {
		wg.Add(1)
		go func(o Order) {
			defer wg.Done()
			data, _ := json.Marshal(o)
			headers := map[string]string{
				"type":     "order",
				"priority": fmt.Sprintf("%d", o.Priority),
			}
			if err := ps.Publish(context.Background(), "orders", data, headers); err != nil {
				log.Printf("Failed to publish order %s: %v", o.ID, err)
			} else {
				log.Printf("Published order: %s", o.ID)
			}
		}(order)
	}

	// Publish payments
	for _, payment := range payments {
		wg.Add(1)
		go func(p Payment) {
			defer wg.Done()
			data, _ := json.Marshal(p)
			headers := map[string]string{
				"type":   "payment",
				"method": p.Method,
			}
			if err := ps.Publish(context.Background(), "payments", data, headers); err != nil {
				log.Printf("Failed to publish payment %s: %v", p.ID, err)
			} else {
				log.Printf("Published payment: %s", p.ID)
			}
		}(payment)
	}

	// Publish notifications
	for _, notification := range notifications {
		wg.Add(1)
		go func(n Notification) {
			defer wg.Done()
			data, _ := json.Marshal(n)
			headers := map[string]string{
				"type":    "notification",
				"channel": n.Channel,
			}
			if err := ps.Publish(context.Background(), "notifications", data, headers); err != nil {
				log.Printf("Failed to publish notification for user %s: %v", n.UserID, err)
			} else {
				log.Printf("Published notification for user: %s", n.UserID)
			}
		}(notification)
	}

	wg.Wait()
	log.Println("All messages published")

	// Wait for message processing
	time.Sleep(2 * time.Second)

	// Get system statistics
	stats := ps.GetStats()
	log.Printf("System Statistics:")
	log.Printf("  Total Messages: %d", stats["total_messages"])
	log.Printf("  Total Topics: %d", stats["total_topics"])
	log.Printf("  Total Subscriptions: %d", stats["total_subscriptions"])
	log.Printf("  Errors: %d", stats["errors"])

	// Get processor results
	processedOrders := orderProcessor.GetProcessedOrders()
	processedPayments := paymentProcessor.GetProcessedPayments()
	sentNotifications := notificationSender.GetSentNotifications()

	log.Printf("\nProcessing Results:")
	log.Printf("  Orders Processed: %d", len(processedOrders))
	log.Printf("  Payments Processed: %d", len(processedPayments))
	log.Printf("  Notifications Sent: %d", len(sentNotifications))

	// Verify results
	if len(processedOrders) != len(orders) {
		log.Printf("Warning: Expected %d orders, but processed %d", len(orders), len(processedOrders))
	}

	if len(processedPayments) != len(payments) {
		log.Printf("Warning: Expected %d payments, but processed %d", len(payments), len(processedPayments))
	}

	if len(sentNotifications) != len(notifications) {
		log.Printf("Warning: Expected %d notifications, but sent %d", len(notifications), len(sentNotifications))
	}

	log.Println("\nExample completed successfully!")
}
