package lib

import (
	"context"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net"
	"sync"
	"time"

	"github.com/Clouded-Sabre/Pseudo-TCP/config"
)

// ReconnectConfig defines the reconnection behavior
type ReconnectConfig struct {
	Enabled           bool          // Enable auto-reconnection
	MaxRetries        int           // Maximum number of reconnection attempts (-1 for infinite)
	InitialBackoff    time.Duration // Initial backoff duration (e.g., 100ms)
	MaxBackoff        time.Duration // Maximum backoff duration (e.g., 30s)
	BackoffMultiplier float64       // Backoff multiplier for exponential backoff (e.g., 1.5 or 2.0)
	OnReconnect       func()        // Optional callback when reconnection succeeds
	OnFinalFailure    func()        // Optional callback when all reconnection attempts fail
}

// DefaultReconnectConfig returns a sensible default configuration
func DefaultReconnectConfig() *ReconnectConfig {
	return &ReconnectConfig{
		Enabled:           true,
		MaxRetries:        10,
		InitialBackoff:    100 * time.Millisecond,
		MaxBackoff:        30 * time.Second,
		BackoffMultiplier: 2.0,
	}
}

// DialConfig stores the parameters needed to recreate a connection
type DialConfig struct {
	PcpCore    *PcpCore
	LocalIP    string
	ServerIP   string
	ServerPort uint16
	PcpConfig  *config.Config
}

// ReconnectingConnection wraps a Connection and provides automatic reconnection
// on connection failures with exponential backoff strategy.
type ReconnectingConnection struct {
	// Configuration
	reconnectConfig *ReconnectConfig
	dialConfig      *DialConfig // stores dial parameters to recreate connection

	// Connection management
	mu              sync.RWMutex
	currentConn     *Connection
	isClosed        bool
	reconnectCount  int

	// Reconnection tracking
	lastError    error
	lastFailTime time.Time
}

// NewReconnectingConnection creates a new reconnecting connection wrapper
func NewReconnectingConnection(conn *Connection, reconnectConfig *ReconnectConfig,
	dialCfg *DialConfig) *ReconnectingConnection {
	if reconnectConfig == nil {
		reconnectConfig = DefaultReconnectConfig()
	}
	return &ReconnectingConnection{
		reconnectConfig: reconnectConfig,
		dialConfig:      dialCfg,
		currentConn:     conn,
		isClosed:        false,
		reconnectCount:  0,
	}
}

// Read implements the net.Conn Read interface with automatic reconnection
func (rc *ReconnectingConnection) Read(buffer []byte) (int, error) {
	rc.mu.RLock()
	if rc.isClosed {
		rc.mu.RUnlock()
		return 0, io.EOF
	}
	conn := rc.currentConn
	rc.mu.RUnlock()

	n, err := conn.Read(buffer)
	if err == nil {
		return n, nil
	}

	// Check if this is a connection failure that warrants reconnection
	if !rc.shouldReconnect(err) {
		return n, err
	}

	log.Printf("ReconnectingConnection: Read error detected: %v. Attempting reconnection...\n", err)

	// Try to reconnect
	if err := rc.reconnectWithBackoff(context.Background()); err != nil {
		log.Printf("ReconnectingConnection: Reconnection failed: %v\n", err)
		if rc.reconnectConfig.OnFinalFailure != nil {
			rc.reconnectConfig.OnFinalFailure()
		}
		return 0, fmt.Errorf("reconnection failed after %d attempts: %w", rc.reconnectCount, err)
	}

	// Successfully reconnected, retry the read
	log.Println("ReconnectingConnection: Successfully reconnected. Retrying read...")
	return rc.Read(buffer)
}

// Write implements the net.Conn Write interface with automatic reconnection
func (rc *ReconnectingConnection) Write(buffer []byte) (int, error) {
	rc.mu.RLock()
	if rc.isClosed {
		rc.mu.RUnlock()
		return 0, io.EOF
	}
	conn := rc.currentConn
	rc.mu.RUnlock()

	n, err := conn.Write(buffer)
	if err == nil {
		return n, nil
	}

	// Check if this is a connection failure that warrants reconnection
	if !rc.shouldReconnect(err) {
		return n, err
	}

	log.Printf("ReconnectingConnection: Write error detected: %v. Attempting reconnection...\n", err)

	// Try to reconnect
	if err := rc.reconnectWithBackoff(context.Background()); err != nil {
		log.Printf("ReconnectingConnection: Reconnection failed: %v\n", err)
		if rc.reconnectConfig.OnFinalFailure != nil {
			rc.reconnectConfig.OnFinalFailure()
		}
		return 0, fmt.Errorf("reconnection failed after %d attempts: %w", rc.reconnectCount, err)
	}

	// Successfully reconnected, retry the write
	log.Println("ReconnectingConnection: Successfully reconnected. Retrying write...")
	return rc.Write(buffer)
}

// Close implements the net.Conn Close interface
func (rc *ReconnectingConnection) Close() error {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	if rc.isClosed {
		return io.EOF
	}

	rc.isClosed = true
	if rc.currentConn != nil {
		return rc.currentConn.Close()
	}
	return nil
}

