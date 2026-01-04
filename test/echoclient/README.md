# Echo Client - New Implementation

## Overview

The new `test/echoclient/main.go` demonstrates the client-initiated automatic reconnection approach. It sends echo messages to an echo server and automatically reconnects if a keepalive timeout occurs.

## Features

✅ Uses new `ClientReconnectHelper` for automatic reconnection
✅ Detects `KeepAliveTimeoutError` and retries
✅ Handles other error types appropriately
✅ Provides statistics on successful/failed echoes
✅ Configurable number of iterations
✅ Proper error handling and recovery

## Usage

### Basic Usage
```bash
cd /home/rodger/dev/Pseudo-TCP
go build -o test/echoclient/echoclient ./test/echoclient

# Run with defaults
./test/echoclient/echoclient

# Run with custom parameters
./test/echoclient/echoclient -sourceIP 127.0.0.4 -serverIP 127.0.0.2 -serverPort 8901 -iterations 100
```

### Parameters
- `-sourceIP` (default: 127.0.0.4) - Source IP address for the client
- `-serverIP` (default: 127.0.0.2) - Server IP address to connect to
- `-serverPort` (default: 8901) - Server port number
- `-iterations` (default: 100) - Number of echo messages to send

## Testing Scenarios

### Test 1: Normal Echo Exchange
```bash
# Terminal 1: Start echo server
go run test/echoserver/main.go

# Terminal 2: Run echo client
go run test/echoclient/main.go -iterations 50

# Expected: All 50 echoes succeed, success rate 100%
```

### Test 2: Keepalive Timeout Recovery
```bash
# Terminal 1: Start echo server
go run test/echoserver/main.go

# Terminal 2: Run echo client (will take ~60+ seconds with default config)
go run test/echoclient/main.go -iterations 20

# Terminal 3: After a few iterations (wait ~30 seconds), kill the server
# Watch as client detects keepalive timeout
pkill -f echoserver

# Terminal 1 again: Restart the echo server before reconnect window closes
go run test/echoserver/main.go

# Expected: Client reconnects and resumes operation after backoff
```

### Test 3: Immediate Server Restart
```bash
# Terminal 1: Start echo server
go run test/echoserver/main.go

# Terminal 2: Run echo client
go run test/echoclient/main.go -iterations 100

# Terminal 3: Kill and restart server quickly (within 30 seconds)
pkill -f echoserver
# Wait a few seconds, then restart:
go run test/echoserver/main.go

# Expected: Client reconnects and continues
```

## Output Example

```
Echo client connected to server!
[0] Sending: Echo message 0
[0] Sent 14 bytes
[0] Received: Echo message 0
[0] ✓ Echo match!
[1] Sending: Echo message 1
[1] Sent 14 bytes
[1] Received: Echo message 1
[1] ✓ Echo match!
...
[Keepalive timeout detected during idle period]
[50] Keepalive timeout detected, attempting reconnection
[RECONNECT] Successfully reconnected to echo server
[50] Reconnected, retrying read
[50] Received: Echo message 50
[50] ✓ Echo match!
...

=== Echo Client Statistics ===
Total iterations: 100
Successful echoes: 100
Failed echoes: 0
Success rate: 100.0%
Echo client exit
```

## Implementation Details

### Error Handling Flow

```
Read/Write Operation
    ↓
Error occurs?
    ├─ Yes: KeepAliveTimeoutError?
    │   ├─ Yes: Call helper.HandleError()
    │   │   ├─ Reconnection successful? Continue
    │   │   └─ Reconnection failed? Exit
    │   └─ No: io.EOF or other?
    │       ├─ io.EOF: Graceful close, exit
    │       └─ Other: Log and continue/exit as appropriate
    └─ No: Process response normally
```

### Reconnection Strategy

- **Initial Backoff**: 1 second
- **Max Backoff**: 60 seconds
- **Multiplier**: 1.5x exponential growth
- **Max Retries**: 10 attempts
- **Total Recovery Window**: ~105 seconds

