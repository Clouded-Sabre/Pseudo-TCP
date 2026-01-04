# Migration from iptables to nftables

## Overview

This document outlines the migration from `iptables` to `nftables` for managing firewall rules. With Ubuntu switching to nftables as the default firewall management tool, this update ensures compatibility with modern Linux distributions.

**Branch:** `feature/iptables-to-nftables`  
**Status:** In Progress

---

## Why nftables?

### Problems with iptables

1. **Deprecated:** iptables is deprecated in favor of nftables
2. **Complex codebase:** Large legacy codebase, hard to maintain
3. **Default removed:** Recent Ubuntu versions use nftables by default
4. **Fragmented:** Separate tools for IPv4 and IPv6 (iptables vs ip6tables)

### Benefits of nftables

1. **Modern:** Designed as a replacement for iptables
2. **Unified:** Single tool for IPv4 and IPv6
3. **Better syntax:** More intuitive, composable rules
4. **Performance:** More efficient rule evaluation
5. **Default:** Standard in current Ubuntu, Fedora, Debian
6. **Backward compatible:** Can read iptables rules via iptables-nft

---

## Current iptables Usage

The codebase uses iptables to drop RST (Reset) packets from the server to prevent the OS TCP/IP stack from interfering with the PCP protocol.

### Files to Modify

```
lib/pconn.go         - PcpProtocolConnection RST packet filtering
lib/pcpcore.go       - PcpCore RST packet filtering
lib/service.go       - Service-level RST packet filtering
```

### Current Functions

1. **lib/pconn.go**
   - `addIptablesRule(ip string, port int)` - Add client-side rule
   - `removeIptablesRule(ip string, port int)` - Remove client-side rule

2. **lib/pcpcore.go**
   - `addServerIptablesRule(ip string, port int)` - Add server-side rule
   - `removeServerIptablesRule(ip string, port int)` - Remove server-side rule

3. **lib/service.go**
   - Uses `removeIptablesRule()` for cleanup

---

## nftables Equivalent Commands

### Current iptables Rules

**Add RST drop rule (client side):**
```bash
# Check if rule exists first (to avoid duplicates)
iptables -C OUTPUT -p tcp --tcp-flags RST RST -d <ip> --dport <port> -j DROP 2>/dev/null
if [ $? -ne 0 ]; then
    iptables -A OUTPUT -p tcp --tcp-flags RST RST -d <ip> --dport <port> -j DROP
fi
```

**Remove RST drop rule (client side):**
```bash
iptables -D OUTPUT -p tcp --tcp-flags RST RST -d <ip> --dport <port> -j DROP
```

**Add RST drop rule (server side):**
```bash
# Check if rule exists first (to avoid duplicates)
iptables -C OUTPUT -p tcp --tcp-flags RST RST -s <ip> --sport <port> -j DROP 2>/dev/null
if [ $? -ne 0 ]; then
    iptables -A OUTPUT -p tcp --tcp-flags RST RST -s <ip> --sport <port> -j DROP
fi
```

**Remove RST drop rule (server side):**
```bash
iptables -D OUTPUT -p tcp --tcp-flags RST RST -s <ip> --sport <port> -j DROP
```

### Bug Fix: Duplicate Rule Prevention

**Issue:** The original implementation uses `-A` (append) without checking if the rule already exists, allowing duplicate rules to be added to the table.

**Solution:** Use `-C` (check) flag to verify the rule exists before adding:
- If rule exists: Skip adding (return success)
- If rule doesn't exist: Add the rule with `-A`

This prevents duplicate rules and keeps the firewall rules clean.

### nftables Equivalent

**nftables uses a table and chain structure:**

```bash
# Create table if it doesn't exist
nft add table ip filter 2>/dev/null || true

# Create output chain if it doesn't exist
nft add chain ip filter output 2>/dev/null || true

# Add rule (with unique handle for removal)
nft add rule ip filter output tcp flags rst flags rst drop

# List rules with handles (for reference)
nft list chain ip filter output

# Remove specific rule by handle
nft delete rule ip filter output handle <handle>
```

---

## Migration Strategy

### Phase 1: Abstraction Layer (Recommended)

Create abstraction functions that work with both iptables and nftables.

```go
// Abstract interface
type PacketFilterer interface {
    AddRule(ip string, port int, direction string) error
    RemoveRule(ip string, port int, direction string) error
}

// Implementations: IptablesFilterer, NftablesFilterer
```

### Phase 2: Detection & Fallback

Detect if nftables is available, fall back to iptables:

```go
func getPacketFilterer() PacketFilterer {
    if isNftablesAvailable() {
        return NewNftablesFilterer()
    }
    return NewIptablesFilterer()
}
```

### Phase 3: Migration Plan