// LocalAddr implements the net.Conn LocalAddr interface
func (rc *ReconnectingConnection) LocalAddr() net.Addr {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	if rc.currentConn != nil {
		return rc.currentConn.LocalAddr()
	}
	return nil
}

// RemoteAddr implements the net.Conn RemoteAddr interface
func (rc *ReconnectingConnection) RemoteAddr() net.Addr {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	if rc.currentConn != nil {
		return rc.currentConn.RemoteAddr()
	}
	return nil
}

// SetDeadline implements the net.Conn SetDeadline interface
func (rc *ReconnectingConnection) SetDeadline(t time.Time) error {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	if rc.currentConn != nil {
		return rc.currentConn.SetReadDeadline(t)
	}
	return io.EOF
}

// SetReadDeadline implements the net.Conn SetReadDeadline interface
func (rc *ReconnectingConnection) SetReadDeadline(t time.Time) error {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	if rc.currentConn != nil {
		return rc.currentConn.SetReadDeadline(t)
	}
	return io.EOF
}

// SetWriteDeadline implements the net.Conn SetWriteDeadline interface
func (rc *ReconnectingConnection) SetWriteDeadline(t time.Time) error {
	// SetWriteDeadline is not supported by the underlying Connection
	return nil
}

// shouldReconnect determines if an error warrants attempting reconnection
func (rc *ReconnectingConnection) shouldReconnect(err error) bool {
	if !rc.reconnectConfig.Enabled {
		return false
	}

	if err == nil {
		return false
	}

	// Reconnect on these transient errors
	if err == io.EOF {
		return true
	}
	if err == net.ErrClosed {
		return true
	}

	// Check for connection reset or broken pipe
	if netErr, ok := err.(net.Error); ok {
		// Reconnect on timeout or temporary connection issues
		return netErr.Timeout() || netErr.Temporary()
	}

	// Don't reconnect on other errors (likely permanent)
	return false
}

// reconnectWithBackoff attempts to reconnect with exponential backoff
func (rc *ReconnectingConnection) reconnectWithBackoff(ctx context.Context) error {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	rc.reconnectCount = 0
	var lastErr error

	for {
		// Check if max retries reached
		if rc.reconnectConfig.MaxRetries != -1 && rc.reconnectCount >= rc.reconnectConfig.MaxRetries {
			return fmt.Errorf("max reconnection attempts reached: %w", lastErr)
		}

		// Check context
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Calculate backoff with exponential growth
		backoff := rc.calculateBackoff(rc.reconnectCount)

		log.Printf("ReconnectingConnection: Reconnection attempt %d, waiting %v...\n",
			rc.reconnectCount+1, backoff)

		// Wait with context awareness
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}

		// Attempt reconnection
		newConn, err := rc.dialConfig.PcpCore.DialPcp(
			rc.dialConfig.LocalIP,
			rc.dialConfig.ServerIP,
			rc.dialConfig.ServerPort,
			rc.dialConfig.PcpConfig,
		)

		if err == nil {
			// Successfully reconnected
			rc.currentConn = newConn
			rc.reconnectCount = 0
			rc.lastError = nil
			rc.lastFailTime = time.Time{}

			log.Printf("ReconnectingConnection: Successfully reconnected on attempt %d\n",
				rc.reconnectCount)

			if rc.reconnectConfig.OnReconnect != nil {
				rc.reconnectConfig.OnReconnect()
			}

			return nil
		}

		// Reconnection failed, increment count and retry
		rc.lastError = err
		rc.lastFailTime = time.Now()
		rc.reconnectCount++

		log.Printf("ReconnectingConnection: Reconnection attempt %d failed: %v\n",
			rc.reconnectCount, err)
	}
}

// calculateBackoff calculates the backoff duration using exponential growth with jitter
func (rc *ReconnectingConnection) calculateBackoff(attempt int) time.Duration {
	// Exponential backoff: initial * (multiplier ^ attempt)
	exponentialBackoff := time.Duration(float64(rc.reconnectConfig.InitialBackoff) *
		math.Pow(rc.reconnectConfig.BackoffMultiplier, float64(attempt)))

	// Cap at max backoff
	if exponentialBackoff > rc.reconnectConfig.MaxBackoff {
		exponentialBackoff = rc.reconnectConfig.MaxBackoff
	}

	// Add small jitter (±10%) to prevent thundering herd
	jitterFraction := 0.1
	jitter := time.Duration(
		float64(exponentialBackoff) *
			jitterFraction *
			(2*rand.Float64() - 1.0), // Random value in [-jitterFraction, +jitterFraction]
	)

	return exponentialBackoff + jitter
}

// GetCurrentConnection returns the underlying connection (for advanced usage)
func (rc *ReconnectingConnection) GetCurrentConnection() *Connection {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	return rc.currentConn
}

// GetReconnectStats returns current reconnection statistics
func (rc *ReconnectingConnection) GetReconnectStats() (attempts int, lastErr error, lastFailTime time.Time) {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	return rc.reconnectCount, rc.lastError, rc.lastFailTime
}
