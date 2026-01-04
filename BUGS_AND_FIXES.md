# Bugs and Fixes - Reference for Future Development

## Overview

This document records critical issues discovered during testing and the fixes applied. Use as a reference to prevent similar issues in future development.

---

## Issue 1: Ctrl+C Signal Handling Not Responsive

### Symptoms
- Client required external kill command (`kill -9`) to stop
- Ctrl+C pressed but client continued running
- No immediate response to interrupt signals

### Root Cause
```
Read() blocking indefinitely
    ↓
Signal arrives and handler sets flag
    ↓
Main loop only checks flag on ticker events (~500ms interval)
    ↓
Result: Up to 500ms delay before shutdown begins
```

The `Connection.Read()` call was blocking indefinitely on the `readChannel`, and the main loop only checked the signal channel on ticker intervals. With a 500ms ticker, Ctrl+C could be delayed by up to 500ms.

### Fix Applied

**Use ReadDeadline to make Read timeout:**

```go
// Before: No deadline, blocks indefinitely
n, err := conn.Read(buffer)

// After: Times out, returns control to main loop
conn.SetReadDeadline(time.Now().Add(interval + 100*time.Millisecond))
n, err := conn.Read(buffer)
if err != nil && err.Error() == "Read deadline exceeded" {
    continue  // Not an error, just no data yet
}
```

**Use done channel pattern for cleaner signal handling:**

```go
sigChan := make(chan os.Signal, 1)
signal.Notify(sigChan, syscall.SIGINT)

done := make(chan struct{})
go func() {
    <-sigChan
    close(done)  // Signal shutdown
}()

for {
    select {
    case <-done:
        goto shutdown
    case <-ticker.C:
        // Main work
    }
}
```

### Result
✅ Ctrl+C responds in < 600ms (interval + buffer)
✅ Clean shutdown without external kill
✅ Idiomatic Go concurrency pattern

### Lesson Learned
**Blocking I/O in event loops needs timeouts.** When using `Read()` or other blocking operations in a main loop that needs to respond to signals, set a deadline so the loop gets control back periodically.

---

## Issue 2: PcpProtocolConnection Not Recovering on Server Restart

### Symptoms
- Server dies → client detects keepalive timeout ✓
- Client attempts reconnection ✓
- Reconnection fails repeatedly with "connection refused" or "i/o error"
- Never recovers even when server restarts
- Eventually gives up after all retry attempts

### Root Cause

PCP protocol has **two-layer architecture**:

```
Layer 1: PcpProtocolConnection
         └─ Wraps net.IPConn (raw socket)
         └─ Shared across multiple Connections
         └─ Manages protocol-level operations

Layer 2: Connection
         └─ TCP-like protocol built on PcpProtocolConnection
         └─ Can be created and destroyed multiple times
```

**The problem:**

```go
func (pc *PcpCore) DialPcp(srcAddr, dstAddr string) (*Connection, error) {
    // BUGGY: Always reuses existing PcpProtocolConnection if one exists
    if pconn, exists := pc.connMap.Load(key); exists {
        // Just create new Connection from existing (possibly broken) protocol connection
        conn, err := pconn.Dial(...)
        if err != nil {
            return nil, err  // Retries will keep using the same broken pconn
        }
    }
}
```

**When server dies:**
1. net.IPConn becomes invalid/broken
2. But PcpProtocolConnection still references it
3. Reconnect creates new Connection from broken protocol connection
4. New Connection also fails (inherits broken socket)
5. All retries fail because they reuse the same broken protocol connection

### Fix Applied

**Check if protocol connection is broken:**

```go
func (pc *PcpCore) DialPcp(srcAddr, dstAddr string) (*Connection, error) {
    // Check if existing protocol connection is broken
    if pconn != nil && pconn.isClosed {
        pc.connMap.Delete(key)
        pconn = nil
    }
    
    // Create fresh if needed
    if pconn == nil {
        pconn = NewPcpProtocolConnection(...)
        pc.connMap.Store(key, pconn)
    }
    
    // On dial failure, also recreate
    if conn, err := pconn.Dial(...); err != nil {
        pc.connMap.Delete(key)
        pconn = nil
        // Return error, let caller retry with fresh protocol connection
        return nil, err
    }
    
    return conn, nil
}
```

**Why this works:**
- Next reconnection attempt calls DialPcp() again
- Finds no protocol connection (deleted above)
- Creates fresh PcpProtocolConnection with new net.IPConn
- Fresh socket succeeds in connecting to restarted server