1. **Create abstraction layer** (PacketFilterer interface)
2. **Implement nftables backend** (NftablesFilterer)
3. **Implement detection logic** (choose backend at runtime)
4. **Keep iptables backend** (fallback for older systems)
5. **Test with both backends**
6. **Eventually deprecate iptables** (in future version)

---

## Challenges & Solutions

### Challenge 1: Duplicate Rules ⭐ **CRITICAL BUG FIX**
**Problem:** The original iptables implementation uses `-A` (append) without checking if the rule already exists, allowing duplicate rules to be added to the table.

**Symptom:** Running the application multiple times or reconnecting causes the same rule to be added multiple times, cluttering the firewall rule table and potentially causing performance issues.

**Solution (for both backends):**
- **iptables:** Use `-C` (check) flag before adding with `-A`
  - If rule exists: Skip adding (return success)
  - If rule doesn't exist: Add with `-A`
- **nftables:** Parse `nft list chain` output to check if rule exists before adding

**Implementation:**
```go
// Check if rule already exists (iptables example)
checkCmd := exec.Command("iptables", "-C", "OUTPUT", "-p", "tcp", "--tcp-flags", "RST", "RST",
    "-d", ip, "--dport", strconv.Itoa(port), "-j", "DROP")
if err := checkCmd.Run(); err == nil {
    // Rule already exists, nothing to do
    return nil
}
// Rule doesn't exist, add it
addCmd := exec.Command("iptables", "-A", ...)
```

### Challenge 2: Rule Handles

**Solution:** 
- For nftables: Parse output to get handles before deletion
- For iptables: Use direct rule matching (existing approach)

### Challenge 2: Different Syntax
**Problem:** iptables and nftables have different command syntax.

**Solution:**
- Create abstraction interface (PacketFilterer)
- Implement separate backends for each tool
- Each backend handles its own syntax

### Challenge 3: IPv6 Support
**Problem:** iptables needed separate ip6tables for IPv6.

**Solution:**
- nftables handles IPv4 and IPv6 in single command
- Potential improvement: add IPv6 rule support
- Currently IPv4-only, can add IPv6 later

### Challenge 4: Backwards Compatibility
**Problem:** Some systems still use iptables.

**Solution:**
- Auto-detect available tool at runtime
- Implement both backends
- Fall back gracefully if one is unavailable

---

## Implementation Roadmap

### Step 1: Create Abstraction (Week 1)

```go
// lib/packet_filter.go (NEW FILE)

type PacketFilterer interface {
    // Add rule to drop RST packets
    // direction: "client" (destination-based) or "server" (source-based)
    AddRule(ip string, port int, direction string) error
    RemoveRule(ip string, port int, direction string) error
}

// Function to select appropriate implementation
func NewPacketFilterer() PacketFilterer
```

### Step 2: Implement nftables Backend (Week 1)

```go
type NftablesFilterer struct {
    // Track rules by id for removal
    rules map[string]string  // key: "ip:port:direction", value: "handle"
}

func (n *NftablesFilterer) AddRule(ip string, port int, direction string) error
func (n *NftablesFilterer) RemoveRule(ip string, port int, direction string) error
```

### Step 3: Implement iptables Backend (Week 1)

```go
type IptablesFilterer struct {}

func (i *IptablesFilterer) AddRule(ip string, port int, direction string) error
func (i *IptablesFilterer) RemoveRule(ip string, port int, direction string) error
```

### Step 4: Refactor Existing Code (Week 2)

- Update `lib/pconn.go` to use `PacketFilterer`
- Update `lib/pcpcore.go` to use `PacketFilterer`
- Update `lib/service.go` to use `PacketFilterer`
- Remove old `addIptablesRule()` and `removeIptablesRule()` functions

### Step 5: Testing (Week 2)

- Test with both iptables and nftables systems
- Test rule addition and removal
- Test error handling
- Test fallback mechanism

---

## Implementation Details

### Detection Logic

```go
func isNftablesAvailable() bool {
    cmd := exec.Command("which", "nft")
    err := cmd.Run()
    return err == nil
}

func isIptablesAvailable() bool {
    cmd := exec.Command("which", "iptables")
    err := cmd.Run()
    return err == nil
}

func NewPacketFilterer() PacketFilterer {
    if isNftablesAvailable() {
        return &NftablesFilterer{}
    } else if isIptablesAvailable() {
        return &IptablesFilterer{}
    } else {
        // Fallback: no-op implementation (warning logged)
        return &NoOpFilterer{}
    }
}
```

### nftables Rule Management

