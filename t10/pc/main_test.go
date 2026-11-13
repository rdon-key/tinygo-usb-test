package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestFraming(t *testing.T) {
	for _, n := range []int{0, 1, 31, 50, 63, 64, 65, 127, 128, 255, 256, 511, 512} {
		f := frame{Kind: 'R', Seq: 99, N: uint16(n)}
		pattern(&f)
		b := encode(f)
		if checksum(b[:len(b)-4]) != crc32.ChecksumIEEE(b[:len(b)-4]) {
			t.Fatal("CRC reference mismatch")
		}
		r, e := decode(b)
		if e != nil || checkFrame(r, 'R', 0, 99, n) != nil {
			t.Fatalf("n=%d frame=%+v error=%v", n, r, e)
		}
		b[len(b)-1] ^= 1
		if _, e = decode(b); e == nil {
			t.Fatal("corruption accepted")
		}
	}
}
func TestRejectLengthAndSequence(t *testing.T) {
	f := frame{Kind: 'R', Seq: 1, N: 32}
	pattern(&f)
	if checkFrame(f, 'R', 0, 2, 32) == nil {
		t.Fatal("duplicate/old sequence accepted")
	}
	b := encode(f)
	binary.LittleEndian.PutUint16(b[8:10], 513)
	if _, e := decode(b); e == nil {
		t.Fatal("oversize accepted")
	}
}
func TestFragmentedTransport(t *testing.T) {
	fds, e := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_NONBLOCK, 0)
	if e != nil {
		t.Fatal(e)
	}
	defer syscall.Close(fds[0])
	defer syscall.Close(fds[1])
	f := frame{Kind: 'R', Seq: 2, N: 64}
	pattern(&f)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		for _, b := range encode(f) {
			if e := transfer(ctx, fds[1], []byte{b}, true); e != nil {
				done <- e
				return
			}
		}
		done <- nil
	}()
	l := link{fd: fds[0]}
	r, e := l.receive(ctx)
	if e != nil || !bytes.Equal(r.Data[:r.N], f.Data[:f.N]) {
		t.Fatal(e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
}
func TestDeadline(t *testing.T) {
	fds, e := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_NONBLOCK, 0)
	if e != nil {
		t.Fatal(e)
	}
	defer syscall.Close(fds[0])
	defer syscall.Close(fds[1])
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	l := link{fd: fds[0]}
	_, e = l.receive(ctx)
	if classify(e) != "TIMEOUT" {
		t.Fatal(e)
	}
}

func TestCoalescedFrames(t *testing.T) {
	fds, e := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_NONBLOCK, 0)
	if e != nil {
		t.Fatal(e)
	}
	defer syscall.Close(fds[0])
	defer syscall.Close(fds[1])
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	a := frame{Kind: 'R', Seq: 1}
	b := frame{Kind: 'R', Seq: 2}
	wire := append(encode(a), encode(b)...)
	if e = transfer(ctx, fds[1], wire, true); e != nil {
		t.Fatal(e)
	}
	l := link{fd: fds[0]}
	for _, seq := range []uint32{1, 2} {
		f, e := l.receive(ctx)
		if e != nil || f.Seq != seq {
			t.Fatalf("seq=%d error=%v", f.Seq, e)
		}
	}
}

func TestValidCRCWrongPayload(t *testing.T) {
	f := frame{Kind: 'R', Seq: 1, N: 32}
	pattern(&f)
	f.Data[5] ^= 1
	b := encode(f)
	r, e := decode(b)
	if e != nil {
		t.Fatal(e)
	}
	e = checkFrame(r, 'R', 0, 1, 32)
	if e == nil || !bytes.Contains([]byte(e.Error()), []byte("offset=5")) {
		t.Fatalf("missing byte mismatch evidence: %v", e)
	}
}
func socketLink(t *testing.T) (*link, *link, context.Context) {
	t.Helper()
	fds, e := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_NONBLOCK, 0)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { syscall.Close(fds[0]); syscall.Close(fds[1]) })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	return &link{fd: fds[0]}, &link{fd: fds[1]}, ctx
}