### Result
✅ Automatic recovery when server restarts
✅ Client continues after reconnection succeeds
✅ No explicit retry loop needed (exponential backoff handles it)

### Lesson Learned
**Resource pooling needs health checks.** When caching expensive resources (like protocol connections), must detect when they become invalid and recreate them. Check the health flag or error state before reusing.

---

## Issue 3: Keepalive Timer Panic - "send on closed channel"

### Symptoms
- Client runs fine for a while
- Keepalive timeout triggered (expected)
- Client attempts reconnection (expected)
- Then: **PANIC: send on closed channel**

```
panic: send on closed channel

goroutine 316 [running]:
github.com/Clouded-Sabre/Pseudo-TCP/lib.(*Connection).sendKeepalivePacket()
    /home/rodger/dev/Pseudo-TCP/lib/connection.go:799 +0x113
github.com/Clouded-Sabre/Pseudo-TCP/lib.(*Connection).startKeepaliveTimer.func1()
    /home/rodger/dev/Pseudo-TCP/lib/connection.go:834 +0x17d
```

### Root Cause

**Classic race condition between timer callback and resource cleanup:**

```
Timeline:
T1: Keepalive timer scheduled to fire at T+5s
T2: Keepalive timeout detected (after 25s of failures)
T3: clearConnResource() called
T4: closeSignal channel closed
T5: Keepalive timer fires (scheduled earlier, still in queue)
T6: startKeepaliveTimer callback executes
T7: Calls sendKeepalivePacket()
T8: sendKeepalivePacket() tries to send on closeSignal → closed channel
T9: PANIC!
```

The problem: Timer fire was queued before the channel was closed. When it finally fires, the channel is gone.

### Fix Applied

**Three-layer defensive approach:**

**Layer 1 - Check in timer callback:**
```go
func (c *Connection) startKeepaliveTimer() {
    time.AfterFunc(interval, func() {
        c.isClosedMu.Lock()
        if c.isClosed {  // Check if connection already closed
            c.isClosedMu.Unlock()
            return  // Exit early, don't proceed
        }
        c.isClosedMu.Unlock()
        
        c.sendKeepalivePacket()
    })
}
```

**Layer 2 - Check before sending:**
```go
func (c *Connection) sendKeepalivePacket() {
    c.isClosedMu.Lock()
    if c.isClosed {  // Double-check immediately before operation
        c.isClosedMu.Unlock()
        return
    }
    c.isClosedMu.Unlock()
    
    // Now safe to use channels
    select {
    case c.params.sigOutputChan <- packet:
        // Success
    default:
        // Layer 3: Non-blocking send prevents panic
        log.Printf("Could not send keepalive packet\n")
    }
}
```

**Layer 3 - Non-blocking send with default case:**
```go
// Using select with default clause:
select {
case channel <- value:
    // Sent successfully
default:
    // Channel closed or full - graceful degradation
    // No panic!
}

// Better than blocking send:
channel <- value  // PANIC if channel closed
```

### Result
✅ No panic on keepalive timeout
✅ Graceful degradation instead of crash
✅ Connection can close safely while timers are pending

### Lesson Learned
**Race conditions in concurrent systems require multiple defensive checks.** A single check isn't enough because of timing windows. Use:
1. **Early detection** - Check before starting expensive operations
2. **Last-minute validation** - Check immediately before I/O
3. **Graceful alternatives** - Use non-blocking sends instead of blocking

---

## Issue 4: Termination Panic on Reconnect - "send on closed channel"

### Symptoms
- Client successfully reconnects (new connection established) ✓
- Old connection starts cleanup process
- Then: **PANIC: send on closed channel** in termination code

```
panic: send on closed channel

goroutine XYZ [running]:
github.com/Clouded-Sabre/Pseudo-TCP/lib.(*Connection).termCallerSendFin()
    /home/rodger/dev/Pseudo-TCP/lib/connection.go:1054
```

Log shows: `Reconnection successful` immediately followed by `Initiate termination of pcp connection`

### Root Cause

**Race between explicit Close() and ongoing termination operations:**

```go
// OLD BUGGY CODE in client_reconnector.go:
go func(conn *Connection) {
    time.Sleep(100 * time.Millisecond)
    conn.Close()  // Triggers 4-way termination
}(oldConn)
```

**What happens:**

1. New connection created and swapped in (reconnection successful)
2. Async goroutine waits 100ms
3. Goroutine calls `oldConn.Close()`
4. `Close()` → `initTermination()` → `termCallerSendFin()`
5. `termCallerSendFin()` tries to send FIN packet
6. But `c.params.sigOutputChan` was already closed by `clearConnResource()`
7. PANIC: send on closed channel

