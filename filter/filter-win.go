//go:build windows
// +build windows

package filter

import (
	"errors"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	divert "github.com/imgk/divert-go"
)

type filterImpl struct {
	handle    *divert.Handle
	stopChan  chan struct{}
	isRunning bool
	ruleSet   map[string]bool // Track individual rules
	mutex     sync.Mutex
	udpSrcMap sync.Map // Map to store UDP source addresses and ports to udp connections
}

/*var (
	handle    *divert.Handle
	stopChan  chan struct{}
	isRunning bool
	ruleSet   = make(map[string]bool) // Track individual rules
	mutex     sync.Mutex
)*/

// NewFilter creates a new filter instance
func NewFilter(identifier string) (Filter, error) {
	if err := checkDependencies(); err != nil {
		log.Fatalf("Dependency check failed: %v", err)
	}

	return &filterImpl{
		handle:    nil,
		stopChan:  nil,
		isRunning: false,
		ruleSet:   make(map[string]bool),
	}, nil
}

// addAFilteringRule adds a precise rule to filter TCP RST packets
func (f *filterImpl) AddTcpClientFiltering(dstAddr string, dstPort int) error {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	ruleKey := fmt.Sprintf("%s:%d", dstAddr, dstPort)
	if f.ruleSet[ruleKey] {
		return fmt.Errorf("rule already exists: %s", ruleKey)
	}

	if !f.isRunning {
		filter := "tcp.Rst" // Capture TCP RST packets
		h, err := divert.Open(filter, divert.LayerNetwork, 0, 0)
		if err != nil {
			return err
		}
		f.handle = h
		f.stopChan = make(chan struct{})
		f.isRunning = true

		go f.runFilteringLoop() // Start filter loop
	}

	f.ruleSet[ruleKey] = true
	return nil
}

// removeAFilteringRule removes a specific filtering rule
func (f *filterImpl) RemoveTcpClientFiltering(dstAddr string, dstPort int) error {
	f.mutex.Lock()

	ruleKey := fmt.Sprintf("%s:%d", dstAddr, dstPort)
	if !f.ruleSet[ruleKey] {
		return fmt.Errorf("rule not found: %s", ruleKey)
	}

	delete(f.ruleSet, ruleKey)

	if len(f.ruleSet) == 0 {
		f.mutex.Unlock()
		f.FinishFiltering() // Clean up if no rules remain
		f.mutex.Lock()
	}

	f.mutex.Unlock()
	return nil
}

// removeAnchor removes all filtering rules and stops the WinDivert handle
func (f *filterImpl) FinishFiltering() error {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	if !f.isRunning {
		return errors.New("no active filtering rules")
	}

	close(f.stopChan)
	f.isRunning = false
	f.ruleSet = make(map[string]bool) // Clear all stored rules
	return nil
}

func (f *filterImpl) runFilteringLoop() {
	defer func() {
		f.mutex.Lock()
		f.handle.Close()
		f.isRunning = false
		f.mutex.Unlock()
	}()

	buf := make([]byte, 1500)
	addr := divert.Address{}

	for {
		select {
		case <-f.stopChan:
			log.Println("Stopping filter...")
			return
		default:
			n, err := f.handle.Recv(buf, &addr)
			if err != nil {
				log.Println("Failed to receive packet:", err)
				continue
			}

			packet := gopacket.NewPacket(buf[:n], layers.LayerTypeIPv4, gopacket.Default)
			if packet == nil {
				continue
			}

			ipv4Layer := packet.Layer(layers.LayerTypeIPv4)
			if ipv4Layer == nil {
				continue
			}
			ipv4, _ := ipv4Layer.(*layers.IPv4)

			tcpLayer := packet.Layer(layers.LayerTypeTCP)
			if tcpLayer == nil {
				continue
			}
			tcp, _ := tcpLayer.(*layers.TCP)

			ruleKey := fmt.Sprintf("%s:%d", ipv4.DstIP, tcp.DstPort)
			if f.ruleSet[ruleKey] {
				log.Printf("Dropping RST packet: %s", ruleKey)
				continue
			}

			if _, err := f.handle.Send(buf[:n], &addr); err != nil {
				log.Println("Failed to reinject packet:", err)
			}
		}
	}
}

