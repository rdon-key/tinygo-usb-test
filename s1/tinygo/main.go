package main

import (
	"crypto/sha256"
	"fmt"
	"machine/usb/cdc"
	"time"
)

func main() {
	hash := sha256.New()
	var data [1]byte
	for {
		if b, err := cdc.USB.ReadByte(); err == nil {
			data[0] = b
			hash.Write(data[:])
			break
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(5 * time.Second)
	count := 1
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := cdc.USB.ReadByte(); err == nil {
			data[0] = b
			hash.Write(data[:])
			count++
		} else {
			time.Sleep(time.Millisecond)
		}
	}
	reply := []byte(fmt.Sprintf("%010d %x\n", count, hash.Sum(nil)))
	for len(reply) > 0 {
		n, err := cdc.USB.Write(reply)
		if err != nil || n == 0 {
			break
		}
		reply = reply[n:]
	}
	for {
		time.Sleep(time.Second)
	}
}
