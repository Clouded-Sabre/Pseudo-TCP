# Summary of Changes to Linux Packet Filtering

This document outlines the recent updates made to the packet filtering implementation for Linux (`filter/filter-linux.go`).

## Key Changes

### 1. Added Support for nftables

The Linux filtering logic has been enhanced to support `nftables` as a backend, in addition to the existing `iptables` support.

- The system now automatically detects the available firewall management tool upon initialization.
- `nftables` is now the preferred tool. If `nftables` is detected on the system, it will be used for all firewall rule manipulations. `iptables` will be used as a fallback if `nftables` is not available.

This change modernizes the filtering mechanism and provides flexibility for different Linux environments.

### 2. Fixed Bug: Duplicate Firewall Rules

A bug has been fixed that could cause the same firewall rule to be applied multiple times.

- The code now verifies if a specific rule already exists before attempting to add it.
- This prevents redundancy in the firewall configuration and makes the filtering operations idempotent and more efficient.

## Minor Changes

- Minor corrections were also made in `filter/filter.go`.
