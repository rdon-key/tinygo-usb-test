package main

import (
	"bytes"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"time"
)

func main() {
	port := flag.String("port", "", "serial port: COM12 or /dev/ttyACM0")
	flag.Parse()
	fmt.Println("S1 - USB CDC RX Data Loss Reproducer")
	check := func(err error) {
		if err != nil {
			fmt.Printf("Result   : ERROR (%v)\n", err)
			os.Exit(1)
		}
	}
	if !validPort(*port) || flag.NArg() != 0 {
		check(fmt.Errorf("specify -port; see -help"))
	}
	p, err := openPort(*port)
	check(err)
	defer p.Close()
	data := make([]byte, 1024)
	for i := range data {
		data[i] = byte(i)
	}
	expected := fmt.Sprintf("%x", sha256.Sum256(data))
	timer := time.AfterFunc(10*time.Second, func() {
		fmt.Println("Result   : TIMEOUT")
		os.Exit(1)
	})
	defer timer.Stop()
	_, err = io.Copy(p, bytes.NewReader(data))
	check(err)
	fmt.Println("Sent     : 1024 B")
	var reply [76]byte
	_, err = io.ReadFull(p, reply[:])
	check(err)
	timer.Stop()
	if !regexp.MustCompile(`\A[0-9]{10} [0-9a-f]{64}\n\z`).Match(reply[:]) {
		check(fmt.Errorf("invalid result format"))
	}
	received, err := strconv.ParseUint(string(reply[:10]), 10, 64)
	check(err)
	hash, result := "MISMATCH", "FAIL"
	if string(reply[11:75]) == expected {
		hash = "MATCH"
		if received == 1024 {
			result = "PASS"
		}
	}
	fmt.Printf("Received : %d B\nSHA-256  : %s\nResult   : %s\n", received, hash, result)
	if result == "FAIL" {
		os.Exit(1)
	}
}
