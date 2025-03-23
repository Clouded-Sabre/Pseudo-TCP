package filter

type Filter interface {
	// AddFilteringRule adds a filtering rule to block RST packets.
	AddAClientFilteringRule(dstAddr string, dstPort int) error // AddFilteringRule adds a filtering rule to block RST packets.
	// RemoveFilteringRule removes a filtering rule to block RST packets.
	RemoveAClientFilteringRule(dstAddr string, dstPort int) error // RemoveFilteringRule removes a filtering rule to block RST packets.                                // finishFiltering flushes all rules.
	AddAServerFilteringRule(dstAddr string, dstPort int) error    // AddFilteringRule adds a filtering rule to block RST packets.
	RemoveAServerFilteringRule(dstAddr string, dstPort int) error // RemoveFilteringRule removes a filtering rule to block RST packets.
	FinishFiltering() error                                       // finishFiltering flushes all rules.
	AddIcmpSrcFilteringRule(srcAddr string) error                 // AddFilteringRule adds a filtering rule which blocks icmp unreacheable packets from srcAddr .
	RemoveIcmpSrcFilteringRule(srcAddr string) error              // RemoveFilteringRule removes a filtering rule which blocks icmp unreacheable packets from srcAddr.
	AddIcmpDstFilteringRule(dstAddr string) error                 // AddFilteringRule adds a filtering rule which block icmp unreacheable packets to dstAddr.
	RemoveIcmpDstFilteringRule(dstAddr string) error              // RemoveFilteringRule removes a filtering rule which blocks icmp unreacheable packets to dstAddr.
}