func TestTransitionIdentity(t *testing.T) {
	before := device{vid: "2e8a", pid: "000a", sysPath: "/sys/usb/1-4", serial: "abc", bus: "1", address: "5"}
	after := before
	after.address = "6"
	after.port = "/dev/ttyACM8"
	if _, ok := returnedDevice(before, []device{before}); ok {
		t.Fatal("old address accepted")
	}
	if _, ok := returnedDevice(before, []device{after}); !ok {
		t.Fatal("new address rejected")
	}
	wrong := after
	wrong.serial = "other"
	if _, ok := returnedDevice(before, []device{wrong}); ok {
		t.Fatal("wrong identity accepted")
	}
	wrong = after
	wrong.sysPath = "/sys/usb/1-5"
	if _, ok := returnedDevice(before, []device{wrong}); ok {
		t.Fatal("wrong connection accepted")
	}
	if _, ok := returnedDevice(before, []device{after, after}); ok {
		t.Fatal("ambiguous return accepted")
	}
}
func TestDisappearanceDeadline(t *testing.T) {
	original := usbPresent
	defer func() { usbPresent = original }()
	usbPresent = func(device) bool { return true }
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if e := waitGone(ctx, device{}); classify(e) != "TIMEOUT" {
		t.Fatal(e)
	}
	usbPresent = func(device) bool { return false }
	if e := waitGone(context.Background(), device{}); e != nil {
		t.Fatal(e)
	}
}
func TestOffsetsAndStatus(t *testing.T) {
	if _, e := offsets("0s,20ms,2s"); e != nil {
		t.Fatal(e)
	}
	for _, v := range []string{"", "-1ms", "3s", "bad"} {
		if _, e := offsets(v); e == nil {
			t.Fatalf("accepted %s", v)
		}
	}
	f := frame{Kind: 'S', Seq: 4, N: 16}
	f.Data[0] = 1
	f.Data[6] = 1
	f.Data[7] = 1
	if e := checkStatus(f, 4); e != nil {
		t.Fatal(e)
	}
	f.Data[0] = 0
	if checkStatus(f, 4) == nil {
		t.Fatal("stuck worker accepted")
	}
	f.Data[0] = 1
	f.Data[6] = 1
	f.Data[7] = 1
	f.Data[8] = 1
	if checkStatus(f, 4) == nil {
		t.Fatal("outside-window corruption accepted")
	}
}
func TestRecoveryWindowAndStrictFrame(t *testing.T) {
	host, dev, ctx := socketLink(t)
	done := make(chan error, 1)
	go func() {
		p, e := dev.receive(ctx)
		if e != nil {
			done <- e
			return
		}
		p.Kind = 'R'
		noise := append([]byte{1, 2, 3}, encode(frame{Kind: 'D', Source: 1, Seq: 9})...)
		wire := append(noise, encode(p)...)
		bad := encode(frame{Kind: 'R', Seq: 8})
		bad[len(bad)-1] ^= 1
		wire = append(wire, bad...)
		done <- transfer(ctx, dev.fd, wire, true)
	}()
	if e := recoveryPong(ctx, host, 60000); e != nil {
		t.Fatal(e)
	}
	if _, e := host.receive(ctx); e == nil {
		t.Fatal("post-window CRC corruption silently recovered")
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}
func TestScheduledTransitionAndFreshDescriptor(t *testing.T) {
	host, oldDev, parent := socketLink(t)
	fresh, newDev, ctx := socketLink(t)
	_ = parent
	before := device{port: "/dev/ttyACM7", vid: "2e8a", pid: "000a", sysPath: "/sys/usb/1-4", serial: "abc", bus: "1", address: "5"}
	after := before
	after.port = "/dev/ttyACM8"
	after.address = "6"
	host.d = before
	var gone atomic.Bool
	present0, scan0, open0 := usbPresent, scanCDC, openReconnected
	defer func() { usbPresent = present0; scanCDC = scan0; openReconnected = open0 }()
	usbPresent = func(device) bool { return !gone.Load() }
	scanCDC = func() []device {
		if gone.Load() {
			return []device{after}
		}
		return nil
	}
	openReconnected = func(port string) (int, error) {
		if port != after.port {
			t.Errorf("stale port reused: %s", port)
		}
		return fresh.fd, nil
	}
	oldDone, newDone := make(chan error, 1), make(chan error, 1)
	go func() {
		p, e := oldDev.receive(ctx)
		if e != nil {
			oldDone <- e
			return
		}
		p.Kind = 'R'
		if e = oldDev.send(ctx, p); e != nil {
			oldDone <- e
			return
		}
		c, e := oldDev.receive(ctx)
		if e != nil {
			oldDone <- e
			return
		}
		if e = oldDev.send(ctx, frame{Kind: 'A', Seq: c.Seq}); e != nil {
			oldDone <- e
			return
		}
		g, e := oldDev.receive(ctx)
		if e != nil || g.Kind != 'G' {
			oldDone <- e
			return
		}
		if doHostRX {
			for j := 0; j < 4; j++ {
				d := frame{Kind: 'D', Source: 1, Seq: uint32(j), N: 32}
				pattern(&d)
				if e = oldDev.send(ctx, d); e != nil {
					oldDone <- e
					return
				}
			}
		}
		if doHostTX {
			if _, e = oldDev.receive(ctx); e != nil {
				oldDone <- e
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
		gone.Store(true)
		oldDone <- nil
	}()
	go func() {
		p, e := newDev.receive(ctx)
		if e != nil {
			newDone <- e
			return
		}
		p.Kind = 'R'
		if e = newDev.send(ctx, p); e != nil {
			newDone <- e
			return
		}
		s := frame{Kind: 'S', Seq: 1, N: 16}
		s.Data[0] = 1
		s.Data[6] = 1
		s.Data[7] = 1
		s.Data[4] = 2
		if doHostTX {
			binary.LittleEndian.PutUint32(s.Data[12:16], 1)
		}
		if e = newDev.send(ctx, s); e != nil {
			newDone <- e
			return
		}
		p, e = newDev.receive(ctx)
		if e != nil {
			newDone <- e
			return
		}
		p.Kind = 'R'
		newDone <- newDev.send(ctx, p)
	}()
	if e := iteration(ctx, host, 1, options{Offsets: "20ms", Off: 100 * time.Millisecond, Reconnect: time.Second, comm: time.Second}); e != nil {
		t.Fatal(e)
	}
	if host.fd != fresh.fd || host.d.address != "6" {
		t.Fatal("old descriptor/identity retained")
	}
	if e := <-oldDone; e != nil {
		t.Fatal(e)
	}
	if e := <-newDone; e != nil {
		t.Fatal(e)
	}
}

func TestWorkloadProofRequired(t *testing.T) {
	f := frame{Kind: 'S', Seq: 1, N: 16}
	f.Data[0] = 1
	f.Data[6] = 1
	if classify(checkStatus(f, 1)) != "SKIPPED" {
		t.Fatal("missing workload stimulus accepted")
	}
}
