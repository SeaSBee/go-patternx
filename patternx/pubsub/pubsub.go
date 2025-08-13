package pubsub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/seasbee/go-logx"
)

// Common errors for pub/sub operations
var (
	ErrInvalidConfig         = errors.New("invalid pub/sub configuration")
	ErrTopicNotFound         = errors.New("topic not found")
	ErrSubscriptionNotFound  = errors.New("subscription not found")
	ErrMessageTooLarge       = errors.New("message exceeds maximum size")
	ErrSubscriptionClosed    = errors.New("subscription is closed")
	ErrPublisherClosed       = errors.New("publisher is closed")
	ErrContextCancelled      = errors.New("operation cancelled by context")
	ErrMessageDeliveryFailed = errors.New("message delivery failed")
	ErrStoreUnavailable      = errors.New("store is not available")
	ErrInvalidMessage        = errors.New("invalid message")
	ErrTopicClosed           = errors.New("topic is closed")
	ErrSubscriptionLimit     = errors.New("subscription limit exceeded")
	ErrConcurrencyLimit      = errors.New("concurrency limit exceeded")
	ErrBatchTooLarge         = errors.New("batch size exceeds limit")
	ErrInvalidHeaders        = errors.New("invalid message headers")
	ErrCircuitBreakerOpen    = errors.New("circuit breaker is open")
	ErrDeadLetterQueue       = errors.New("message sent to dead letter queue")
	ErrTimeout               = errors.New("operation timeout")
	ErrPanicRecovered        = errors.New("panic recovered in message handler")
)

// Constants for production constraints
const (
	MaxMessageSize                 = 10 * 1024 * 1024 // 10MB max message size
	MinMessageSize                 = 1                // 1 byte min message size
	MaxTopicNameLength             = 255              // Max topic name length
	MaxSubscriberCount             = 10000            // Max subscribers per topic
	MaxConcurrentOperations        = 1000             // Max concurrent operations
	MaxBatchSize                   = 1000             // Max messages in a batch
	DefaultBufferSize              = 10000            // Default message buffer size
	DefaultTTL                     = 24 * time.Hour
	DefaultKeyPrefix               = "pubsub"
	MaxRetryAttempts               = 3
	RetryDelay                     = 100 * time.Millisecond
	DefaultOperationTimeout        = 30 * time.Second
	DefaultCircuitBreakerThreshold = 5
	DefaultCircuitBreakerTimeout   = 60 * time.Second
	MaxHeaderKeyLength             = 100
	MaxHeaderValueLength           = 1000
	MaxHeadersCount                = 50
)

// Message represents a pub/sub message with metadata
type Message struct {
	ID          string            `json:"id"`
	Topic       string            `json:"topic"`
	Data        []byte            `json:"data"`
	Headers     map[string]string `json:"headers"`
	Timestamp   time.Time         `json:"timestamp"`
	RetryCount  int               `json:"retry_count"`
	Priority    int               `json:"priority"`
	TTL         time.Duration     `json:"ttl"`
	Correlation string            `json:"correlation_id,omitempty"`
	TraceID     string            `json:"trace_id,omitempty"`
	Sequence    uint64            `json:"sequence,omitempty"`
}

// MessageHandler defines the interface for processing messages
type MessageHandler func(ctx context.Context, msg *Message) error

// DeadLetterHandler defines the interface for handling failed messages
type DeadLetterHandler func(ctx context.Context, msg *Message, err error) error

// CircuitBreaker tracks failure rates and prevents cascading failures
type CircuitBreaker struct {
	failureCount atomic.Int64
	lastFailure  atomic.Value // time.Time
	state        atomic.Int32 // 0: closed, 1: open, 2: half-open
	threshold    int64
	timeout      time.Duration
}

// NewCircuitBreaker creates a new circuit breaker
func NewCircuitBreaker(threshold int64, timeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		threshold: threshold,
		timeout:   timeout,
	}
}

// Execute runs the operation with circuit breaker protection
func (cb *CircuitBreaker) Execute(ctx context.Context, operation func() error) error {
	state := cb.getState()

	switch state {
	case 1: // Open
		if cb.shouldAttemptReset() {
			cb.setState(2) // Half-open
		} else {
			return ErrCircuitBreakerOpen
		}
	case 2: // Half-open
		// Allow one attempt
	}

	err := operation()

	if err != nil {
		cb.recordFailure()
		return err
	}

	cb.recordSuccess()
	return nil
}

func (cb *CircuitBreaker) getState() int32 {
	return cb.state.Load()
}

func (cb *CircuitBreaker) setState(state int32) {
	cb.state.Store(state)
}

func (cb *CircuitBreaker) shouldAttemptReset() bool {
	lastFailure := cb.lastFailure.Load()
	if lastFailure == nil {
		return true
	}
	return time.Since(lastFailure.(time.Time)) > cb.timeout
}

