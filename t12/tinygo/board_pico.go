//go:build pico

package main

import (
	"machine"
	"machine/usb/cdc"
	"time"
)

// Only Pico is supported. Other boards must supply their actual USB API
// and board-specific status/heartbeat implementation in a separate file.
var testUSB = cdc.USB

const activeHigh = true // Verify LED polarity and series resistors.

func led(pin machine.Pin, on bool) { pin.Set(on == activeHigh) }
func showTest(on bool)             { led(machine.GP14, on) }
func showWaiting(on bool)          { led(machine.GP15, on) }
func showReading(on bool)          { led(machine.GP26, on) }
func showError()                   { led(machine.GP27, true) }

func boardInit() {
	for _, pin := range [...]machine.Pin{machine.GP14, machine.GP15, machine.GP26, machine.GP27, machine.GP28} {
		pin.Configure(machine.PinConfig{Mode: machine.PinOutput})
		led(pin, false)
	}
	go func() {
		for {
			led(machine.GP28, true)
			time.Sleep(500 * time.Millisecond)
			led(machine.GP28, false)
			time.Sleep(500 * time.Millisecond)
		}
	}()
}
