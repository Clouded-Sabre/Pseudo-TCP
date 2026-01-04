package main

import (
	"flag"
	"io"
	"log"

	"github.com/Clouded-Sabre/Pseudo-TCP/config"
	"github.com/Clouded-Sabre/Pseudo-TCP/lib"
)

func main() {
	serviceIP := flag.String("serviceIP", "127.0.0.2", "Service IP address to listen on")
	port := flag.Int("port", 8901, "Service port")
	flag.Parse()

	var err error
	config.AppConfig, err = config.ReadConfig("config.yaml")
	if err != nil {
		log.Fatalln("Configurtion file error:", err)
	}

	pcpCoreConfig := &lib.PcpCoreConfig{
		ProtocolID:      uint8(config.AppConfig.ProtocolID),
		PreferredMSS:    config.AppConfig.PreferredMSS,
		PayloadPoolSize: config.AppConfig.PayloadPoolSize,
	}
	pcpCoreObj, err := lib.NewPcpCore(pcpCoreConfig)
	if err != nil {
		log.Println(err)
		return
	}
	defer pcpCoreObj.Close()

	srv, err := pcpCoreObj.ListenPcp(*serviceIP, *port, config.AppConfig)
	if err != nil {
		log.Fatalln("Listen error:", err)
	}

	log.Printf("Echo server listening on %s:%d\n", *serviceIP, *port)

	for {
		conn, err := srv.Accept()
		if err != nil {
			log.Println("Accept error:", err)
			continue
		}
		log.Printf("New connection from %s\n", conn.RemoteAddr())
		go handleConn(conn)
	}
}

func handleConn(c *lib.Connection) {
	defer c.Close()
	buf := make([]byte, config.AppConfig.PreferredMSS)
	for {
		n, err := c.Read(buf)
		if err != nil {
			if err == io.EOF {
				log.Println("Connection closed by client")
				return
			}
			log.Println("Read error:", err)
			return
		}
		log.Printf("Echo server got: %s", string(buf[:n]))
		_, err = c.Write(buf[:n])
		if err != nil {
			log.Println("Write error:", err)
			return
		}
	}
}
