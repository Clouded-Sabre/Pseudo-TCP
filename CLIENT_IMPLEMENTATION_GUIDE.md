# Client Implementation Guide - Using the Reconnection Feature

## Overview

This guide shows how to implement a client that uses the auto-reconnect feature. Follow these patterns for consistent, reliable client implementations.

---

## Quick Start: Running the Example

### Build Everything

```bash
cd /home/rodger/dev/Pseudo-TCP
go build ./...
go build -o test/echoclient/echoclient ./test/echoclient
go build -o test/echoserver/echoserver ./test/echoserver
```

### Test Automatic Reconnection

**Terminal 1: Start Server**
```bash
cd test/echoserver && ./echoserver
```

**Terminal 2: Start Client**
```bash
cd test/echoclient && ./echoclient -interval=500ms
```

**Terminal 1 (after 10s): Restart Server**
```bash
# Press Ctrl+C to stop
# Wait 2-3 seconds
./echoserver
```

**Expected Output in Terminal 2:**
```
[1] Sent: 512 bytes, Received: 512 bytes
[2] Sent: 512 bytes, Received: 512 bytes
...
[20] ERROR: Read error - Keepalive timeout detected, initiating reconnection
[Attempt 1/10] Reconnecting in 1s...
[Attempt 2/10] Reconnecting in 1.5s...
[21] Sent: 512 bytes, Received: 512 bytes  (automatic recovery!)
```

---

## Step-by-Step Client Implementation

### Step 1: Setup Configuration and Initialization

```go
package main

import (
    "log"
    "github.com/Clouded-Sabre/Pseudo-TCP/lib"
    "github.com/Clouded-Sabre/Pseudo-TCP/config"
)

func main() {
    // Load configuration
    cfg := config.ParseConfig("config.yaml")
    
    // Initialize PCP core
    pcpCore := lib.NewPcpCore(cfg)
    
    // Create reconnection helper
    helper := lib.NewClientReconnectHelper(pcpCore, cfg)
    
    // Set callbacks (optional)
    helper.OnReconnect(func() {
        log.Println("✅ Successfully reconnected to server")
    })
    helper.OnFinalFailure(func() {
        log.Println("❌ Failed to reconnect after all attempts")
    })
```

### Step 2: Establish Initial Connection

```go
    // Create initial connection
    conn, err := pcpCore.DialPcp(sourceIP, serverIP)
    if err != nil {
        log.Fatalf("Failed to connect: %v", err)
    }
    
    log.Printf("Connected to %s\n", serverIP)
```

### Step 3: Set Up Signal Handling

```go
    // Signal handling for clean Ctrl+C shutdown
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
    
    done := make(chan struct{})
    go func() {
        <-sigChan
        close(done)  // Signal shutdown
    }()
```

### Step 4: Main Event Loop with Reconnection

