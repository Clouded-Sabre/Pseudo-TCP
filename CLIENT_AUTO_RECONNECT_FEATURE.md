# Client Auto-Reconnection Feature

## Overview

This feature implements automatic client-side reconnection with exponential backoff when keepalive timeouts or connection failures occur. Instead of simply dropping the connection on keepalive failure, the client can now transparently attempt to reconnect with configurable retry logic.

## Branch

**Feature branch:** `feature/client-auto-reconnect`

This feature is implemented as a non-invasive wrapper (`ReconnectingConnection`) around the existing `Connection` type, allowing selective adoption without modifying core connection logic.

## Files Added

1. **`lib/reconnecting_connection.go`** - Core reconnection implementation
   - `ReconnectConfig` - Configuration structure for reconnection behavior
   - `DialConfig` - Parameters needed to recreate a connection
   - `ReconnectingConnection` - Wrapper type implementing `net.Conn` interface

2. **`client/client_with_reconnect.go`** - Example client demonstrating usage

## Architecture

### Wrapper Design Pattern

The `ReconnectingConnection` wraps an underlying `Connection` and intercepts `Read()` and `Write()` calls to detect connection failures:

```
Application Code
       ↓
[ReconnectingConnection] ← Detects failures and handles reconnection
       ↓
   [Connection]  ← Underlying PCP connection
       ↓
  [PcpCore]     ← Recreated on reconnection
```

### Key Components

#### 1. ReconnectConfig Structure

```go
type ReconnectConfig struct {
    Enabled             bool          // Enable/disable auto-reconnection
    MaxRetries          int           // Max attempts (-1 for infinite)
    InitialBackoff      time.Duration // First backoff delay (e.g., 100ms)
    MaxBackoff          time.Duration // Backoff cap (e.g., 30s)
    BackoffMultiplier   float64       // Exponential growth (e.g., 2.0)
    OnReconnect         func()        // Success callback
    OnFinalFailure      func()        // Failure callback
}
```

#### 2. DialConfig Structure

Stores parameters needed to recreate a connection:

```go
type DialConfig struct {
    PcpCore    *PcpCore
    LocalIP    string
    ServerIP   string
    ServerPort uint16
    PcpConfig  *config.Config
}
```

## Usage Example

### Basic Usage

```go
// Create initial connection
baseConn, err := pcpCoreObj.DialPcp(sourceIP, serverIP, uint16(serverPort), pcpConfig)
if err != nil {
    return err
}

// Wrap with automatic reconnection
reconnectConfig := &lib.ReconnectConfig{
    Enabled:           true,
    MaxRetries:        5,                      // Retry up to 5 times
    InitialBackoff:    500 * time.Millisecond, // Start with 500ms
    MaxBackoff:        10 * time.Second,       // Cap at 10 seconds
    BackoffMultiplier: 1.5,                    // 1.5x exponential growth
}

dialCfg := &lib.DialConfig{
    PcpCore:    pcpCoreObj,
    LocalIP:    sourceIP,
    ServerIP:   serverIP,
    ServerPort: uint16(serverPort),
    PcpConfig:  pcpConfig,
}

conn := lib.NewReconnectingConnection(baseConn, reconnectConfig, dialCfg)

// Use like a normal connection - reconnection is transparent
n, err := conn.Write(data)
n, err := conn.Read(buffer)
conn.Close()
```

### With Callbacks

```go
reconnectConfig := lib.DefaultReconnectConfig()
reconnectConfig.OnReconnect = func() {
    log.Println("Successfully reconnected!")
}
reconnectConfig.OnFinalFailure = func() {
    log.Println("All reconnection attempts failed. Giving up.")
}
```

### Advanced: Get Statistics

```go
conn := lib.NewReconnectingConnection(baseConn, config, dialCfg)
// ... use connection ...

// Get reconnection statistics
attempts, lastErr, lastFailTime := conn.GetReconnectStats()
log.Printf("Reconnection attempts: %d, Last error: %v\n", attempts, lastErr)
```

## Reconnection Behavior

### Error Detection

The wrapper detects and triggers reconnection on:

- `io.EOF` - Connection closed
- `net.ErrClosed` - Network error
- `net.Error` with `Timeout()` - Timeout errors
- `net.Error` with `Temporary()` - Temporary network errors

### Exponential Backoff

When reconnection fails, the wrapper waits with exponential backoff:

1. **Initial delay:** `InitialBackoff` (e.g., 100ms)
2. **Growth:** Each failed attempt multiplies by `BackoffMultiplier` (e.g., 2.0)
3. **Cap:** Maximum delay capped at `MaxBackoff` (e.g., 30 seconds)
4. **Jitter:** ±10% random jitter added to prevent thundering herd

Example sequence with `InitialBackoff=100ms`, `Multiplier=2.0`, `MaxBackoff=30s`:
- Attempt 1: wait 100ms
- Attempt 2: wait 200ms
- Attempt 3: wait 400ms
- Attempt 4: wait 800ms
- Attempt 5: wait 1.6s
- Attempt 6: wait 3.2s
- Attempt 7: wait 6.4s
- Attempt 8: wait 12.8s
- Attempt 9: wait 25.6s
- Attempt 10: wait 30s (capped)

