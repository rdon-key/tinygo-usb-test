package main

import (
	"encoding/binary"
	"fmt"
	"regexp"
	"syscall"
	"unsafe"
)

type serialPort struct{ h syscall.Handle }

func validPort(port string) bool {
	return regexp.MustCompile(`^COM[1-9][0-9]*$`).MatchString(port)
}

func (p *serialPort) Close() error { return syscall.CloseHandle(p.h) }
func (p *serialPort) Read(b []byte) (int, error) {
	var n uint32
	err := syscall.ReadFile(p.h, b, &n, nil)
	return int(n), err
}
func (p *serialPort) Write(b []byte) (int, error) {
	var n uint32
	err := syscall.WriteFile(p.h, b, &n, nil)
	return int(n), err
}

var kernel = syscall.NewLazyDLL("kernel32.dll")

func comm(name string, h syscall.Handle, p unsafe.Pointer) error {
	r, _, e := kernel.NewProc(name).Call(uintptr(h), uintptr(p))
	if r == 0 {
		return fmt.Errorf("%s: %w", name, e)
	}
	return nil
}
func openPort(port string) (*serialPort, error) {
	p, e := syscall.UTF16PtrFromString(`\\.\` + port)
	if e != nil {
		return nil, e
	}
	h, e := syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_EXISTING, 0, 0)
	if e != nil {
		return nil, e
	}
	// Win32 DCB (28 bytes): binary, DTR enabled, RTS enabled, no flow control,
	// 115200 baud, 8 data bits, no parity, one stop bit.
	var dcb [28]byte
	binary.LittleEndian.PutUint32(dcb[:], 28)
	if e = comm("GetCommState", h, unsafe.Pointer(&dcb[0])); e == nil {
		binary.LittleEndian.PutUint32(dcb[4:], 115200)
		binary.LittleEndian.PutUint32(dcb[8:], 0x1011)
		dcb[18] = 8
		dcb[19] = 0
		dcb[20] = 0
		e = comm("SetCommState", h, unsafe.Pointer(&dcb[0]))
	}
	// Each synchronous syscall returns within 50ms (read) / 100ms (write).
	// The outer loop supplies a total deadline and signal cancellation.
	timeouts := [5]uint32{0, 0, 50, 0, 100}
	if e == nil {
		e = comm("SetCommTimeouts", h, unsafe.Pointer(&timeouts[0]))
	}
	if e != nil {
		syscall.CloseHandle(h)
		return nil, e
	}
	return &serialPort{h: h}, nil
}
