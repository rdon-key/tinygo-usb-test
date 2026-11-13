package main

import (
	"machine"
	"machine/usb/cdc"
	"time"
)

const activeHigh = true          // Confirm wiring and resistors before powering LEDs.
func led(p machine.Pin, on bool) { p.Set(on == activeHigh) }

func main() {
	pins := [...]machine.Pin{machine.GP14, machine.GP15, machine.GP26, machine.GP27, machine.GP28}
	for _, p := range pins {
		p.Configure(machine.PinConfig{Mode: machine.PinOutput})
		led(p, false)
	}
	go func() {
		for {
			led(machine.GP28, true)
			time.Sleep(500 * time.Millisecond)
			led(machine.GP28, false)
			time.Sleep(500 * time.Millisecond)
		}
	}()
	usb := cdc.USB // Initialized by the pico runtime.
	var line [32]byte
	var reply [32]byte
	jobs := make(chan int, 1)
	done := make(chan bool, 1)
	go func() {
		for n := range jobs {
			written, err := usb.Write(reply[:n])
			done <- err == nil && written == n
		}
	}()
	n, dropping, sending, halted := 0, false, false, false
	var rxUntil, txUntil, errUntil, sendDeadline time.Time
	for {
		now := time.Now()
		led(machine.GP14, now.Before(rxUntil))
		led(machine.GP15, now.Before(txUntil))
		led(machine.GP27, halted || now.Before(errUntil))
		if sending {
			select {
			case ok := <-done:
				sending = false
				if !ok {
					halted = true
				}
			default:
				if !now.Before(sendDeadline) {
					halted = true
				}
			}
		}
		if !sending && !halted {
			// Bounded work per scheduling turn, including under malformed input.
			for k := 0; k < 32; k++ {
				b, err := usb.ReadByte()
				if err != nil {
					break
				}
				if b == '\n' {
					valid := !dropping && n >= 6 && string(line[:5]) == "PING " && n <= 25
					if valid {
						for _, c := range line[5:n] {
							if c < '0' || c > '9' {
								valid = false
							}
						}
					}
					if valid && usb.DTR() {
						rxUntil = now.Add(30 * time.Millisecond)
						txUntil = rxUntil
						copy(reply[:], line[:n])
						reply[1] = 'O'
						reply[n] = '\n'
						jobs <- n + 1
						sending = true
						sendDeadline = now.Add(time.Second)
					} else {
						errUntil = now.Add(200 * time.Millisecond)
					}
					n = 0
					dropping = false
					if sending {
						break
					}
				} else if n < len(line) && !dropping {
					line[n] = b
					n++
				} else {
					dropping = true
				}
			}
		}
		time.Sleep(time.Millisecond)
	}
}
