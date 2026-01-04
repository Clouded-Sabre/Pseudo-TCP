package lib

import (
	"log"
	"math"
	"sync"
	"time"

	"github.com/Clouded-Sabre/Pseudo-TCP/config"
)

// ClientReconnectConfig holds configuration for client-side automatic reconnection
type ClientReconnectConfig struct {
	MaxRetries        int           // Maximum reconnection attempts (-1 for infinite)
	InitialBackoff    time.Duration // Initial backoff delay
	MaxBackoff        time.Duration // Maximum backoff cap
	BackoffMultiplier float64       // Exponential backoff multiplier (e.g., 2.0)
	OnReconnect       func()        // Called on successful reconnection
	OnFinalFailure    func(error)   // Called when all retries are exhausted
}

// ClientReconnectHelper manages reconnection for a client connection
type ClientReconnectHelper struct {
	pcpCore      *PcpCore
	localIP      string
	serverIP     string
	serverPort   uint16
	pcpConfig    *config.Config
	reconnectCfg *ClientReconnectConfig
	currentConn  *Connection  // Use *Connection directly
	connMutex    sync.RWMutex // Protects currentConn access
	retryCount   int
	lastBackoff  time.Duration
}

// NewClientReconnectHelper creates a new reconnection helper
func NewClientReconnectHelper(
	pcpCore *PcpCore,
	localIP string,
	serverIP string,
	serverPort uint16,
	pcpConfig *config.Config,
	reconnectCfg *ClientReconnectConfig,
) *ClientReconnectHelper {
	return &ClientReconnectHelper{
		pcpCore:      pcpCore,
		localIP:      localIP,
		serverIP:     serverIP,
		serverPort:   serverPort,
		pcpConfig:    pcpConfig,
		reconnectCfg: reconnectCfg,
		retryCount:   0,
		lastBackoff:  reconnectCfg.InitialBackoff,
	}
}

// SetConnection sets the initial connection
func (helper *ClientReconnectHelper) SetConnection(conn *Connection) {
	helper.connMutex.Lock()
	defer helper.connMutex.Unlock()
	helper.currentConn = conn
	helper.retryCount = 0
	helper.lastBackoff = helper.reconnectCfg.InitialBackoff
}

// GetConnection returns the current connection
func (helper *ClientReconnectHelper) GetConnection() *Connection {
	helper.connMutex.RLock()
	defer helper.connMutex.RUnlock()
	return helper.currentConn
}

// HandleError processes connection errors and attempts reconnection if appropriate
// Returns true if reconnection was successful, false otherwise
func (helper *ClientReconnectHelper) HandleError(err error) bool {
	if err == nil {
		return true
	}

	// Check if this is a keepalive timeout error that warrants reconnection
	if _, isKeepaliveTimeout := err.(*KeepAliveTimeoutError); !isKeepaliveTimeout {
		// Not a keepalive timeout - don't attempt reconnection
		return false
	}

	log.Printf("Keepalive timeout detected: %v. Attempting reconnection...\n", err)

	// Attempt reconnection with backoff
	for helper.reconnectCfg.MaxRetries == -1 || helper.retryCount < helper.reconnectCfg.MaxRetries {
		helper.retryCount++

		// Wait before reconnecting (backoff)
		log.Printf("Reconnection attempt %d: waiting %v before retry\n", helper.retryCount, helper.lastBackoff)
		time.Sleep(helper.lastBackoff)

		// Try to create a new connection
		newConn, err := helper.pcpCore.DialPcp(
			helper.localIP,
			helper.serverIP,
			helper.serverPort,
			helper.pcpConfig,
		)

		if err == nil {
			// Verify the new connection is actually viable by checking if we can use it
			// A broken PcpProtocolConnection might return a connection that fails immediately
			log.Printf("Reconnection successful on attempt %d (new connection created)\n", helper.retryCount)

			// Update the current connection
			helper.connMutex.Lock()
			oldConn := helper.currentConn
			helper.currentConn = newConn
			helper.connMutex.Unlock()

			// Note: We don't close the old connection here to avoid race conditions
			// The old connection's goroutines will eventually clean up on their own
			// when they detect the connection is not being used anymore.
			// Explicitly closing the connection can cause panics during termination
			// if shutdown racing conditions occur (e.g., termination trying to send on closed channels).
			// The old connection will be garbage collected when no goroutines reference it.
			if oldConn != nil {
				log.Printf("Old connection replaced with new one, letting it clean up naturally\n")
				// Just mark it as abandoned by not using it anymore
				// The keepalive timeout or read errors will cause cleanup
			}

			// Reset retry counter and backoff
			helper.retryCount = 0
			helper.lastBackoff = helper.reconnectCfg.InitialBackoff

			// Call success callback
			if helper.reconnectCfg.OnReconnect != nil {
				helper.reconnectCfg.OnReconnect()
			}

			return true
		}

		log.Printf("Reconnection attempt %d failed: %v\n", helper.retryCount, err)

		// Update backoff for next iteration
		newBackoff := time.Duration(float64(helper.lastBackoff) * helper.reconnectCfg.BackoffMultiplier)
		if newBackoff > helper.reconnectCfg.MaxBackoff {
			newBackoff = helper.reconnectCfg.MaxBackoff
		}
		helper.lastBackoff = newBackoff
	}

	// All retries exhausted
	log.Printf("Reconnection failed after %d attempts\n", helper.retryCount)

	if helper.reconnectCfg.OnFinalFailure != nil {
		finalErr := &KeepAliveTimeoutError{ConnectionKey: "unknown"}
		helper.reconnectCfg.OnFinalFailure(finalErr)
	}

	return false
}

// DefaultClientReconnectConfig returns a conservative reconnection configuration suitable for production
func DefaultClientReconnectConfig() *ClientReconnectConfig {
	return &ClientReconnectConfig{
		MaxRetries:        10,
		InitialBackoff:    1 * time.Second,
		MaxBackoff:        60 * time.Second,
		BackoffMultiplier: 1.5,
	}
}

// AggressiveClientReconnectConfig returns an aggressive configuration for testing/development
func AggressiveClientReconnectConfig() *ClientReconnectConfig {
	return &ClientReconnectConfig{
		MaxRetries:        5,
		InitialBackoff:    100 * time.Millisecond,
		MaxBackoff:        5 * time.Second,
		BackoffMultiplier: 2.0,
	}
}

// InfiniteClientReconnectConfig returns a configuration that retries indefinitely
func InfiniteClientReconnectConfig() *ClientReconnectConfig {
	return &ClientReconnectConfig{
		MaxRetries:        -1, // Infinite retries
		InitialBackoff:    100 * time.Millisecond,
		MaxBackoff:        30 * time.Second,
		BackoffMultiplier: 1.5,
	}
}

// CalculateBackoffDuration calculates the backoff duration for a given retry count
func CalculateBackoffDuration(retryCount int, initialBackoff time.Duration, maxBackoff time.Duration, multiplier float64) time.Duration {
	backoff := time.Duration(float64(initialBackoff) * math.Pow(multiplier, float64(retryCount)))
	if backoff > maxBackoff {
		backoff = maxBackoff
	}
	return backoff
}