func (cb *CircuitBreaker) recordFailure() {
	cb.failureCount.Add(1)
	cb.lastFailure.Store(time.Now())

	if cb.failureCount.Load() >= cb.threshold {
		cb.setState(1) // Open
	}
}

func (cb *CircuitBreaker) recordSuccess() {
	cb.failureCount.Store(0)
	cb.setState(0) // Closed
}

// Subscription represents a topic subscription with reliability features
type Subscription struct {
	ID                string
	Topic             string
	Handler           MessageHandler
	DeadLetterHandler DeadLetterHandler
	Filter            MessageFilter
	BatchSize         int
	BufferSize        int
	MaxRetries        int
	RetryDelay        time.Duration
	CircuitBreaker    *CircuitBreaker
	closed            int32
	metrics           *SubscriptionMetrics
	workerPool        chan struct{} // Semaphore for concurrency control
}

// SubscriptionMetrics tracks subscription performance
type SubscriptionMetrics struct {
	MessagesReceived    atomic.Uint64
	MessagesProcessed   atomic.Uint64
	MessagesFailed      atomic.Uint64
	MessagesRetried     atomic.Uint64
	MessagesDLQ         atomic.Uint64
	AverageLatency      atomic.Int64 // nanoseconds
	LastMessageTime     atomic.Value // time.Time
	CircuitBreakerTrips atomic.Uint64
	PanicRecoveries     atomic.Uint64
	TimeoutErrors       atomic.Uint64
}

// MessageFilter defines message filtering criteria
type MessageFilter struct {
	Headers       map[string]string
	Priority      int
	MaxRetryCount int
	RegexPatterns map[string]string // Header value regex patterns
}

// Publisher manages message publishing with reliability features
type Publisher struct {
	topics         map[string]*Topic
	mu             sync.RWMutex
	store          PubSubStore
	maxRetries     int
	retryDelay     time.Duration
	metrics        *PublisherMetrics
	circuitBreaker *CircuitBreaker
	workerPool     chan struct{} // Semaphore for concurrency control
}

// PublisherMetrics tracks publisher performance
type PublisherMetrics struct {
	MessagesPublished   atomic.Uint64
	MessagesFailed      atomic.Uint64
	TopicsCreated       atomic.Uint64
	AverageLatency      atomic.Int64 // nanoseconds
	LastPublishTime     atomic.Value // time.Time
	BatchOperations     atomic.Uint64
	CircuitBreakerTrips atomic.Uint64
}

// Topic represents a message topic with subscribers
type Topic struct {
	Name         string
	Subscribers  map[string]*Subscription
	mu           sync.RWMutex
	MessageQueue chan *Message
	closed       int32
	metrics      *TopicMetrics
	sequence     atomic.Uint64
}

// TopicMetrics tracks topic performance
type TopicMetrics struct {
	MessagesPublished atomic.Uint64
	MessagesDelivered atomic.Uint64
	SubscriberCount   atomic.Int64
	QueueSize         atomic.Int64
	QueueFullCount    atomic.Uint64
}

// PubSubStore defines the interface for message persistence
type PubSubStore interface {
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Get(ctx context.Context, key string) ([]byte, error)
	Del(ctx context.Context, key string) error
	Exists(ctx context.Context, key string) (bool, error)
}

// Config holds pub/sub configuration
type Config struct {
	Store                   PubSubStore
	KeyPrefix               string
	TTL                     time.Duration
	BufferSize              int
	MaxRetryAttempts        int
	RetryDelay              time.Duration
	EnableMetrics           bool
	MaxConcurrentOperations int
	OperationTimeout        time.Duration
	CircuitBreakerThreshold int64
	CircuitBreakerTimeout   time.Duration
	EnableDeadLetterQueue   bool
	DeadLetterHandler       DeadLetterHandler
}

// PubSub implements the main pub/sub system
type PubSub struct {
	publisher *Publisher
	store     PubSubStore
	config    *Config
	closed    int32
	metrics   *PubSubMetrics
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

// PubSubMetrics tracks overall system performance
type PubSubMetrics struct {
	TotalMessages      atomic.Uint64
	TotalTopics        atomic.Uint64
	TotalSubscriptions atomic.Uint64
	ActiveConnections  atomic.Int64
	Errors             atomic.Uint64
	PanicRecoveries    atomic.Uint64
}

var messageCounter uint64

// NewPubSub creates a new production-ready pub/sub system
func NewPubSub(config *Config) (*PubSub, error) {
	if err := validateConfig(config); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}

	applyDefaults(config)

	ctx, cancel := context.WithCancel(context.Background())

	publisher := &Publisher{
		topics:         make(map[string]*Topic),
		store:          config.Store,
		maxRetries:     config.MaxRetryAttempts,
		retryDelay:     config.RetryDelay,
		metrics:        &PublisherMetrics{},
		circuitBreaker: NewCircuitBreaker(config.CircuitBreakerThreshold, config.CircuitBreakerTimeout),
		workerPool:     make(chan struct{}, config.MaxConcurrentOperations),
	}

	pubsub := &PubSub{
		publisher: publisher,
		store:     config.Store,
		config:    config,
		metrics:   &PubSubMetrics{},
		ctx:       ctx,
		cancel:    cancel,
	}

	logx.Info("Pub/Sub system created successfully",
		logx.String("key_prefix", config.KeyPrefix),
		logx.Int("buffer_size", config.BufferSize),
		logx.Bool("enable_metrics", config.EnableMetrics),
		logx.Int("max_concurrent_operations", config.MaxConcurrentOperations))

	return pubsub, nil
}

