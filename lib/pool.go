package lib

import (
	"fmt"
	"log"

	rp "github.com/Clouded-Sabre/ringpool/lib"
)

var (
	//PoolDebug  = rp.Debug
	emptySlice []byte
	Pool       *rp.RingPool
)

func SetEmptySlice(length int) {
	emptySlice = make([]byte, length)
}

// PayloadBytes represents packet payload byte slice
type Payload struct {
	payloadBytes []byte
	length       int
}

// Define a function that creates a new instance of ConcreteData
func NewPayload(params ...interface{}) rp.DataInterface {
	if len(params) != 1 {
		log.Println("NewPayload: Invalid number of calling parameters. Should be only one: bufferlength")
		return nil
	}

	/*bufferLength, ok := params[0].(int)
	if !ok {
		log.Println("NewPayload: Invalid data type of bufferLength. Should be of type int")
		return nil
	}*/

	pBufferLength := bufferLength // make it 65536 to accommodate the maximum tcp segment size

	if len(emptySlice) == 0 { // initialize it
		SetEmptySlice(pBufferLength)
	}

	return &Payload{
		payloadBytes: make([]byte, pBufferLength),
	}
}

// set the content of the payload
func (p *Payload) SetContent(s string) {
	p.payloadBytes = []byte(s)
	p.length = len(s)
}

// Reset resets the content of the payload
func (p *Payload) Reset() {
	copy(p.payloadBytes, emptySlice)
	p.length = 0
}

// PrintContent prints the content of the payload
func (p *Payload) PrintContent() {
	fmt.Println("Content:", string(p.payloadBytes[:p.length]))
}

func (p *Payload) Copy(src []byte) error {
	if len(src) > len(p.payloadBytes) {
		err := fmt.Errorf("Payload Copy: Source byte slice(%d) is longer than bufferLength(%d)", len(src), len(p.payloadBytes))
		//log.Println(err)
		return err
	}
	if len(src) == 0 {
		err := fmt.Errorf("Payload Copy: Source byte slice is empty")
		//log.Println(err)
		return err
	}
	copy(p.payloadBytes, src)
	p.length = len(src)
	return nil
}

func (p *Payload) GetSlice() []byte {
	return p.payloadBytes[:p.length]
}
