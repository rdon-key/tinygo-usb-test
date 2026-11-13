package main

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

type serialPort struct{ fd int }

func validPort(port string) bool {
	return filepath.IsAbs(port) && !strings.ContainsRune(port, 0)
}

func ioctl(fd int, request uintptr, p unsafe.Pointer) error {
	_, _, err := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), request, uintptr(p))
	if err != 0 {
		return err
	}
	return nil
}

func openPort(port string) (*serialPort, error) {
	fd, err := syscall.Open(port, syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*serialPort, error) {
		syscall.Close(fd)
		return nil, fmt.Errorf("configure %s: %w", port, err)
	}
	// select uses a fixed-size fd set. Reject rather than indexing beyond it.
	var set syscall.FdSet
	if fd >= len(set.Bits)*64 {
		return fail(fmt.Errorf("serial descriptor %d exceeds select fd limit", fd))
	}
	var term syscall.Termios
	if err := ioctl(fd, syscall.TCGETS, unsafe.Pointer(&term)); err != nil {
		return fail(err)
	}
	// Raw 115200 8N1, no flow control; DTR/RTS match the Windows backend.
	term.Iflag, term.Oflag, term.Lflag = 0, 0, 0
	term.Cflag = syscall.B115200 | syscall.CS8 | syscall.CREAD | syscall.CLOCAL
	term.Cc[syscall.VMIN], term.Cc[syscall.VTIME] = 1, 0
	if err := ioctl(fd, syscall.TCSETS, unsafe.Pointer(&term)); err != nil {
		return fail(err)
	}
	bits := int32(syscall.TIOCM_DTR | syscall.TIOCM_RTS)
	if err := ioctl(fd, syscall.TIOCMBIS, unsafe.Pointer(&bits)); err != nil {
		return fail(err)
	}
	return &serialPort{fd: fd}, nil
}

func (p *serialPort) Close() error { return syscall.Close(p.fd) }

func (p *serialPort) transfer(b []byte, write bool) (int, error) {
	var set syscall.FdSet
	set.Bits[p.fd/64] |= 1 << uint(p.fd%64)
	readSet, writeSet := &set, (*syscall.FdSet)(nil)
	wait := 50 * time.Millisecond
	if write {
		readSet, writeSet = nil, &set
		wait = 100 * time.Millisecond
	}
	timeout := syscall.NsecToTimeval(wait.Nanoseconds())
	n, err := syscall.Select(p.fd+1, readSet, writeSet, nil, &timeout)
	if err == syscall.EINTR || (err == nil && n == 0) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if write {
		n, err = syscall.Write(p.fd, b)
	} else {
		n, err = syscall.Read(p.fd, b)
	}
	if err == syscall.EAGAIN || err == syscall.EINTR {
		return 0, nil
	}
	if n < 0 {
		n = 0
	}
	if !write && n == 0 && err == nil {
		err = io.EOF
	}
	return n, err
}

func (p *serialPort) Read(b []byte) (int, error)  { return p.transfer(b, false) }
func (p *serialPort) Write(b []byte) (int, error) { return p.transfer(b, true) }