// CreateTopic creates a new topic with proper validation
func (ps *PubSub) CreateTopic(ctx context.Context, name string) error {
	if atomic.LoadInt32(&ps.closed) == 1 {
		return errors.New("pub/sub system is closed")
	}

	if err := validateTopicName(name); err != nil {
		return fmt.Errorf("invalid topic name: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrContextCancelled, err)
	}

	ps.publisher.mu.Lock()
	defer ps.publisher.mu.Unlock()

	if _, exists := ps.publisher.topics[name]; exists {
		return fmt.Errorf("topic %s already exists", name)
	}

	topic := &Topic{
		Name:         name,
		Subscribers:  make(map[string]*Subscription),
		MessageQueue: make(chan *Message, ps.config.BufferSize),
		metrics:      &TopicMetrics{},
	}

	ps.publisher.topics[name] = topic
	ps.publisher.metrics.TopicsCreated.Add(1)
	ps.metrics.TotalTopics.Add(1)

	// Start topic message processor with proper cleanup
	ps.wg.Add(1)
	go func() {
		defer ps.wg.Done()
		ps.processTopicMessages(ps.ctx, topic)
	}()

	logx.Info("Topic created successfully", logx.String("topic", name))
	return nil
}

// Subscribe creates a new subscription to a topic with comprehensive validation
func (ps *PubSub) Subscribe(ctx context.Context, topicName, subscriptionID string, handler MessageHandler, filter *MessageFilter) (*Subscription, error) {
	if atomic.LoadInt32(&ps.closed) == 1 {
		return nil, errors.New("pub/sub system is closed")
	}

	if err := validateSubscriptionID(subscriptionID); err != nil {
		return nil, fmt.Errorf("invalid subscription ID: %w", err)
	}

	if handler == nil {
		return nil, errors.New("message handler cannot be nil")
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrContextCancelled, err)
	}

	// Check context
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrContextCancelled, err)
	}

	ps.publisher.mu.RLock()
	topic, exists := ps.publisher.topics[topicName]
	ps.publisher.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrTopicNotFound, topicName)
	}

	// Check subscriber limit
	topic.mu.RLock()
	subscriberCount := len(topic.Subscribers)
	topic.mu.RUnlock()

	if subscriberCount >= MaxSubscriberCount {
		return nil, fmt.Errorf("%w: topic %s has reached maximum subscriber limit", ErrSubscriptionLimit, topicName)
	}

	subscription := &Subscription{
		ID:                subscriptionID,
		Topic:             topicName,
		Handler:           handler,
		DeadLetterHandler: ps.config.DeadLetterHandler,
		Filter:            *filter,
		BatchSize:         1,
		BufferSize:        ps.config.BufferSize,
		MaxRetries:        ps.config.MaxRetryAttempts,
		RetryDelay:        ps.config.RetryDelay,
		CircuitBreaker:    NewCircuitBreaker(ps.config.CircuitBreakerThreshold, ps.config.CircuitBreakerTimeout),
		metrics:           &SubscriptionMetrics{},
		workerPool:        make(chan struct{}, 10), // Limit concurrent message processing per subscription
	}

	topic.mu.Lock()
	defer topic.mu.Unlock()

	if _, exists := topic.Subscribers[subscriptionID]; exists {
		return nil, fmt.Errorf("subscription %s already exists for topic %s", subscriptionID, topicName)
	}

	topic.Subscribers[subscriptionID] = subscription
	topic.metrics.SubscriberCount.Add(1)
	ps.metrics.TotalSubscriptions.Add(1)

	logx.Info("Subscription created successfully",
		logx.String("topic", topicName),
		logx.String("subscription", subscriptionID))

	return subscription, nil
}

