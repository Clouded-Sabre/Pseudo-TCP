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
	handle          *divert.Handle
	stopChan        chan struct{}
	isRunning       bool
	ruleSet         map[string]bool // Track individual rules
	mutex           sync.Mutex
	udpServerFilter *udpServerFilter // Struct containing the shared method
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
		handle:          nil,
		stopChan:        nil,
		isRunning:       false,
		ruleSet:         make(map[string]bool),
		udpServerFilter: NewUdpServerFilter(),
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
func (f *filterImpl) AddUdpServerFiltering(srcAddr string) error { // srcAddr is the source ip address of the UDP server
	return f.udpServerFilter.AddUdpServerFiltering(srcAddr) // call the shared method which is cross-platform implementation
}

// RemoveUdpServerFiltering removes a filtering rule which blocks icmp unreacheable packets from srcAddr.
func (f *filterImpl) RemoveUdpServerFiltering(srcAddr string) error { // srcAddr is the source ip address of the UDP server in "ip:port" format
	return f.udpServerFilter.RemoveUdpServerFiltering(srcAddr) // call the shared method which is cross-platform implementation
}

// AddUdpClientFiltering adds a filtering rule which blocks icmp unreacheable packets to dstAddr.
func (f *filterImpl) AddUdpClientFiltering(dstAddr string) error {
	// we don't use udp raw socket client on windows platform, so we do nothing here
	return nil
}

// RemoveUdpClientFiltering removes a filtering rule which blocks icmp unreacheable packets to dstAddr.
func (f *filterImpl) RemoveUdpClientFiltering(dstAddr string) error {
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
