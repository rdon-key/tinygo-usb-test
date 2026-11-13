package main

import (
	"device/rp"
	"encoding/binary"
	"errors"
	"machine"
	"machine/usb/cdc"
	"runtime"
	"sync/atomic"
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
	generation := epoch.Load()
	for {
		if current := epoch.Load(); current != generation {
			used = 0
			total = 10
			generation = current
		}
		for k := 0; k < 64; k++ {
			c, e := cdc.USB.ReadByte()
			if e != nil {
				break
			}
			if used == 0 && c != 0xa5 {
				if active.Load() == 0 {
					rxBad.Add(1)
					fail()
				}
				continue
			}
			if used == 1 && c != 0x5a {
				if active.Load() == 0 {
					rxBad.Add(1)
					fail()
				}
				used = 0
				continue
			}
			b[used] = c
			used++
			if used == 10 {
				n := int(binary.LittleEndian.Uint16(b[8:10]))
				if n > 512 {
					if active.Load() == 0 {
						rxBad.Add(1)
						fail()
					}
					used = 0
					continue
				}
				total = n + 14
			}
			if used == total {
				f, e := decode(b[:total])
				if e != nil {
					if active.Load() == 0 {
						rxBad.Add(1)
						fail()
					}
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

const (
	deviceTX      = true
	schedulerWork = true
)

var rxCore, backgroundReturned, backgroundObserved atomic.Uint32

var active, epoch, transitionDone, txStop, txReturned, rxBad, rxFrames, detachCore, attachCore, backgroundCore, backgroundValue atomic.Uint32

func core() uint32 { return rp.SIO.CPUID.Get() }
func transition(round uint32, delay, off time.Duration) {
	time.Sleep(delay)
	detachCore.Store(core())
	machine.USBDev.Detach()
	epoch.Add(1)
	time.Sleep(off)
	attachCore.Store(core())
	machine.USBDev.Attach()
	epoch.Add(1)
	txStop.Store(1)
	transitionDone.Store(1)
}
func stream(round uint32) {
	defer txReturned.Store(1)
	var j uint32
	for txStop.Load() == 0 {
		f := frame{Kind: 'D', Source: 1, Seq: round*65536 + j, N: 32}
		pattern(&f)
		send(f)
		j++
		runtime.Gosched()
	}
}
func background() {
	defer backgroundReturned.Store(1)
	machine.GP26.Set(activeHigh)
	var x uint32
	for txStop.Load() == 0 {
		for j := 0; j < 4096; j++ {
			x = x*1664525 + 1013904223
		}
		backgroundCore.Store(core())
		backgroundValue.Store(x)
		backgroundObserved.Store(1)
		runtime.Gosched()
	}
	machine.GP26.Set(!activeHigh)
}
func main() {
	setup()
	backgroundReturned.Store(1)
	txReturned.Store(1)
	var armed frame
	for {
		f, ok := receive()
		rxCore.Store(core())
		if !ok {
			continue
		}
		switch f.Kind {
		case 'C':
			if active.Load() != 0 || f.N != 4 {
				fail()
				continue
			}
			armed = f
			send(frame{Kind: 'A', Seq: f.Seq})
		case 'G':
			if f.N != 0 || armed.Kind != 'C' || f.Seq != armed.Seq || active.Load() != 0 {
				fail()
				continue
			}
			lastRound.Store(f.Seq)
			active.Store(1)
			transitionDone.Store(0)
			txStop.Store(0)
			rxFrames.Store(0)
			machine.GP14.Set(activeHigh)
			if deviceTX {
				txReturned.Store(0)
				machine.GP15.Set(activeHigh)
				go stream(f.Seq)
			}
			if schedulerWork {
				backgroundReturned.Store(0)
				backgroundObserved.Store(0)
				go background()
			}
			go transition(f.Seq, time.Duration(binary.LittleEndian.Uint16(armed.Data[:2]))*time.Millisecond, time.Duration(binary.LittleEndian.Uint16(armed.Data[2:4]))*time.Millisecond)
			armed = frame{}
		case 'X':
			// Corruption/loss within G..fresh recovery is expected, but count valid stimulus.
			want := f
			pattern(&want)
			good := f.Source == 0 && f.N == 32
			for j := 0; j < int(f.N); j++ {
				if f.Data[j] != want.Data[j] {
					good = false
				}
			}
			if good && active.Load() != 0 {
				rxFrames.Add(1)
			} else if active.Load() == 0 {
				rxBad.Add(1)
				fail()
			}
		case 'P':
			if active.Load() != 0 {
				if transitionDone.Load() == 0 || f.Seq != lastRound.Load()*65536+60000 {
					continue
				}
				deadline := time.Now().Add(2 * time.Second)
				for (txReturned.Load() == 0 || backgroundReturned.Load() == 0) && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				if txReturned.Load() == 0 || backgroundReturned.Load() == 0 {
					fail()
					f.Kind = 'E'
					send(f)
					continue
				}
				echo(f)
				s := frame{Kind: 'S', Seq: lastRound.Load(), N: 16}
				s.Data[0] = byte(txReturned.Load())
				s.Data[1] = byte(detachCore.Load())
				s.Data[2] = byte(attachCore.Load())
				s.Data[3] = byte(rxCore.Load())
				s.Data[4] = byte(runtime.NumCPU())
				s.Data[5] = byte(backgroundCore.Load())
				s.Data[6] = byte(backgroundReturned.Load())
				s.Data[7] = byte(backgroundObserved.Load())
				binary.LittleEndian.PutUint32(s.Data[8:12], rxBad.Load())
				binary.LittleEndian.PutUint32(s.Data[12:16], rxFrames.Load())
				send(s)
				active.Store(0)
				machine.GP14.Set(!activeHigh)
				machine.GP15.Set(!activeHigh)
			} else {
				echo(f)
			}
		default:
			if active.Load() == 0 {
				rxBad.Add(1)
				fail()
			}
		}
	}
}

var lastRound atomic.Uint32