// addAFilteringRule adds an iptables rule to block RST packets originating from the given IP and port.
func (f *filterImpl) AddTcpServerFiltering(srcAddr string, srcPort int) error {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	ruleKey := fmt.Sprintf("%s:%d", srcAddr, srcPort)
	if f.ruleSet[ruleKey] {
		return fmt.Errorf("rule already exists: %s", ruleKey)
	}

	// Define the WinDivert filter string to block outgoing RST packets from srcAddr and srcPort
	filter := fmt.Sprintf("tcp and tcp.Rst == 1 and ip.SrcAddr == %s and tcp.SrcPort == %d", srcAddr, srcPort)

	// Open a WinDivert handle with the specified filter
	handle, err := divert.Open(filter, divert.LayerNetwork, 0, divert.FlagDrop)
	if err != nil {
		return fmt.Errorf("failed to open WinDivert handle: %v", err)
	}

	// Store the handle and start the filtering loop if not already running
	if !f.isRunning {
		f.handle = handle
		f.stopChan = make(chan struct{})
		f.isRunning = true
		go f.runFilteringLoop()
	}

	// Add the rule to the rule set
	f.ruleSet[ruleKey] = true
	log.Printf("Successfully added server filtering rule for source address: %s and port: %d\n", srcAddr, srcPort)
	return nil
}

func (f *filterImpl) RemoveTcpServerFiltering(srcAddr string, srcPort int) error {
	f.mutex.Lock()

	ruleKey := fmt.Sprintf("%s:%d", srcAddr, srcPort)
	if !f.ruleSet[ruleKey] {
		f.mutex.Unlock()
		return fmt.Errorf("rule not found: %s", ruleKey)
	}

	// Remove the rule from the rule set
	delete(f.ruleSet, ruleKey)

	// Clean up if no rules remain
	if len(f.ruleSet) == 0 {
		f.mutex.Unlock()
		f.FinishFiltering()
		return nil
	}

	f.mutex.Unlock()
	return nil
}

// AddUdpServerFiltering adds a filtering rule which blocks icmp unreacheable packets from srcAddr.
func (f *filterImpl) AddUdpServerFiltering(srcAddr string) error {
	/*
		f.mutex.Lock()
		defer f.mutex.Unlock()

		// Check if the rule already exists
		if f.ruleSet[srcAddr] {
			return fmt.Errorf("rule already exists for source address: %s", srcAddr)
		}

		// Define the WinDivert filter string to block ICMP type 3/3 packets from srcAddr
		filter := fmt.Sprintf("icmp and icmp.Type == 3 and icmp.Code == 3 and ip.SrcAddr == %s", srcAddr)

		// Open a WinDivert handle with the specified filter
		handle, err := divert.Open(filter, divert.LayerNetwork, 0, divert.FlagDrop)
		if err != nil {
			return fmt.Errorf("failed to open WinDivert handle: %v", err)
		}

		// Store the handle and start the filtering loop if not already running
		if !f.isRunning {
			f.handle = handle
			f.stopChan = make(chan struct{})
			f.isRunning = true
			go f.runIcmpFilteringLoop()
		}

		// Add the rule to the rule set
		f.ruleSet[srcAddr] = true
		log.Printf("Successfully added ICMP filtering rule for source address: %s\n", srcAddr)
	*/

	// we don't use udp raw socket server on windows platform, so we do nothing here
	return nil
}

/*func (f *filterImpl) runIcmpFilteringLoop() {
	defer func() {
		f.handle.Close()
		f.isRunning = false
	}()

	buf := make([]byte, 1500)
	addr := divert.Address{}

	for {
		select {
		case <-f.stopChan:
			log.Println("Stopping ICMP filter...")
			return
		default:
			// Receive packets from WinDivert
			n, err := f.handle.Recv(buf, &addr)
			if err != nil {
				log.Println("Failed to receive packet:", err)
				continue
			}

			// Parse the packet using gopacket
			packet := gopacket.NewPacket(buf[:n], layers.LayerTypeIPv4, gopacket.Default)
			if packet == nil {
				continue
			}

			// Extract the IPv4 layer
			ipv4Layer := packet.Layer(layers.LayerTypeIPv4)
			if ipv4Layer == nil {
				continue
			}
			ipv4, _ := ipv4Layer.(*layers.IPv4)

			// Extract the ICMP layer
			icmpLayer := packet.Layer(layers.LayerTypeICMPv4)
			if icmpLayer == nil {
				continue
			}
			icmp, _ := icmpLayer.(*layers.ICMPv4)

			// Check if the packet matches the rule
			if icmp.TypeCode.Type() == layers.ICMPv4TypeDestinationUnreachable &&
				icmp.TypeCode.Code() == 3 {
				log.Printf("Dropping ICMP packet from %s\n", ipv4.SrcIP.String())
				continue // Drop the packet
			}

			// Reinject the packet if it does not match the rule
			if _, err := f.handle.Send(buf[:n], &addr); err != nil {
				log.Println("Failed to reinject packet:", err)
			}
		}
	}
}*/