### State Management

During reconnection:

- The wrapper acquires an exclusive lock on the connection
- On successful reconnection, the underlying `currentConn` is replaced
- In-flight operations are retried automatically (up to once)
- If reconnection fails, the original error is returned

## Configuration Best Practices

### Conservative (Recommended for Production)

```go
&lib.ReconnectConfig{
    Enabled:           true,
    MaxRetries:        10,
    InitialBackoff:    1 * time.Second,
    MaxBackoff:        60 * time.Second,
    BackoffMultiplier: 1.5,
}
```

**Rationale:** Longer initial delay prevents network thrashing; 10 retries provides ~2 minutes of recovery window.

### Aggressive (For Testing)

```go
&lib.ReconnectConfig{
    Enabled:           true,
    MaxRetries:        5,
    InitialBackoff:    100 * time.Millisecond,
    MaxBackoff:        5 * time.Second,
    BackoffMultiplier: 2.0,
}
```

**Rationale:** Quick recovery for development/testing; fails fast if unavailable.

### Infinite Retry (For Critical Systems)

```go
&lib.ReconnectConfig{
    Enabled:           true,
    MaxRetries:        -1,  // Infinite
    InitialBackoff:    100 * time.Millisecond,
    MaxBackoff:        30 * time.Second,
    BackoffMultiplier: 2.0,
}
```

**Rationale:** Keeps attempting indefinitely; useful for applications that must maintain connection.

## Implementation Details

### Thread Safety

- Uses `sync.RWMutex` to protect concurrent access to connection state
- Safe for concurrent `Read()` and `Write()` operations
- `Close()` is idempotent - safe to call multiple times

### Memory Efficiency

- Minimal overhead: single wrapper struct per connection
- No goroutines spawned by the wrapper
- Original connection garbage collected after successful reconnection

### Interface Compatibility

The `ReconnectingConnection` implements the `net.Conn` interface:

```go
func (rc *ReconnectingConnection) Read(b []byte) (int, error)
func (rc *ReconnectingConnection) Write(b []byte) (int, error)
func (rc *ReconnectingConnection) Close() error
func (rc *ReconnectingConnection) LocalAddr() net.Addr
func (rc *ReconnectingConnection) RemoteAddr() net.Addr
func (rc *ReconnectingConnection) SetDeadline(t time.Time) error
func (rc *ReconnectingConnection) SetReadDeadline(t time.Time) error
func (rc *ReconnectingConnection) SetWriteDeadline(t time.Time) error // No-op
```

## Limitations and Future Improvements

### Current Limitations

1. **No data loss recovery** - Any in-flight data is lost on reconnection
   - Application layer must handle retransmission if needed

2. **Context-based cancellation** - Uses `context.Background()` internally
   - Could be extended to support explicit cancellation contexts

3. **SetWriteDeadline not supported** - No-op operation
   - Underlying Connection doesn't implement this method

### Recommended Future Enhancements

1. **Connection state preservation**
   - Track sequence numbers and outstanding requests
   - Allow resuming partial operations on reconnect

2. **Custom error classification**
   - Allow applications to define which errors are retryable
   - Support for application-specific error handling

3. **Metrics and monitoring**
   - Built-in counters for reconnection attempts
   - Hooks for observability/logging integration

4. **Graceful degradation**
   - Circuit breaker pattern for repeated failures
   - Exponential decay of reconnection urgency

## Testing

### Manual Testing

1. Start a server:
   ```bash
   cd /home/rodger/dev/Pseudo-TCP
   go run test/testserver/main.go
   ```

2. Run the client with reconnection:
   ```bash
   go run client/client_with_reconnect.go \
     -sourceIP 127.0.0.4 \
     -serverIP 127.0.0.2 \
     -serverPort 8901
   ```

3. Simulate connection failure (in another terminal):
   ```bash
   # Kill the server to trigger connection loss
   pkill -f testserver
   ```

4. Observe reconnection attempts in client output

### Unit Testing Considerations

To add unit tests:

1. Create a mock `*PcpCore` that can simulate connection failures
2. Test backoff calculation accuracy
3. Test thread safety with concurrent operations
4. Test callback invocation
5. Test error classification logic

## Migration from Standard Connections

Existing code using standard `Connection`:

```go
// Before
conn, err := pcpCoreObj.DialPcp(sourceIP, serverIP, uint16(port), config)

// After - Minimal change
conn, err := pcpCoreObj.DialPcp(sourceIP, serverIP, uint16(port), config)
conn = lib.NewReconnectingConnection(conn, lib.DefaultReconnectConfig(), &lib.DialConfig{...})
```

## Summary

The `ReconnectingConnection` wrapper provides:

- ✅ **Transparent reconnection** - Application code changes minimally
- ✅ **Configurable retry logic** - Exponential backoff with jitter
- ✅ **Thread-safe** - Suitable for concurrent access
- ✅ **Non-invasive** - No changes to core Connection logic
- ✅ **Lightweight** - Minimal memory and CPU overhead
- ✅ **Observable** - Statistics and callbacks for monitoring

It's the recommended approach for production systems that need resilient client-server communication with automatic recovery from temporary failures.
