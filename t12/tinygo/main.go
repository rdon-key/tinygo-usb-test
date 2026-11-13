package main

import (
	"crypto/sha256"
	"encoding/binary"
	"hash/crc32"
	"time"
)

func halt() {
	showError()
	for {
		time.Sleep(time.Second)
	}
}
func send(b []byte) {
	binary.LittleEndian.PutUint32(b[len(b)-4:], crc32.ChecksumIEEE(b[:len(b)-4]))
	for len(b) > 0 {
		n, e := testUSB.Write(b)
		if e != nil || n <= 0 {
			halt()
		}
		b = b[n:]
	}
}
func main() {
	boardInit()
	for {
		var cmd [24]byte
		used := 0
		var deadline time.Time
		for used < len(cmd) {
			b, e := testUSB.ReadByte()
			if e == nil {
				if used == 0 {
					deadline = time.Now().Add(5 * time.Second)
				}
				cmd[used] = b
				used++
			} else {
				time.Sleep(time.Millisecond)
			}
			if used > 0 && time.Now().After(deadline) {
				halt()
			}
		}
		u := binary.LittleEndian.Uint32
		id, size, pause, observe := u(cmd[4:8]), u(cmd[8:12]), u(cmd[12:16]), u(cmd[16:20])
		if string(cmd[:4]) != "T2C!" || u(cmd[20:]) != crc32.ChecksumIEEE(cmd[:20]) || id == 0 || size == 0 || size > 16*1024*1024 || pause > 60000 || observe == 0 || observe > 60000 {
			halt()
		}
		h := sha256.New()
		showTest(true)
		showWaiting(true)
		var ready [12]byte
		copy(ready[:], "T2R!")
		binary.LittleEndian.PutUint32(ready[4:], id)
		// Do not read application data until the requested pause ends.
		start := time.Now()
		send(ready[:])
		if left := time.Duration(pause)*time.Millisecond - time.Since(start); left > 0 {
			time.Sleep(left)
		}
		pausedUS := uint32(time.Since(start) / time.Microsecond)
		showWaiting(false)
		showReading(true)
		until := time.Now().Add(time.Duration(observe) * time.Millisecond)
		count := uint32(0)
		firstUS := ^uint32(0)
		var buf [64]byte
		for time.Now().Before(until) {
			n, e := testUSB.Read(buf[:])
			if e != nil {
				halt()
			}
			if n == 0 {
				time.Sleep(time.Millisecond)
				continue
			}
			if firstUS == ^uint32(0) {
				firstUS = uint32(time.Since(start) / time.Microsecond)
			}
			h.Write(buf[:n])
			count += uint32(n)
		}
		showReading(false)
		showTest(false)
		if count != size {
			showError()
		}
		var result [60]byte
		copy(result[:], "T2D!")
		for i, v := range [...]uint32{id, count, firstUS, pausedUS, observe} {
			binary.LittleEndian.PutUint32(result[4+i*4:], v)
		}
		copy(result[24:56], h.Sum(nil))
		send(result[:])
		// Require a separate acknowledgement before accepting another command. Late
		// payload cannot be mistaken for a new command; any unexpected byte halts.
		var ack [12]byte
		deadline = time.Now().Add(10 * time.Second)
		for i := 0; i < len(ack); {
			b, e := testUSB.ReadByte()
			if e == nil {
				ack[i] = b
				i++
			} else {
				time.Sleep(time.Millisecond)
			}
			if time.Now().After(deadline) {
				halt()
			}
		}
		if string(ack[:4]) != "T2A!" || u(ack[4:8]) != id || u(ack[8:]) != crc32.ChecksumIEEE(ack[:8]) || count != size {
			halt()
		}
	}
}