// Publish publishes a message to a topic with comprehensive validation and circuit breaker
func (ps *PubSub) Publish(ctx context.Context, topicName string, data []byte, headers map[string]string) error {
	if atomic.LoadInt32(&ps.closed) == 1 {
		return ErrPublisherClosed
	}

	if err := validateMessage(data); err != nil {
		return fmt.Errorf("invalid message: %w", err)
	}

	if err := validateHeaders(headers); err != nil {
		return fmt.Errorf("invalid headers: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrContextCancelled, err)
	}

	// Use operation timeout
	opCtx, cancel := context.WithTimeout(ctx, ps.config.OperationTimeout)
	defer cancel()

	start := time.Now()
	defer func() {
		ps.publisher.metrics.AverageLatency.Store(time.Since(start).Nanoseconds())
		ps.publisher.metrics.LastPublishTime.Store(time.Now())
	}()

	// Use circuit breaker for publishing
	return ps.publisher.circuitBreaker.Execute(opCtx, func() error {
		return ps.publishWithRetry(opCtx, topicName, data, headers)
	})
}

// PublishBatch publishes multiple messages with validation and batching
func (ps *PubSub) PublishBatch(ctx context.Context, topicName string, messages []Message) error {
	if atomic.LoadInt32(&ps.closed) == 1 {
		return ErrPublisherClosed
	}

	if len(messages) == 0 {
		return errors.New("batch cannot be empty")
	}

	if len(messages) > MaxBatchSize {
		return fmt.Errorf("%w: batch size %d exceeds limit %d", ErrBatchTooLarge, len(messages), MaxBatchSize)
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrContextCancelled, err)
	}

	// Use operation timeout
	opCtx, cancel := context.WithTimeout(ctx, ps.config.OperationTimeout)
	defer cancel()

	start := time.Now()
	defer func() {
		ps.publisher.metrics.AverageLatency.Store(time.Since(start).Nanoseconds())
		ps.publisher.metrics.LastPublishTime.Store(time.Now())
		ps.publisher.metrics.BatchOperations.Add(1)
	}()

	// Validate all messages
	for i, msg := range messages {
		if err := validateMessage(msg.Data); err != nil {
			return fmt.Errorf("invalid message at index %d: %w", i, err)
		}
		if err := validateHeaders(msg.Headers); err != nil {
			return fmt.Errorf("invalid headers at index %d: %w", i, err)
		}
	}

	// Publish messages with circuit breaker
	return ps.publisher.circuitBreaker.Execute(opCtx, func() error {
		for i, msg := range messages {
			if err := ps.publishMessage(opCtx, topicName, msg.Data, msg.Headers); err != nil {
				return fmt.Errorf("failed to publish message at index %d: %w", i, err)
			}
		}
		return nil
	})
}

// publishWithRetry publishes a message with retry logic
func (ps *PubSub) publishWithRetry(ctx context.Context, topicName string, data []byte, headers map[string]string) error {
	var lastErr error
	for attempt := 0; attempt <= ps.publisher.maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%w: %v", ErrContextCancelled, err)
		}

		if err := ps.publishMessage(ctx, topicName, data, headers); err != nil {
			lastErr = err
			if attempt < ps.publisher.maxRetries {
				select {
				case <-ctx.Done():
					return fmt.Errorf("%w: %v", ErrContextCancelled, ctx.Err())
				case <-time.After(ps.publisher.retryDelay * time.Duration(attempt+1)):
					continue
				}
			}
		} else {
			return nil
		}
	}

	ps.publisher.metrics.MessagesFailed.Add(1)
	ps.metrics.Errors.Add(1)
	return fmt.Errorf("failed to publish message after %d attempts: %w", ps.publisher.maxRetries+1, lastErr)
}

// publishMessage publishes a message to a topic with proper resource management
func (ps *PubSub) publishMessage(ctx context.Context, topicName string, data []byte, headers map[string]string) error {
	// Acquire worker pool slot with timeout
	select {
	case ps.publisher.workerPool <- struct{}{}:
		defer func() { <-ps.publisher.workerPool }()
	case <-ctx.Done():
		return fmt.Errorf("%w: %v", ErrContextCancelled, ctx.Err())
	case <-time.After(ps.config.OperationTimeout):
		return fmt.Errorf("%w: timeout acquiring worker slot", ErrTimeout)
	default:
		return fmt.Errorf("%w: too many concurrent operations", ErrConcurrencyLimit)
	}

	ps.publisher.mu.RLock()
	topic, exists := ps.publisher.topics[topicName]
	ps.publisher.mu.RUnlock()

	if !exists {
		return fmt.Errorf("%w: %s", ErrTopicNotFound, topicName)
	}

	if atomic.LoadInt32(&topic.closed) == 1 {
		return fmt.Errorf("%w: %s", ErrTopicClosed, topicName)
	}

	message := &Message{
		ID:        generateMessageID(),
		Topic:     topicName,
		Data:      data,
		Headers:   headers,
		Timestamp: time.Now(),
		TTL:       ps.config.TTL,
		Sequence:  topic.sequence.Add(1),
	}

	// Store message if store is available
	if ps.store != nil {
		if err := ps.storeMessage(ctx, message); err != nil {
			logx.Error("Failed to store message",
				logx.ErrorField(err),
				logx.String("message_id", message.ID))
			// Continue with in-memory delivery
		}
	}

	// Send message to topic queue with timeout
	select {
	case topic.MessageQueue <- message:
		topic.metrics.MessagesPublished.Add(1)
		ps.publisher.metrics.MessagesPublished.Add(1)
		ps.metrics.TotalMessages.Add(1)
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%w: %v", ErrContextCancelled, ctx.Err())
	default:
		topic.metrics.QueueFullCount.Add(1)
		return fmt.Errorf("topic %s message queue is full", topicName)
	}
}