```go
    // Ticker for periodic operations (e.g., sending packets)
    packetInterval := 500 * time.Millisecond
    ticker := time.NewTicker(packetInterval)
    defer ticker.Stop()
    
    packetCount := 0
    
    for {
        select {
        case <-done:
            log.Println("Shutting down...")
            goto shutdown
            
        case <-ticker.C:
            packetCount++
            
            // Try to send data
            payload := []byte("hello world")
            n, err := conn.Write(payload)
            if err != nil {
                log.Printf("[%d] ERROR: Write error - %v\n", packetCount, err)
                
                // Check if it's a keepalive timeout (needs reconnection)
                if isKeepAliveTimeoutError(err) {
                    log.Printf("[%d] ERROR: Keepalive timeout detected, initiating reconnection\n", 
                        packetCount)
                    
                    // Attempt reconnection
                    newConn, err := helper.HandleError(err)
                    if err != nil {
                        log.Printf("[%d] ERROR: Reconnection failed - %v\n", packetCount, err)
                        continue  // Keep trying, HandleError manages backoff
                    }
                    
                    // Success! Swap to new connection
                    conn = newConn
                    log.Printf("[%d] ✅ Reconnection successful\n", packetCount)
                }
                continue
            }
            
            // Try to read response (with timeout for responsiveness)
            buffer := make([]byte, 1024)
            conn.SetReadDeadline(time.Now().Add(packetInterval + 100*time.Millisecond))
            n, err = conn.Read(buffer)
            
            if err != nil {
                // Check for read deadline timeout (normal, not an error)
                if err.Error() == "Read deadline exceeded" {
                    continue  // Just no response yet
                }
                
                log.Printf("[%d] ERROR: Read error - %v\n", packetCount, err)
                
                if isKeepAliveTimeoutError(err) {
                    log.Printf("[%d] ERROR: Keepalive timeout detected, initiating reconnection\n", 
                        packetCount)
                    
                    newConn, err := helper.HandleError(err)
                    if err != nil {
                        log.Printf("[%d] ERROR: Reconnection failed - %v\n", packetCount, err)
                        continue
                    }
                    
                    conn = newConn
                    log.Printf("[%d] ✅ Reconnection successful\n", packetCount)
                }
                continue
            }
            
            // Success
            log.Printf("[%d] Sent: %d bytes, Received: %d bytes\n", 
                packetCount, n, len(buffer[:n]))
        }
    }
    
shutdown:
    conn.Close()
    log.Println("Client closed")
}
```

### Step 5: Helper Function for Error Detection

```go
// isKeepAliveTimeoutError checks if an error is a keepalive timeout
func isKeepAliveTimeoutError(err error) bool {
    if err == nil {
        return false
    }
    
    // Check if it's the specific KeepAliveTimeoutError type
    _, ok := err.(*lib.KeepAliveTimeoutError)
    return ok
}
```

---

## Complete Working Example: Echo Client

See [test/echoclient/main.go](test/echoclient/main.go) for the full implementation:

```bash
# Features demonstrated:
# - Signal handling with done channel
# - Keepalive timeout detection
# - Automatic reconnection with exponential backoff
# - Read deadline for responsive Ctrl+C
# - Statistics reporting
# - Clean shutdown
```

Key code segments:

```go
// Signal handling
done := make(chan struct{})
go func() {
    <-sigChan
    close(done)
}()

// Read deadline for responsiveness
currentConn.SetReadDeadline(time.Now().Add(*packetInterval + 100*time.Millisecond))
n, err := currentConn.Read(buffer)

// Handle read timeout gracefully
if err != nil && err.Error() == "Read deadline exceeded" {
    continue
}

// Detect and handle keepalive timeout
if isKeepAliveTimeoutError(err) {
    newConn, err := helper.HandleError(err)
    if err == nil {
        currentConn = newConn
    }
}
```

---

## Common Implementation Patterns

### Pattern 1: Simple Loop with Reconnection

```go
for {
    select {
    case <-done:
        goto shutdown
    case <-ticker.C:
        data := []byte("message")
        _, err := conn.Write(data)
        
        if isKeepAliveTimeoutError(err) {
            newConn, err := helper.HandleError(err)
            if err == nil {
                conn = newConn
            }
            continue
        }
        
        if err != nil {
            log.Printf("Write error: %v\n", err)
            continue
        }
    }
}
```

### Pattern 2: Request-Response with Timeout

```go
// Send request
_, err := conn.Write(request)
if isKeepAliveTimeoutError(err) {
    conn, err = helper.HandleError(err)
    if err != nil {
        return err
    }
}

// Read response with timeout
conn.SetReadDeadline(time.Now().Add(5 * time.Second))
response := make([]byte, 1024)
n, err := conn.Read(response)

if err != nil {
    if err.Error() == "Read deadline exceeded" {
        return errors.New("server response timeout")
    }
    if isKeepAliveTimeoutError(err) {
        conn, err = helper.HandleError(err)
        if err != nil {
            return err
        }
    }
}

return processResponse(response[:n])
```

### Pattern 3: With Retry Logic