### Key Code Patterns

#### 1. Setup
```go
reconnectCfg := lib.DefaultClientReconnectConfig()
reconnectHelper := lib.NewClientReconnectHelper(
    pcpCore, sourceIP, serverIP, port, config,
    reconnectCfg)
conn, _ := pcpCore.DialPcp(sourceIP, serverIP, port, config)
reconnectHelper.SetConnection(conn)
```

#### 2. Error Detection on Read
```go
currentConn := reconnectHelper.GetConnection()
n, err := currentConn.Read(buffer)

if err != nil {
    if _, isKeepaliveTimeout := err.(*lib.KeepAliveTimeoutError); isKeepaliveTimeout {
        if reconnectHelper.HandleError(err) {
            continue  // Retry
        } else {
            break     // Give up
        }
    } else if err == io.EOF {
        break  // Graceful close
    }
}
```

#### 3. Error Detection on Write
```go
currentConn := reconnectHelper.GetConnection()
n, err := currentConn.Write(payload)

if err != nil {
    if reconnectHelper.HandleError(err) {
        // Reconnected, retry write
        currentConn = reconnectHelper.GetConnection()
        n, err = currentConn.Write(payload)
    }
}
```

## Monitoring

The client provides callbacks for monitoring:

```go
reconnectCfg.OnReconnect = func() {
    log.Println("[RECONNECT] Successfully reconnected to echo server")
}

reconnectCfg.OnFinalFailure = func(err error) {
    log.Printf("[RECONNECT] Failed to reconnect after all retries: %v\n", err)
}
```

## Comparing to Old Implementation

| Aspect | Old | New |
|--------|-----|-----|
| Error Handling | Ignored errors | Detects and handles errors |
| Reconnection | None | Automatic with exponential backoff |
| Configuration | N/A | Configurable via ClientReconnectConfig |
| Monitoring | None | Callbacks provided |
| Thread Safety | N/A | Mutex-protected connection access |
| Error Types | Generic | Specific (KeepAliveTimeoutError) |

## Building & Testing the Client

### Build
```bash
go build -o test/echoclient/echoclient ./test/echoclient
```

### Run Tests
```bash
# Test 1: Normal operation
./test/echoclient/echoclient -iterations 20

# Test 2: Many iterations (longer)
./test/echoclient/echoclient -iterations 1000

# Test 3: Custom server
./test/echoclient/echoclient -serverIP 192.168.1.100 -iterations 50
```

## Statistics Output

At the end, the client prints:
- Total iterations attempted
- Successful echo matches
- Failed operations
- Success rate percentage

This helps identify whether reconnection is working properly.

## Common Issues & Solutions

### Q: Client hangs on keepalive timeout
**A**: This is normal behavior. The client is waiting for backoff before retrying. With default config, it waits up to 60 seconds total across all retries.

### Q: "Reconnection failed" appears after a few attempts
**A**: The server might not be restarted within the recovery window. The default recovery window is ~105 seconds. To test:
1. Kill server
2. Wait < 105 seconds
3. Restart server

### Q: All echoes fail
**A**: Check that:
- Server is running
- Network is accessible
- Server and client use same config (port, MSS, etc.)
- No firewall blocking PCP protocol

### Q: Want faster reconnection for testing
**A**: Use aggressive config:
```go
reconnectCfg := lib.AggressiveClientReconnectConfig()
// Now: 5 retries, ~1.5 seconds total window
```

## Next Steps

1. **Test with echo server**: Run both server and client
2. **Simulate failure**: Kill server, watch reconnection
3. **Monitor recovery**: Check callback logs
4. **Adjust config**: Tune for your use case
5. **Deploy**: Use pattern in production code

---

**File**: test/echoclient/main.go
**Status**: ✅ Complete and tested
**Compilation**: ✅ Successful