// RemoveUdpServerFiltering removes a filtering rule which blocks icmp unreacheable packets from srcAddr.
func (f *filterImpl) RemoveUdpServerFiltering(srcAddr string) error {
	/*
		f.mutex.Lock()

		// Check if the rule exists
		if !f.ruleSet[srcAddr] {
			f.mutex.Unlock()
			return fmt.Errorf("rule not found for source address: %s", srcAddr)
		}

		// Remove the rule from the rule set
		delete(f.ruleSet, srcAddr)

		// Clean up if no rules remain
		if len(f.ruleSet) == 0 {
			f.mutex.Unlock()
			f.FinishFiltering()
			return nil
		}

		f.mutex.Unlock()
	*/

	// we don't use udp raw socket server on windows platform, so we do nothing here
	return nil
}

// AddUdpClientFiltering adds a filtering rule which blocks icmp unreacheable packets to dstAddr.
func (f *filterImpl) AddUdpClientFiltering(dstAddr string) error {
	/*
		f.mutex.Lock()
		defer f.mutex.Unlock()

		// Check if the rule already exists
		if f.ruleSet[dstAddr] {
			return fmt.Errorf("rule already exists for destination address: %s", dstAddr)
		}

		// Define the WinDivert filter string to block ICMP type 3/3 packets to dstAddr
		filter := fmt.Sprintf("icmp and icmp.Type == 3 and icmp.Code == 3 and ip.DstAddr == %s", dstAddr)

		// Open a WinDivert handle with the specified filter
		handle, err := divert.Open(filter, divert.LayerNetwork, 0, divert.FlagDrop)
		if err != nil {
			return fmt.Errorf("failed to open WinDivert handle: %v", err)
		}

		// Store the handle and start the filtering loop if not already running
		if !f.isRunning {
			f.handle = handle
			f.stopChan = make(chan struct{})
			f.isRunning = true
			go f.runIcmpFilteringLoop()
		}

		// Add the rule to the rule set
		f.ruleSet[dstAddr] = true
		log.Printf("Successfully added ICMP filtering rule for destination address: %s\n", dstAddr)
	*/

	// we don't use udp raw socket client on windows platform, so we do nothing here
	return nil
}

// RemoveUdpClientFiltering removes a filtering rule which blocks icmp unreacheable packets to dstAddr.
func (f *filterImpl) RemoveUdpClientFiltering(dstAddr string) error {
	/*
		f.mutex.Lock()

		// Check if the rule exists
		if !f.ruleSet[dstAddr] {
			f.mutex.Unlock()
			return fmt.Errorf("rule not found for destination address: %s", dstAddr)
		}

		// Remove the rule from the rule set
		delete(f.ruleSet, dstAddr)

		// Clean up if no rules remain
		if len(f.ruleSet) == 0 {
			f.mutex.Unlock()
			f.FinishFiltering()
			return nil
		}

		f.mutex.Unlock()
	*/

	// we don't use udp raw socket client on windows platform, so we do nothing here
	return nil
}

func isWinDivertInstalled() error {
	// Attempt to open a dummy WinDivert handle
	handle, err := divert.Open("true", divert.LayerNetwork, 0, 0)
	if err != nil {
		return fmt.Errorf("WinDivert is not installed or not functioning properly: %v", err)
	}
	defer handle.Close()

	// If no error, WinDivert is installed and working
	return nil
}

// Check if the Npcap service is running
func isNpcapInstalled() error {
	// Use the `sc query` command to check the Npcap service
	cmd := exec.Command("sc", "query", "npcap")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} // Hide the command window
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("Npcap service is not installed or not running: %v\nOutput: %s", err, string(output))
	}

	// Check if the service is running
	if !strings.Contains(string(output), "RUNNING") {
		return fmt.Errorf("Npcap service is installed but not running")
	}

	// If no error, Npcap is installed and running
	return nil
}

func checkDependencies() error {
	// Check if WinDivert is installed
	if err := isWinDivertInstalled(); err != nil {
		return fmt.Errorf("WinDivert check failed: %v", err)
	}

	// Check if Npcap is installed
	if err := isNpcapInstalled(); err != nil {
		return fmt.Errorf("Npcap check failed: %v", err)
	}

	// Both dependencies are installed and functioning
	return nil
}