```go
const maxLocalRetries = 3

var conn *lib.Connection
var err error

for attempt := 0; attempt < maxLocalRetries; attempt++ {
    _, err = conn.Write(data)
    
    if err == nil {
        break  // Success
    }
    
    if isKeepAliveTimeoutError(err) {
        // Reconnection needed
        newConn, rerr := helper.HandleError(err)
        if rerr != nil {
            return rerr  // Give up
        }
        conn = newConn
        continue  // Retry send with new connection
    }
    
    // Other error - retry without reconnection
    time.Sleep(time.Millisecond * time.Duration(100 * (attempt + 1)))
}

return err
```

---

## Configuration Reference

### config.yaml Example

```yaml
# Server address
server:
  addr: "127.0.0.1"
  port: 8888

# Reconnection settings
reconnect:
  maxRetries: 10              # Max reconnection attempts
                              # -1 = infinite retries
  initialBackoff: 1s          # First retry delay
  maxBackoff: 60s             # Maximum delay between retries
  backoffMultiplier: 1.5      # Exponential backoff factor
  jitter: true                # Add randomness

# Connection timeouts
idleTimeout: 25               # Seconds before idle connection closes
keepaliveInterval: 5          # Seconds between keepalive packets
readDeadline: 2s              # Read timeout per operation
```

### Configuration Presets

**Aggressive (Fast Testing):**
```yaml
reconnect:
  maxRetries: 20
  initialBackoff: 100ms
  maxBackoff: 10s
  backoffMultiplier: 1.5
idleTimeout: 10
keepaliveInterval: 3
```

**Conservative (Production):**
```yaml
reconnect:
  maxRetries: 30
  initialBackoff: 5s
  maxBackoff: 120s
  backoffMultiplier: 1.3
idleTimeout: 60
keepaliveInterval: 10
```

**Infinite Retries (Critical Systems):**
```yaml
reconnect:
  maxRetries: -1              # Never give up
  initialBackoff: 2s
  maxBackoff: 30s
  backoffMultiplier: 1.5
```

---

## Client Flags/Arguments

### Running the Echo Client

```bash
# Default (500ms interval)
./echoclient

# Custom interval
./echoclient -interval=100ms      # Fast sending
./echoclient -interval=1s         # Slow sending
./echoclient -interval=5s         # Very slow

# With config file
./echoclient -config=config.yaml -interval=500ms
```

---

## Testing Your Implementation

### Test 1: Normal Operation with Ctrl+C

**Goal:** Verify Ctrl+C responds immediately

```bash
# Start server
./echoserver &

# Start client
./echoclient -interval=500ms

# Wait a few seconds, then press Ctrl+C
# Expected: Immediate shutdown (< 1 second)
```

### Test 2: Server Restart (Main Test)

**Goal:** Verify automatic reconnection works

```bash
# Terminal 1: Start server
./echoserver

# Terminal 2: Start client
./echoclient -interval=500ms

# Let it run for ~10 seconds, then in Terminal 1:
# - Press Ctrl+C to stop server
# - Wait 2-3 seconds
# - Run ./echoserver again

# Expected in Terminal 2:
# - "Keepalive timeout detected"
# - "[Attempt 1/10] Reconnecting in 1s..."
# - See exponential backoff: 1s, 1.5s, 2.25s...
# - When server restarts: immediate success
# - Packet count continues from where it left off
# - NO PANICS at any point
```

### Test 3: Ctrl+C During Reconnection

**Goal:** Verify Ctrl+C works even while reconnecting

```bash
# Terminal 1: Start server
./echoserver

# Terminal 2: Start client
./echoclient -interval=500ms

# After ~5 seconds, Terminal 1: Stop server (Ctrl+C)
# Terminal 2: Press Ctrl+C immediately (during retry backoff)
# Expected: Immediate exit, no waiting for retry to complete
```

### Test 4: Multiple Reconnections

**Goal:** Verify handling of repeated failures and recovery

