# Bug Fix: Packet Resend Issue with SACK

## Problem Summary
Packets were being continuously resent every 200ms despite successful delivery and ACK reception, especially in scenarios with packet loss or out-of-order delivery. All resends occurred even for packets that had already been acknowledged.

## Root Cause Analysis

### Issue 1: SACK Permit Negotiation Timing (Commit 185b0ee)
**Location:** `lib/pconn.go` lines 237-247

The server was negotiating SACK support **after** sending the initial ACK response to the client's SYN packet.

```go
// WRONG: Old code
newConn.initSendAck()  // Send ACK without SACK options
// ... later ...
newConn.tcpOptions.permitSack = newConn.tcpOptions.permitSack && packet.TcpOptions.permitSack
```

**Impact:** Client never received SACK permit option in the SYN-ACK, so `c.tcpOptions.SackEnabled` remained false on the client side.

**Fix:** Moved SACK/timestamp negotiation before sending ACK:
```go
// CORRECT: New code
newConn.tcpOptions.permitSack = newConn.tcpOptions.permitSack && packet.TcpOptions.permitSack
newConn.tcpOptions.SackEnabled = newConn.tcpOptions.permitSack && newConn.tcpOptions.SackEnabled
newConn.initSendAck()  // Send ACK WITH SACK options
```

### Issue 2: ACK Processing Logic (Commit 3a1ad07)
**Location:** `lib/connection.go` lines 274-277

The code only called `updateResendPacketsOnAck()` when SACK blocks were present in the ACK packet:

```go
// WRONG: Old code
if packet.TcpOptions.SackEnabled && isACK && !isSYN && !isFIN && !isRST {
    c.updateResendPacketsOnAck(packet)  // Only called for packets with SACK blocks
} else if isACK... {
    // Skip processing
}
```

**Problem:** In standard TCP:
- SACK Permit (kind 4) is negotiated in SYN/SYN-ACK only
- SACK Option (kind 5) appears in ACKs **only when there are missing/OOO packets**
- Normal sequential ACKs have no SACK option, but still acknowledge data!

This caused packets to stay in the resend tracking map even after receiving normal ACKs.

**Fix:** Process **every ACK** for cumulative acknowledgment, using SACK blocks only if present:
```go
// CORRECT: New code
if isACK && !isSYN && !isFIN && !isRST {
    c.updateResendPacketsOnAck(packet)  // Always process ACKs
}
```

The `updateResendPacketsOnAck()` function already handles both cases:
- Line 1037: Removes packets covered by cumulative ACK number (always works)
- Lines 1030-1033: Additionally removes packets in SACK blocks (if present)

## Test Results

### Scenario 1: No Packet Loss
- 10 sequential messages sent and received
- All packets delivered once with no resends
- ✅ **PASS**

### Scenario 2: Packet Loss (Multiple Drops)
- 29 total messages, with 5 random drops
- Each dropped packet: single timeout-based resend after ~500ms
- After resend: packet delivered and no further resends
- All echo responses matched correctly
- ✅ **PASS**

### Example: Message 8 (Packet 7 Dropped)
```
12:48:18 [8] Sending: Echo message 8
12:48:18 Packet 7 is lost
12:48:19 One Packet resent with SEQ 99 and payload length 14!
12:48:19 [8] Received: Echo message 8
12:48:19 [8] ✓ Echo match!
```

## Commits

1. **185b0ee**: "fix: Move SACK permit negotiation before sending ACK"
   - Ensures SYN-ACK includes SACK permit option
   - Sets `permitSack=true` on both client and server before sending ACK

2. **3a1ad07**: "fix: Always call updateResendPacketsOnAck on any ACK, not just SACK ACKs"
   - Processes all incoming ACKs for packet removal
   - Removes packets via cumulative ACK number (always)
   - Additionally uses SACK blocks when available (for OOO scenarios)

## Files Modified
- `lib/pconn.go`: Fixed SACK negotiation timing
- `lib/connection.go`: Fixed ACK processing logic

## Verification
- All packages compile successfully
- No new syntax errors or warnings
- Comprehensive testing with both loss and no-loss scenarios
- All echo messages match and deliver correctly

## Related Configuration
- Resend timeout: 500ms (configurable)
- Maximum resends: 5 times per packet
- SACK enabled by default if negotiated with peer