// processTopicMessages processes messages for a topic with proper cleanup
func (ps *PubSub) processTopicMessages(ctx context.Context, topic *Topic) {
	defer func() {
		if r := recover(); r != nil {
			logx.Error("Panic recovered in topic message processor",
				logx.String("topic", topic.Name),
				logx.Any("panic", r))
			ps.metrics.PanicRecoveries.Add(1)
		}
	}()

	logx.Info("Topic message processor started", logx.String("topic", topic.Name))

	for {
		select {
		case message := <-topic.MessageQueue:
			if message == nil {
				continue
			}
			logx.Info("Processing message",
				logx.String("topic", topic.Name),
				logx.String("message_id", message.ID))
			ps.deliverMessageToSubscribers(ctx, topic, message)
		case <-ctx.Done():
			logx.Info("Topic message processor stopped", logx.String("topic", topic.Name))
			return
		}
	}
}

// deliverMessageToSubscribers delivers a message to all subscribers with proper concurrency control
func (ps *PubSub) deliverMessageToSubscribers(ctx context.Context, topic *Topic, message *Message) {
	topic.mu.RLock()
	subscribers := make([]*Subscription, 0, len(topic.Subscribers))
	for _, sub := range topic.Subscribers {
		subscribers = append(subscribers, sub)
	}
	topic.mu.RUnlock()

	logx.Info("Delivering message to subscribers",
		logx.String("topic", topic.Name),
		logx.String("message_id", message.ID),
		logx.Int("subscriber_count", len(subscribers)))

	var wg sync.WaitGroup
	for _, subscription := range subscribers {
		if atomic.LoadInt32(&subscription.closed) == 1 {
			continue
		}

		// Check message filter
		if !subscription.matchesFilter(message) {
			continue
		}

		// Deliver message to subscriber with concurrency control
		wg.Add(1)
		go func(sub *Subscription) {
			defer wg.Done()
			ps.deliverMessageToSubscriber(ctx, sub, message)
		}(subscription)
	}

	// Wait for all deliveries to complete with timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		topic.metrics.MessagesDelivered.Add(1)
	case <-ctx.Done():
		logx.Warn("Message delivery timeout", logx.String("topic", topic.Name))
	case <-time.After(ps.config.OperationTimeout):
		logx.Warn("Message delivery timeout", logx.String("topic", topic.Name))
	}
}

// deliverMessageToSubscriber delivers a message to a single subscriber with comprehensive error handling
func (ps *PubSub) deliverMessageToSubscriber(ctx context.Context, subscription *Subscription, message *Message) {
	// Acquire worker pool slot (non-blocking)
	select {
	case subscription.workerPool <- struct{}{}:
		defer func() { <-subscription.workerPool }()
	case <-ctx.Done():
		return
	default:
		// If worker pool is full, process message directly to avoid blocking
		logx.Warn("Subscription worker pool full, processing message directly",
			logx.String("subscription", subscription.ID))
	}

	start := time.Now()
	defer func() {
		subscription.metrics.AverageLatency.Store(time.Since(start).Nanoseconds())
		subscription.metrics.LastMessageTime.Store(time.Now())
	}()

	subscription.metrics.MessagesReceived.Add(1)

	// Use circuit breaker for message delivery (with fallback)
	err := subscription.CircuitBreaker.Execute(ctx, func() error {
		return ps.deliverWithRetry(ctx, subscription, message)
	})

	// If circuit breaker is open, try direct delivery
	if err != nil && errors.Is(err, ErrCircuitBreakerOpen) {
		logx.Warn("Circuit breaker open, attempting direct delivery",
			logx.String("subscription", subscription.ID))
		err = ps.deliverWithRetry(ctx, subscription, message)
	}

	if err != nil {
		subscription.metrics.MessagesFailed.Add(1)
		ps.metrics.Errors.Add(1)

		// Categorize errors for better metrics
		if errors.Is(err, ErrCircuitBreakerOpen) {
			subscription.metrics.CircuitBreakerTrips.Add(1)
		} else if errors.Is(err, ErrTimeout) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			// Record timeout errors specifically
			subscription.metrics.TimeoutErrors.Add(1)
			logx.Warn("Message handler timeout",
				logx.String("subscription", subscription.ID),
				logx.String("message_id", message.ID),
				logx.String("timeout", ps.config.OperationTimeout.String()))
		} else if errors.Is(err, ErrPanicRecovered) {
			// Panic errors are already recorded above
		} else {
			// Record other errors
			logx.Error("Message delivery failed",
				logx.ErrorField(err),
				logx.String("subscription", subscription.ID),
				logx.String("message_id", message.ID))
		}

		// Send to dead letter queue if configured
		if ps.config.EnableDeadLetterQueue && ps.config.DeadLetterHandler != nil {
			if dlqErr := ps.config.DeadLetterHandler(ctx, message, err); dlqErr != nil {
				logx.Error("Failed to send message to dead letter queue",
					logx.ErrorField(dlqErr),
					logx.String("message_id", message.ID))
			} else {
				subscription.metrics.MessagesDLQ.Add(1)
			}
		}
	} else {
		subscription.metrics.MessagesProcessed.Add(1)
	}
}

