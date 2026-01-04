# Client Auto-Reconnect Feature - Documentation Index

## Quick Navigation

### For Implementers (People Building Clients)
👉 **[CLIENT_IMPLEMENTATION_GUIDE.md](CLIENT_IMPLEMENTATION_GUIDE.md)** (669 lines)

How to implement a client using the reconnection feature:
- Step-by-step implementation guide
- Code patterns and examples
- Configuration reference
- Testing procedures
- Best practices
- Debugging tips

**Start here** if you're building a client application.

### For Architects & Designers
👉 **[CLIENT_RECONNECT_DESIGN.md](CLIENT_RECONNECT_DESIGN.md)** (495 lines)

Why the feature is designed the way it is:
- Architecture overview
- Design philosophy (client-initiated vs wrapper-based)
- Component breakdown
- Three-layer race condition defense
- Exponential backoff strategy
- Natural cleanup approach
- Performance characteristics
- Thread safety model

**Read this** to understand the design decisions and reasoning.

### For Future Development
👉 **[BUGS_AND_FIXES.md](BUGS_AND_FIXES.md)** (443 lines)

Reference of issues discovered and how they were fixed:
- Issue #1: Ctrl+C signal handling
- Issue #2: Protocol connection recovery
- Issue #3: Keepalive timer race condition
- Issue #4: Termination panic on reconnect
- Root causes and solutions
- Lessons learned
- Prevention checklist

**Use this** to prevent similar issues and understand the history.

---

## Quick Start

1. **First time implementing a client?**
   - Start with [CLIENT_IMPLEMENTATION_GUIDE.md](CLIENT_IMPLEMENTATION_GUIDE.md)
   - Follow the step-by-step guide
   - Run the echo client example

2. **Want to understand the design?**
   - Read [CLIENT_RECONNECT_DESIGN.md](CLIENT_RECONNECT_DESIGN.md)
   - Understand why decisions were made
   - Learn about thread safety and race conditions

3. **Encountered an issue?**
   - Check [BUGS_AND_FIXES.md](BUGS_AND_FIXES.md)
   - See if it matches a known issue
   - Review the solution and lessons learned

---

## Feature Overview

**What it does:**
- Automatically detects when server connection is lost (via keepalive timeout)
- Attempts to reconnect with exponential backoff
- Returns new connection to application transparently
- Handles Ctrl+C gracefully during reconnection

**Status:** ✅ Production Ready
- All critical issues fixed and tested
- Race condition safe (3-layer defense)
- Responsive signal handling
- Automatic protocol connection recovery

---

## Files in This Branch

### Documentation
- **CLIENT_IMPLEMENTATION_GUIDE.md** - How to use the feature
- **CLIENT_RECONNECT_DESIGN.md** - Why the feature works this way
- **BUGS_AND_FIXES.md** - Issues fixed and lessons learned
- **README.md** - Project root documentation

### Code
- **lib/client_reconnector.go** - Reconnection helper (189 lines)
- **lib/connection.go** - Modified with defensive checks
- **lib/pcpcore.go** - Modified with protocol connection recovery
- **test/echoclient/main.go** - Example client implementation (190 lines)
- **test/echoserver/main.go** - Example server (echo test)

---

## Key Concepts

### Client-Initiated Reconnection
- Application explicitly checks for `KeepAliveTimeoutError`
- Application calls `helper.HandleError(err)` to reconnect
- New connection returned to application
- Full control at application level

### Exponential Backoff
- 1s → 1.5s → 2.25s → 3.375s → ... → 60s
- Configurable via config.yaml
- Prevents thundering herd
- Allows server recovery time

### Three-Layer Defense (Race Conditions)
1. Check `isClosed` flag in timer callback
2. Check `isClosed` flag before sending  
3. Use non-blocking send with `select` + `default`

### Natural Cleanup
- Old connections not explicitly closed
- Timeout triggers natural cleanup after ~25 seconds
- No race conditions with termination code
- Simpler than explicit synchronization

---

## Testing the Feature

### Basic Test
```bash
# Terminal 1
cd test/echoserver && ./echoserver

# Terminal 2
cd test/echoclient && ./echoclient -interval=500ms

# Terminal 2: Press Ctrl+C
# Expected: Immediate shutdown
```

### Main Test (Reconnection)
```bash
# Terminal 1
cd test/echoserver && ./echoserver

# Terminal 2
cd test/echoclient && ./echoclient -interval=500ms

# Terminal 1 (after 10s): Ctrl+C then ./echoserver
# Terminal 2: Watch automatic reconnection
# Expected: No panics, exponential backoff visible, recovery succeeds
```

See [CLIENT_IMPLEMENTATION_GUIDE.md](CLIENT_IMPLEMENTATION_GUIDE.md#testing-your-implementation) for detailed test procedures.

---

## Common Questions

**Q: How do I use this in my client?**
A: See [CLIENT_IMPLEMENTATION_GUIDE.md](CLIENT_IMPLEMENTATION_GUIDE.md) - Step 1-5 section shows the complete pattern.

**Q: Why not use a wrapper connection?**
A: See [CLIENT_RECONNECT_DESIGN.md](CLIENT_RECONNECT_DESIGN.md#client-initiated-vs-wrapper-based) - We chose explicit control over transparency.

**Q: What if I get "send on closed channel" panic?**
A: See [BUGS_AND_FIXES.md](BUGS_AND_FIXES.md) - All known issues have been fixed. Verify you're using latest code.

**Q: How long does Ctrl+C take to respond?**
A: See [CLIENT_IMPLEMENTATION_GUIDE.md](CLIENT_IMPLEMENTATION_GUIDE.md#common-issues) - Should be < 600ms with proper ReadDeadline.

**Q: What happens to old connections when reconnecting?**
A: See [CLIENT_RECONNECT_DESIGN.md](CLIENT_RECONNECT_DESIGN.md#natural-cleanup-solution) - They clean up naturally after ~25 seconds via timeout.

---

## Summary

| Document | Purpose | Audience | Length |
|----------|---------|----------|--------|
| CLIENT_IMPLEMENTATION_GUIDE.md | How to use | Implementers | 669 lines |
| CLIENT_RECONNECT_DESIGN.md | Why it works | Architects | 495 lines |
| BUGS_AND_FIXES.md | What was fixed | Maintenance | 443 lines |

**Pick the one you need based on your role.**