**Why it's hard to fix in termination code:**
- Termination involves 4-way FIN handshake
- Multiple goroutines participating in protocol exchange
- Each tries to send FIN/ACK on shared channel
- Once channel closed, any remaining send panics

### Fix Applied

**Don't explicitly close old connections - let them timeout naturally:**

```go
// OLD: Problematic explicit close
go func(conn *Connection) {
    time.Sleep(100 * time.Millisecond)
    conn.Close()  // Races with termination
}(oldConn)

// NEW: Natural cleanup
if oldConn != nil {
    log.Printf("Old connection replaced with new one, letting it clean up naturally\n")
    // Don't close it explicitly
    // Connection will timeout and cleanup via keepalive mechanism
}
```

**Timeline with natural cleanup:**

```
T0:   Old connection replaced (not closed)
T0.1: New connection in use

T5s:  Old connection's keepalive timer fires
T5.1: sendKeepalivePacket() checks isClosed (protected by previous fix)
T5.2: Protected non-blocking send handles gracefully

T25s: Old connection's keepalive timeout fires (idleTimeout=25s)
T25.1: maxKeepaliveAttempts exceeded
T25.2: clearConnResource() called
T25.3: All goroutines exit, isClosed=true
T25.4: No termination protocol needed (natural cleanup)
T25.5: Connection eligible for garbage collection ✅
```

### Result
✅ No termination panics on reconnection
✅ Old connections cleaned up automatically
✅ Simpler overall - fewer goroutines, no coordination needed
✅ More robust - no timing assumptions

### Lesson Learned
**Explicit resource cleanup can race with ongoing operations.** Better to:
1. Stop accepting new operations
2. Let ongoing operations complete or timeout
3. Let garbage collector handle final cleanup

This is more robust than trying to forcefully shut down (which requires perfect synchronization).

---

## Summary of Patterns

### What Caused the Issues

| Issue | Root Cause Type | Lesson |
|-------|-----------------|--------|
| #1: Ctrl+C delayed | Blocking I/O in event loop | Use timeouts on blocking calls |
| #2: Reconnection failed | Resource caching without health checks | Validate cached resources before reuse |
| #3: Keepalive timer panic | Race between timer callback and cleanup | Multi-layer defensive checks |
| #4: Termination panic | Explicit cleanup racing with ongoing ops | Let timeouts handle cleanup |

### Common Themes

1. **Concurrency is Hard** - Timing-dependent bugs are subtle
2. **Defensive Programming** - Multiple checks at different layers
3. **Graceful Degradation** - Log and continue, don't panic
4. **Timeouts > Explicit Close** - More robust in concurrent systems
5. **Health Checks** - Validate cached/reused resources

### Applied Solutions

1. **Use Read/Write deadlines** - Makes blocking calls responsive
2. **Check before reuse** - Validate cached resources
3. **Multi-layer checks** - Early detection + last-minute validation
4. **Non-blocking operations** - `select` with `default` clause
5. **Natural cleanup** - Timeouts > explicit close in concurrent code

---

## Future Prevention

### Code Review Checklist

When implementing similar features, watch for:

- [ ] Is there blocking I/O in the main event loop?
  - Solution: Add Read/Write deadlines
  
- [ ] Are resources cached or pooled?
  - Solution: Add health checks before reuse
  
- [ ] Can operations race with cleanup?
  - Solution: Use isClosed flags or similar markers
  
- [ ] Are channel sends to channels that might close?
  - Solution: Use non-blocking sends with `select` + `default`
  
- [ ] Is explicit Close() called during ongoing operations?
  - Solution: Let timeouts handle cleanup instead

### Testing Checklist

- [ ] Test Ctrl+C responsiveness (should be < 1 second)
- [ ] Test with server crash/restart
- [ ] Test repeated reconnections
- [ ] Test Ctrl+C during reconnection backoff
- [ ] Look for goroutine leaks (number should stabilize)
- [ ] Check for "send on closed channel" in logs

---

## Reference Implementation

See these files for correct implementation:

- **lib/connection.go** - Defensive checks in keepalive timer and sendKeepalivePacket()
- **lib/pcpcore.go** - Health check in DialPcp() before reusing protocol connection
- **lib/client_reconnector.go** - Natural cleanup pattern (no explicit Close)
- **test/echoclient/main.go** - Proper signal handling and read deadline usage

All four critical issues are fixed in current code. Use as reference to avoid similar problems.
