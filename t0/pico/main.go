package main

import (
	"machine"
	"machine/usb/cdc"
	"time"
)

const activeHigh = true // Verify LED polarity and current-limiting resistors.

func setLED(pin machine.Pin, on bool) { pin.Set(on == activeHigh) }

func command(line []byte) {
	var pin machine.Pin
	on := false
	switch string(line) {
	case "ALL OFF":
		for _, p := range [...]machine.Pin{machine.GP14, machine.GP15, machine.GP26, machine.GP27} {
			setLED(p, false)
		}
		return
	case "LED 14 ON":
		pin = machine.GP14
		on = true
	case "LED 14 OFF":
		pin = machine.GP14
	case "LED 15 ON":
		pin = machine.GP15
		on = true
	case "LED 15 OFF":
		pin = machine.GP15
	case "LED 26 ON":
		pin = machine.GP26
		on = true
	case "LED 26 OFF":
		pin = machine.GP26
	case "LED 27 ON":
		pin = machine.GP27
		on = true
	case "LED 27 OFF":
		pin = machine.GP27
	default:
		return // Includes attempts to control GP28.
	}
	setLED(pin, on)
}

func main() {
	for _, p := range [...]machine.Pin{machine.GP14, machine.GP15, machine.GP26, machine.GP27, machine.GP28} {
		p.Configure(machine.PinConfig{Mode: machine.PinOutput})
		setLED(p, false)
	}
	go func() {
		for {
			setLED(machine.GP28, true)
			time.Sleep(500 * time.Millisecond)
			setLED(machine.GP28, false)
			time.Sleep(500 * time.Millisecond)
		}
	}()
	var line [32]byte
	n, overflow := 0, false
	for {
		// Bound each receiving turn so continuous input cannot starve the heartbeat.
		for k := 0; k < 32; k++ {
			b, err := cdc.USB.ReadByte()
			if err != nil {
				break
			}
			if b == '\n' {
				if !overflow {
					command(line[:n])
				}
				n = 0
				overflow = false
			} else if !overflow && n < len(line) {
				line[n] = b
				n++
			} else {
				overflow = true
			}
		}
		time.Sleep(time.Millisecond)
	}
}
