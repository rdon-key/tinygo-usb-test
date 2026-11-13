package main

import (
	"encoding/binary"
	"errors"
	"machine"
	"machine/usb/cdc"
	"time"
)

const maxPayload = 512
const headerSize = 10

// Little endian: A5 5A, kind, source, sequence(u32), payload length(u16), payload, CRC32 IEEE.
type frame struct {
	Kind, Source byte
	Seq          uint32
	N            uint16
	Data         [maxPayload]byte
}

func checksum(p []byte) uint32 {
	c := uint32(0xffffffff)
	for _, b := range p {
		c ^= uint32(b)
		for k := 0; k < 8; k++ {
			if c&1 != 0 {
				c = (c >> 1) ^ 0xedb88320
			} else {
				c >>= 1
			}
		}
	}
	return ^c
}
func pattern(f *frame) {
	for i := 0; i < int(f.N); i++ {
		f.Data[i] = byte(uint32(i)*31 + f.Seq*17 + uint32(f.Source)*13)
	}
}
func encode(f frame) []byte {
	var b [526]byte
	b[0] = 0xa5
	b[1] = 0x5a
	b[2] = f.Kind
	b[3] = f.Source
	binary.LittleEndian.PutUint32(b[4:8], f.Seq)
	binary.LittleEndian.PutUint16(b[8:10], f.N)
	copy(b[10:], f.Data[:f.N])
	n := 10 + int(f.N)
	binary.LittleEndian.PutUint32(b[n:n+4], checksum(b[:n]))
	return b[:n+4]
}
func decode(b []byte) (frame, error) {
	var f frame
	if len(b) < 14 || b[0] != 0xa5 || b[1] != 0x5a {
		return f, errors.New("invalid frame magic/header")
	}
	n := int(binary.LittleEndian.Uint16(b[8:10]))
	if n > 512 || len(b) != n+14 {
		return f, errors.New("invalid frame length")
	}
	if checksum(b[:n+10]) != binary.LittleEndian.Uint32(b[n+10:]) {
		return f, errors.New("CRC32 mismatch")
	}
	f.Kind = b[2]
	f.Source = b[3]
	f.Seq = binary.LittleEndian.Uint32(b[4:8])
	f.N = uint16(n)
	copy(f.Data[:], b[10:10+n])
	return f, nil
}

const activeHigh = true

func setup() {
	for _, p := range [...]machine.Pin{machine.GP14, machine.GP15, machine.GP26, machine.GP27, machine.GP28} {
		p.Configure(machine.PinConfig{Mode: machine.PinOutput})
		p.Set(!activeHigh)
	}
	go func() {
		for {
			machine.GP28.Set(activeHigh)
			time.Sleep(500 * time.Millisecond)
			machine.GP28.Set(!activeHigh)
			time.Sleep(500 * time.Millisecond)
		}
	}()
}
func fail() { machine.GP27.Set(activeHigh) }

// Firmware has fixed input storage and one reader. USB Write cannot be cancelled by this API.
func receive() (frame, bool) {
	var b [526]byte
	used, total := 0, 10
	for {
		for k := 0; k < 64; k++ {
			c, e := cdc.USB.ReadByte()
			if e != nil {
				break
			}
			if used == 0 && c != 0xa5 {
				continue
			}
			if used == 1 && c != 0x5a {
				used = 0
				continue
			}
			b[used] = c
			used++
			if used == 10 {
				n := int(binary.LittleEndian.Uint16(b[8:10]))
				if n > 512 {
					fail()
					used = 0
					continue
				}
				total = n + 14
			}
			if used == total {
				f, e := decode(b[:total])
				if e != nil {
					fail()
					return frame{}, false
				}
				return f, true
			}
		}
		time.Sleep(time.Millisecond)
	}
}
func send(f frame) {
	b := encode(f)
	n, e := cdc.USB.Write(b)
	if e != nil || n != len(b) {
		fail()
	}
}
func echo(f frame) {
	if f.Kind != 'P' {
		fail()
		return
	}
	want := f
	pattern(&want)
	for i := 0; i < int(f.N); i++ {
		if f.Data[i] != want.Data[i] {
			fail()
			f.Kind = 'E'
			send(f)
			return
		}
	}
	f.Kind = 'R'
	send(f)
}

func main() {
	setup()
	for {
		f, ok := receive()
		if ok {
			echo(f)
		}
	}
}