```bash
# Terminal 1: Start server
./echoserver

# Terminal 2: Start client
./echoclient -interval=500ms

# Repeat 3 times (with 10s+ between each):
# - Terminal 1: Stop and restart server
# - Terminal 2: Should automatically reconnect each time
# Expected: No memory leaks, no accumulated goroutines
```

---

## Debugging Tips

### Enable Detailed Logging

Add logging to track what's happening:

```go
// At start of loop iteration
if packetCount%20 == 0 {
    log.Printf("[%d] Still connected, sending...\n", packetCount)
}

// When error occurs
log.Printf("[%d] Error type: %T, message: %v\n", 
    packetCount, err, err)

// When reconnecting
log.Printf("[%d] Starting reconnection attempt\n", packetCount)
```

### Check Server Status

```bash
# Is server running?
ps aux | grep echoserver

# Can you reach it?
netstat -an | grep 8888

# Check server logs
tail -f /tmp/echoserver.log
```

### Monitor Connection State

```go
// Add periodic status reports
if packetCount%100 == 0 {
    log.Printf("Stats at packet %d: still connected\n", packetCount)
}
```

### Common Issues

**"Keeps reconnecting but never succeeds"**
- Verify server is actually running
- Check firewall isn't blocking connection
- Verify correct server address/port in config

**"Ctrl+C takes too long"**
- ReadDeadline might be too long
- Should be: `interval + 100ms`
- Reduce interval: `-interval=100ms`

**"Connection immediately drops"**
- Check server is configured correctly
- Verify PCP protocol compatibility
- Check for firewall/NAT issues

---

## Best Practices

### 1. Always Set Read Deadline

```go
// Good: Makes Ctrl+C responsive
conn.SetReadDeadline(time.Now().Add(interval + 100*time.Millisecond))
n, err := conn.Read(buffer)

// Bad: Can be blocked for minutes
n, err := conn.Read(buffer)
```

### 2. Check Error Type Specifically

```go
// Good: Handles different errors appropriately
if isKeepAliveTimeoutError(err) {
    // Reconnect
} else if err == io.EOF {
    // Graceful close, don't reconnect
} else {
    // Other error
}

// Bad: Treats all errors the same
if err != nil {
    // Reconnect  (wrong for some errors!)
}
```

### 3. Use Signal Channels, Not Flags

```go
// Good: Works with select statements
done := make(chan struct{})
go func() {
    <-sigChan
    close(done)
}()

select {
case <-done:
    // Shutdown
}

// Bad: Requires polling
var shutdown bool
// ... somewhere else ...
shutdown = true

if shutdown {
    // Need to check periodically
}
```

### 4. Call SetReadDeadline Before Each Read

```go
// Good: Responsive to signals
for {
    conn.SetReadDeadline(time.Now().Add(timeout))
    n, err := conn.Read(buffer)
}

// Bad: Deadline might expire
conn.SetReadDeadline(time.Now().Add(timeout))
for {
    n, err := conn.Read(buffer)  // Deadline ages!
}
```

### 5. Handle Deadline Exceeded Gracefully

```go
// Good: Recognize timeout as non-error
if err != nil && err.Error() == "Read deadline exceeded" {
    continue  // Try again
}

// Bad: Treat timeout as connection error
if err != nil {
    // Try to reconnect (wrong!)
}
```

---

## Summary

To implement a client with auto-reconnect:

1. ✅ Create `ClientReconnectHelper` with config
2. ✅ Establish initial connection
3. ✅ Set up signal handling (done channel)
4. ✅ Use `SetReadDeadline` on all Read operations
5. ✅ Check for `KeepAliveTimeoutError` specifically
6. ✅ Call `helper.HandleError()` to reconnect
7. ✅ Continue using new connection transparently
8. ✅ Handle read deadline timeouts gracefully

Follow the patterns in [test/echoclient/main.go](test/echoclient/main.go) for reference. Your client will automatically recover from server restarts and respond immediately to Ctrl+C.