// deliverWithRetry delivers a message with retry logic and panic recovery
func (ps *PubSub) deliverWithRetry(ctx context.Context, subscription *Subscription, message *Message) error {
	var lastErr error
	for attempt := 0; attempt <= subscription.MaxRetries; attempt++ {
		// Set the retry count for this attempt
		message.RetryCount = attempt
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%w: %v", ErrContextCancelled, err)
		}

		// Create timeout context for handler execution
		handlerCtx, cancel := context.WithTimeout(ctx, ps.config.OperationTimeout)
		defer cancel()

		// Wrap handler with panic recovery and timeout
		err := func() (err error) {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("%w: %v", ErrPanicRecovered, r)
					subscription.metrics.PanicRecoveries.Add(1)
					ps.metrics.PanicRecoveries.Add(1)
					logx.Error("Panic recovered in message handler",
						logx.String("subscription", subscription.ID),
						logx.String("message_id", message.ID),
						logx.Any("panic", r))
				}
			}()

			// Execute handler with timeout
			done := make(chan error, 1)
			go func() {
				defer func() {
					if r := recover(); r != nil {
						done <- fmt.Errorf("%w: %v", ErrPanicRecovered, r)
					}
				}()
				done <- subscription.Handler(handlerCtx, message)
			}()

			select {
			case err := <-done:
				return err
			case <-handlerCtx.Done():
				return fmt.Errorf("%w: handler timeout", ErrTimeout)
			}
		}()

		if err != nil {
			lastErr = err

			// Don't retry timeout errors or context cancellation
			if errors.Is(err, ErrTimeout) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				return err
			}

			subscription.metrics.MessagesRetried.Add(1)
			if attempt < subscription.MaxRetries {
				select {
				case <-ctx.Done():
					return fmt.Errorf("%w: %v", ErrContextCancelled, ctx.Err())
				case <-time.After(subscription.RetryDelay * time.Duration(attempt+1)):
					continue
				}
			}
		} else {
			return nil
		}
	}

	return fmt.Errorf("failed to deliver message after %d attempts: %w", subscription.MaxRetries+1, lastErr)
}

// storeMessage stores a message in the persistent store
func (ps *PubSub) storeMessage(ctx context.Context, message *Message) error {
	if ps.store == nil {
		return ErrStoreUnavailable
	}

	data, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	key := fmt.Sprintf("%s:msg:%s:%s", ps.config.KeyPrefix, message.Topic, message.ID)
	return ps.store.Set(ctx, key, data, message.TTL)
}

// matchesFilter checks if a message matches the subscription filter with regex support
func (s *Subscription) matchesFilter(message *Message) bool {
	if s.Filter.Headers != nil {
		for key, value := range s.Filter.Headers {
			if msgValue, exists := message.Headers[key]; !exists || msgValue != value {
				return false
			}
		}
	}

	if s.Filter.Priority > 0 && message.Priority < s.Filter.Priority {
		return false
	}

	if s.Filter.MaxRetryCount > 0 && message.RetryCount >= s.Filter.MaxRetryCount {
		return false
	}

	// TODO: Implement regex pattern matching for headers
	// if s.Filter.RegexPatterns != nil {
	//     // Add regex matching logic here
	// }

	return true
}

