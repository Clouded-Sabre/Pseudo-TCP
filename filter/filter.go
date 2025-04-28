package filter

type Filter interface {
	// AddFilteringRule adds a filtering rule to block RST packets.
	AddTcpClientFiltering(dstAddr string, dstPort int) error // AddFilteringRule adds a filtering rule to block RST packets.
	// RemoveFilteringRule removes a filtering rule to block RST packets.
	RemoveTcpClientFiltering(dstAddr string, dstPort int) error // RemoveFilteringRule removes a filtering rule to block RST packets.                                // finishFiltering flushes all rules.
	AddTcpServerFiltering(dstAddr string, dstPort int) error    // AddFilteringRule adds a filtering rule to block RST packets.
	RemoveTcpServerFiltering(dstAddr string, dstPort int) error // RemoveFilteringRule removes a filtering rule to block RST packets.
	FinishFiltering() error                                     // finishFiltering flushes all rules.
	AddUdpServerFiltering(srcAddr string) error                 // AddFilteringRule adds a filtering rule which blocks icmp unreacheable packets from srcAddr .
	RemoveUdpServerFiltering(srcAddr string) error              // RemoveFilteringRule removes a filtering rule which blocks icmp unreacheable packets from srcAddr.
	AddUdpClientFiltering(dstAddr string) error                 // AddFilteringRule adds a filtering rule which block icmp unreacheable packets to dstAddr.
	RemoveUdpClientFiltering(dstAddr string) error              // RemoveFilteringRule removes a filtering rule which blocks icmp unreacheable packets to dstAddr.
}