```go
// Parse handles from nftables output
func extractHandles(output string) map[string]string {
    // Parse "nft list chain ip filter output" output
    // Extract rule numbers/handles
    // Return map of rule identifiers
}

// Add rule with handle tracking
func (n *NftablesFilterer) AddRule(ip string, port int, direction string) error {
    var cmd *exec.Cmd
    
    if direction == "client" {
        // Drop RST packets destined for this IP:port
        cmd = exec.Command("nft", "add", "rule", "ip", "filter", "output",
            "tcp", "flags", "rst", "flags", "rst",
            "ip", "daddr", ip,
            "tcp", "dport", strconv.Itoa(port),
            "drop")
    } else {
        // Drop RST packets sourced from this IP:port
        cmd = exec.Command("nft", "add", "rule", "ip", "filter", "output",
            "tcp", "flags", "rst", "flags", "rst",
            "ip", "saddr", ip,
            "tcp", "sport", strconv.Itoa(port),
            "drop")
    }
    
    return cmd.Run()
}
```

---

## Testing Plan

### Test Environment Setup

```bash
# Check current firewall tool
sudo update-alternatives --list iptables

# Test both backends
# 1. System with nftables
# 2. System with iptables (via iptables-nft compatibility layer)
```

### Unit Tests

```go
func TestAddRuleNftables(t *testing.T) { ... }
func TestRemoveRuleNftables(t *testing.T) { ... }
func TestAddRuleIptables(t *testing.T) { ... }
func TestRemoveRuleIptables(t *testing.T) { ... }
func TestFallback(t *testing.T) { ... }
```

### Integration Tests

```bash
# Start server with nftables
./server &

# Connect client multiple times
./client &
# Kill server, restart
# Verify rules are added/removed correctly
# Check with: sudo nft list chain ip filter output
```

---

## Future Enhancements

### 1. IPv6 Support
Add rules for both IPv4 and IPv6:
```go
// Add rules for both IPv4 and IPv6
AddRule(ip string, port int, direction string, ipVersion string)
```

### 2. Dynamic Rule Management
Track all rules in a registry:
```go
type RuleRegistry struct {
    rules map[string]Rule  // Persistent tracking
}
```

### 3. Unified Firewall API
Create higher-level API that abstracts firewall operations:
```go
type FirewallManager interface {
    BlockRSTPackets(addr *net.IPAddr, port int) error
    UnblockRSTPackets(addr *net.IPAddr, port int) error
}
```

### 4. Configuration File
Allow selecting firewall tool via config:
```yaml
firewall:
  tool: "nftables"  # or "iptables" or "auto"
  ipv6Support: true
```

---

## Compatibility Notes

### Ubuntu Versions
- **Ubuntu 22.04 LTS and later:** nftables default
- **Ubuntu 20.04 LTS:** Can use either (iptables-nft wrapper available)
- **Ubuntu 18.04 and earlier:** iptables default

### Debian Versions
- **Debian 11 and later:** nftables default
- **Debian 10:** Transition period

### Red Hat / Fedora
- **Fedora 35+:** nftables default
- **RHEL 8+:** nftables preferred

---

## Files to Create

1. **lib/packet_filter.go** - Abstraction interface and factory
2. **lib/packet_filter_nftables.go** - nftables implementation
3. **lib/packet_filter_iptables.go** - iptables implementation (keep existing logic)
4. **lib/packet_filter_test.go** - Unit tests

## Files to Modify

1. **lib/pconn.go** - Use PacketFilterer interface
2. **lib/pcpcore.go** - Use PacketFilterer interface
3. **lib/service.go** - Use PacketFilterer interface

## Files to Remove

1. Remove old `addIptablesRule()` and `removeIptablesRule()` functions (after refactoring)
2. Remove old `addServerIptablesRule()` and `removeServerIptablesRule()` functions (after refactoring)

---

## Migration Checklist

- [ ] Create abstraction layer (packet_filter.go)
- [ ] Implement nftables backend
- [ ] Implement iptables backend (with existing logic)
- [ ] Implement detection logic
- [ ] Refactor pconn.go
- [ ] Refactor pcpcore.go
- [ ] Refactor service.go
- [ ] Add unit tests
- [ ] Add integration tests
- [ ] Test on Ubuntu 22.04 (nftables)
- [ ] Test on Ubuntu 20.04 (both)
- [ ] Test error scenarios
- [ ] Update documentation
- [ ] Code review
- [ ] Merge to stable-linux-only

---

## Timeline

**Estimated:** 2 weeks  
- Week 1: Implementation (abstraction, backends, tests)
- Week 2: Testing, documentation, code review

---

## References

- nftables documentation: https://wiki.nftables.org/
- iptables vs nftables: https://wiki.nftables.org/wiki-nftables/index.php/Nftables_family
- Ubuntu migration: https://ubuntu.com/blog/nftables-migration-for-ubuntu-22-04

---

**Created:** 2025-01-04  
**Branch:** feature/iptables-to-nftables  
**Status:** Planning Phase