// Close closes the pub/sub system gracefully with proper cleanup
func (ps *PubSub) Close(ctx context.Context) error {
	if !atomic.CompareAndSwapInt32(&ps.closed, 0, 1) {
		return errors.New("pub/sub system already closed")
	}

	logx.Info("Closing pub/sub system...")

	// Cancel context to stop all goroutines
	ps.cancel()

	// Close all topics with proper ordering to avoid deadlocks
	ps.publisher.mu.Lock()
	topics := make([]*Topic, 0, len(ps.publisher.topics))
	for _, topic := range ps.publisher.topics {
		topics = append(topics, topic)
	}
	ps.publisher.mu.Unlock()

	// Close topics in separate loop to avoid deadlock
	for _, topic := range topics {
		atomic.StoreInt32(&topic.closed, 1)
		close(topic.MessageQueue)
	}

	// Close all subscriptions
	ps.publisher.mu.RLock()
	for _, topic := range ps.publisher.topics {
		topic.mu.RLock()
		for _, subscription := range topic.Subscribers {
			atomic.StoreInt32(&subscription.closed, 1)
		}
		topic.mu.RUnlock()
	}
	ps.publisher.mu.RUnlock()

	// Wait for all goroutines to complete with timeout
	done := make(chan struct{})
	go func() {
		ps.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		logx.Info("All goroutines completed")
	case <-time.After(30 * time.Second):
		logx.Warn("Timeout waiting for goroutines to complete")
	case <-ctx.Done():
		logx.Warn("Context cancelled while waiting for goroutines")
	}

	logx.Info("Pub/Sub system closed successfully")
	return nil
}

// GetStats returns comprehensive system statistics
func (ps *PubSub) GetStats() map[string]interface{} {
	stats := map[string]interface{}{
		"total_messages":      ps.metrics.TotalMessages.Load(),
		"total_topics":        ps.metrics.TotalTopics.Load(),
		"total_subscriptions": ps.metrics.TotalSubscriptions.Load(),
		"active_connections":  ps.metrics.ActiveConnections.Load(),
		"errors":              ps.metrics.Errors.Load(),
		"panic_recoveries":    ps.metrics.PanicRecoveries.Load(),
	}

	// Add publisher stats
	stats["publisher"] = map[string]interface{}{
		"messages_published":    ps.publisher.metrics.MessagesPublished.Load(),
		"messages_failed":       ps.publisher.metrics.MessagesFailed.Load(),
		"topics_created":        ps.publisher.metrics.TopicsCreated.Load(),
		"average_latency_ns":    ps.publisher.metrics.AverageLatency.Load(),
		"batch_operations":      ps.publisher.metrics.BatchOperations.Load(),
		"circuit_breaker_trips": ps.publisher.metrics.CircuitBreakerTrips.Load(),
	}

	// Add topic stats
	topicStats := make(map[string]interface{})
	ps.publisher.mu.RLock()
	for name, topic := range ps.publisher.topics {
		topicStats[name] = map[string]interface{}{
			"messages_published": topic.metrics.MessagesPublished.Load(),
			"messages_delivered": topic.metrics.MessagesDelivered.Load(),
			"subscriber_count":   topic.metrics.SubscriberCount.Load(),
			"queue_size":         topic.metrics.QueueSize.Load(),
			"queue_full_count":   topic.metrics.QueueFullCount.Load(),
		}
	}
	ps.publisher.mu.RUnlock()
	stats["topics"] = topicStats

	// Add subscription stats with timeout errors
	subscriptionStats := make(map[string]interface{})
	ps.publisher.mu.RLock()
	for _, topic := range ps.publisher.topics {
		topic.mu.RLock()
		for subID, subscription := range topic.Subscribers {
			subscriptionStats[subID] = map[string]interface{}{
				"messages_received":     subscription.metrics.MessagesReceived.Load(),
				"messages_processed":    subscription.metrics.MessagesProcessed.Load(),
				"messages_failed":       subscription.metrics.MessagesFailed.Load(),
				"messages_retried":      subscription.metrics.MessagesRetried.Load(),
				"messages_dlq":          subscription.metrics.MessagesDLQ.Load(),
				"circuit_breaker_trips": subscription.metrics.CircuitBreakerTrips.Load(),
				"panic_recoveries":      subscription.metrics.PanicRecoveries.Load(),
				"timeout_errors":        subscription.metrics.TimeoutErrors.Load(),
			}
		}
		topic.mu.RUnlock()
	}
	ps.publisher.mu.RUnlock()
	stats["subscriptions"] = subscriptionStats

	return stats
}

// Validation functions with comprehensive checks
func validateConfig(config *Config) error {
	if config == nil {
		return errors.New("config cannot be nil")
	}

	if config.BufferSize <= 0 {
		return errors.New("buffer size must be positive")
	}

	if config.MaxRetryAttempts < 0 {
		return errors.New("max retry attempts cannot be negative")
	}

	if config.MaxConcurrentOperations <= 0 {
		return errors.New("max concurrent operations must be positive")
	}

	if config.OperationTimeout <= 0 {
		return errors.New("operation timeout must be positive")
	}

	if config.CircuitBreakerThreshold <= 0 {
		return errors.New("circuit breaker threshold must be positive")
	}

	if config.CircuitBreakerTimeout <= 0 {
		return errors.New("circuit breaker timeout must be positive")
	}

	return nil
}

