//go:build windows
// +build windows

package lib

import (
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	divert "github.com/imgk/divert-go"
)

var (
	handle    *divert.Handle
	stopChan  chan struct{}
	isRunning bool
	ruleSet   = make(map[string]bool) // Track individual rules
	mutex     sync.Mutex
)

// addAFilteringRule adds a precise rule to filter TCP RST packets
func addAFilteringRule(srcAddr, dstAddr string, srcPort, dstPort int) error {
	mutex.Lock()
	defer mutex.Unlock()

	ruleKey := fmt.Sprintf("%s:%d->%s:%d", srcAddr, srcPort, dstAddr, dstPort)
	if ruleSet[ruleKey] {
		return fmt.Errorf("rule already exists: %s", ruleKey)
	}

	if !isRunning {
		filter := "tcp.Rst" // Capture TCP RST packets
		h, err := divert.Open(filter, divert.LayerNetwork, 0, 0)
		if err != nil {
			return err
		}
		handle = h
		stopChan = make(chan struct{})
		isRunning = true

		go runFilteringLoop() // Start filter loop
	}

	ruleSet[ruleKey] = true
	return nil
}

// removeAFilteringRule removes a specific filtering rule
func removeAFilteringRule(srcAddr, dstAddr string, srcPort, dstPort int) error {
	mutex.Lock()
	defer mutex.Unlock()

	ruleKey := fmt.Sprintf("%s:%d->%s:%d", srcAddr, srcPort, dstAddr, dstPort)
	if !ruleSet[ruleKey] {
		return fmt.Errorf("rule not found: %s", ruleKey)
	}

	delete(ruleSet, ruleKey)

	if len(ruleSet) == 0 {
		finishFiltering() // Clean up if no rules remain
	}

	return nil
}

// removeAnchor removes all filtering rules and stops the WinDivert handle
func finishFiltering() error {
	mutex.Lock()
	defer mutex.Unlock()

	if !isRunning {
		return errors.New("no active filtering rules")
	}

	close(stopChan)
	isRunning = false
	ruleSet = make(map[string]bool) // Clear all stored rules
	return nil
}

func runFilteringLoop() {
	defer func() {
		mutex.Lock()
		handle.Close()
		isRunning = false
		mutex.Unlock()
	}()

	buf := make([]byte, 1500)
	addr := divert.Address{}

	for {
		select {
		case <-stopChan:
			log.Println("Stopping filter...")
			return
		default:
			n, err := handle.Recv(buf, &addr)
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

			ruleKey := fmt.Sprintf("%s:%d->%s:%d", ipv4.SrcIP, tcp.SrcPort, ipv4.DstIP, tcp.DstPort)
			if ruleSet[ruleKey] {
				log.Printf("Dropping RST packet: %s", ruleKey)
				continue
			}

			if _, err := handle.Send(buf[:n], &addr); err != nil {
				log.Println("Failed to reinject packet:", err)
			}
		}
	}
}
