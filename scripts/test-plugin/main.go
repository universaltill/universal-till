package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
)

func main() {
	port := os.Getenv("PLUGIN_PORT")
	if port == "" {
		port = "6000"
	}

	addr := fmt.Sprintf("127.0.0.1:%s", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	log.Printf("test-plugin listening on %s", addr)

	conn, err := ln.Accept()
	if err != nil {
		log.Fatalf("accept: %v", err)
	}
	defer conn.Close()

	rd := bufio.NewReader(conn)
	line, err := rd.ReadBytes('\n')
	if err != nil {
		log.Fatalf("read: %v", err)
	}

	var msg map[string]interface{}
	if err := json.Unmarshal(line, &msg); err != nil {
		log.Printf("invalid json: %v", err)
		return
	}

	// simple behaviour: if event name present, ack ok
	typ, _ := msg["type"].(string)
	if typ == "event" {
		ack := map[string]string{"type": "ack", "status": "ok"}
		b, _ := json.Marshal(ack)
		b = append(b, '\n')
		conn.Write(b)
		return
	}

	// default: nack
	nack := map[string]string{"type": "ack", "status": "error", "message": "unknown message"}
	b, _ := json.Marshal(nack)
	b = append(b, '\n')
	conn.Write(b)
}