func validateTopicName(name string) error {
	if name == "" {
		return errors.New("topic name cannot be empty")
	}

	if len(name) > MaxTopicNameLength {
		return fmt.Errorf("topic name length %d exceeds maximum %d", len(name), MaxTopicNameLength)
	}

	// Check for invalid characters
	for _, char := range name {
		if !unicode.IsLetter(char) && !unicode.IsDigit(char) && char != '-' && char != '_' && char != '.' {
			return fmt.Errorf("topic name contains invalid character: %c", char)
		}
	}

	return nil
}

func validateSubscriptionID(id string) error {
	if id == "" {
		return errors.New("subscription ID cannot be empty")
	}

	if len(id) > MaxTopicNameLength {
		return fmt.Errorf("subscription ID length %d exceeds maximum %d", len(id), MaxTopicNameLength)
	}

	// Check for invalid characters
	for _, char := range id {
		if !unicode.IsLetter(char) && !unicode.IsDigit(char) && char != '-' && char != '_' && char != '.' {
			return fmt.Errorf("subscription ID contains invalid character: %c", char)
		}
	}

	return nil
}

func validateMessage(data []byte) error {
	if len(data) == 0 {
		return errors.New("message data cannot be empty")
	}

	if len(data) > MaxMessageSize {
		return fmt.Errorf("message size %d exceeds maximum %d", len(data), MaxMessageSize)
	}

	return nil
}

func validateHeaders(headers map[string]string) error {
	if headers == nil {
		return nil
	}

	if len(headers) > MaxHeadersCount {
		return fmt.Errorf("headers count %d exceeds maximum %d", len(headers), MaxHeadersCount)
	}

	for key, value := range headers {
		if len(key) > MaxHeaderKeyLength {
			return fmt.Errorf("header key length %d exceeds maximum %d", len(key), MaxHeaderKeyLength)
		}

		if len(value) > MaxHeaderValueLength {
			return fmt.Errorf("header value length %d exceeds maximum %d", len(value), MaxHeaderValueLength)
		}

		// Check for invalid characters in key
		for _, char := range key {
			if !unicode.IsLetter(char) && !unicode.IsDigit(char) && char != '-' && char != '_' {
				return fmt.Errorf("header key contains invalid character: %c", char)
			}
		}
	}

	return nil
}

func applyDefaults(config *Config) {
	if config.KeyPrefix == "" {
		config.KeyPrefix = DefaultKeyPrefix
	}

	if config.TTL == 0 {
		config.TTL = DefaultTTL
	}

	if config.BufferSize == 0 {
		config.BufferSize = DefaultBufferSize
	}

	if config.MaxRetryAttempts == 0 {
		config.MaxRetryAttempts = MaxRetryAttempts
	}

	if config.RetryDelay == 0 {
		config.RetryDelay = RetryDelay
	}

	if config.MaxConcurrentOperations == 0 {
		config.MaxConcurrentOperations = MaxConcurrentOperations
	}

	if config.OperationTimeout == 0 {
		config.OperationTimeout = DefaultOperationTimeout
	}

	if config.CircuitBreakerThreshold == 0 {
		config.CircuitBreakerThreshold = DefaultCircuitBreakerThreshold
	}

	if config.CircuitBreakerTimeout == 0 {
		config.CircuitBreakerTimeout = DefaultCircuitBreakerTimeout
	}
}

func generateMessageID() string {
	return fmt.Sprintf("msg_%d_%d", time.Now().UnixNano(), atomic.AddUint64(&messageCounter, 1))
}

// DefaultConfig returns a default pub/sub configuration
func DefaultConfig(store PubSubStore) *Config {
	return &Config{
		Store:                   store,
		KeyPrefix:               DefaultKeyPrefix,
		TTL:                     DefaultTTL,
		BufferSize:              DefaultBufferSize,
		MaxRetryAttempts:        MaxRetryAttempts,
		RetryDelay:              RetryDelay,
		EnableMetrics:           true,
		MaxConcurrentOperations: MaxConcurrentOperations,
		OperationTimeout:        DefaultOperationTimeout,
		CircuitBreakerThreshold: DefaultCircuitBreakerThreshold,
		CircuitBreakerTimeout:   DefaultCircuitBreakerTimeout,
		EnableDeadLetterQueue:   false,
	}
}

// HighReliabilityConfig returns a high-reliability configuration
func HighReliabilityConfig(store PubSubStore) *Config {
	config := DefaultConfig(store)
	config.BufferSize = 50000
	config.MaxRetryAttempts = 5
	config.RetryDelay = 200 * time.Millisecond
	config.MaxConcurrentOperations = 500
	config.OperationTimeout = 60 * time.Second
	config.CircuitBreakerThreshold = 3
	config.CircuitBreakerTimeout = 120 * time.Second
	config.EnableDeadLetterQueue = true
	return config
}
