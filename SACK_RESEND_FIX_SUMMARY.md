# Summary: Packet Resend Fixes

This document outlines the fixes applied to resolve an issue where packets were being continuously resent despite successful delivery and acknowledgment (ACK).

## 1. Problem Summary

Packets were being resent every 200ms, even for packets that had already been acknowledged by the peer. This was especially prevalent in scenarios involving packet loss or out-of-order delivery but also occurred under normal conditions. The root cause was traced to two separate but related issues in the SACK (Selective Acknowledgment) implementation.

---

## 2. Root Cause Analysis and Fixes

### Issue 1: Incorrect SACK Negotiation on Client-Side

**Problem:** The client-side connection logic was enabling SACK support *after* the 3-way handshake was complete. The client would receive the server's SACK permit in the SYN-ACK, but it would set its internal `SackEnabled` flag too late. As a result, the client's connection object never properly enabled SACK functionality for its own use.

**File Modified:** `lib/pconn.go`

**Fix Applied:**
The SACK and timestamp option negotiation logic within the `dial` function was moved to occur *before* the final ACK of the 3-way handshake is sent (`initSendAck`). This ensures that upon receiving the SYN-ACK, the client correctly enables SACK for the connection before it is fully established.

```go
// Old Logic in lib/pconn.go
newConn.initSendAck()
// ... SACK options were set here, which was too late
newConn.tcpOptions.SackEnabled = ...

// Corrected Logic
// ... SACK options are now set here
newConn.tcpOptions.SackEnabled = ...
newConn.initSendAck()
```

### Issue 2: Incorrect ACK Processing Logic

**Problem:** The code was only cleaning up the resend buffer for ACK packets that explicitly contained SACK blocks. Standard ACKs, which do not contain SACK blocks but still cumulatively acknowledge data, were being ignored. This caused acknowledged packets to remain in the resend queue, leading to unnecessary retransmissions.

The condition incorrectly checked `packet.TcpOptions.SackEnabled` (the incoming packet's options) instead of `c.tcpOptions.SackEnabled` (the connection's overall SACK status).

**File Modified:** `lib/connection.go`

**Fix Applied:**
The condition in the `handleIncomingPackets` function was changed to check if SACK is enabled on the **connection** itself, rather than on the incoming packet. This ensures that `updateResendPacketsOnAck()` is called for *every* ACK on a SACK-enabled connection, allowing the resend buffer to be correctly pruned using the cumulative ACK number.

```go
// Old condition in lib/connection.go
if packet.TcpOptions.SackEnabled && isACK && ... {
    c.updateResendPacketsOnAck(packet)
}

// Corrected condition
if c.tcpOptions.SackEnabled && isACK && ... {
    c.updateResendPacketsOnAck(packet)
}
```

---

## 3. Outcome

These two fixes ensure that SACK is correctly negotiated and that the resend buffer is properly maintained, resolving the erroneous packet resend issue and improving the reliability of the protocol.
