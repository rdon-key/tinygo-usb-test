package main

import (
	"crypto/sha256"
	"fmt"
	"machine/usb/cdc"
	"time"
)

func main() {
	var data [1024]byte
	for {
		if b, err := cdc.USB.ReadByte(); err == nil {
			data[0] = b
			break
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(5 * time.Second)
	count := 1
	deadline := time.Now().Add(3 * time.Second)
	for count < len(data) && time.Now().Before(deadline) {
		if b, err := cdc.USB.ReadByte(); err == nil {
			data[count] = b
			count++
		} else {
			time.Sleep(time.Millisecond)
		}
	}
	reply := []byte(fmt.Sprintf("%010d %x\n", count, sha256.Sum256(data[:count])))
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
