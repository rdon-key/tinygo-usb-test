package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
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

func TestIterationEcho(t *testing.T) {
	host, dev, ctx := socketLink(t)
	done := make(chan error, 1)
	go func() {
		for j := 0; j < 16; j++ {
			f, e := dev.receive(ctx)
			if e != nil {
				done <- e
				return
			}
			f.Kind = 'R'
			if e = dev.send(ctx, f); e != nil {
				done <- e
				return
			}
		}
		done <- nil
	}()
	if e := iteration(ctx, host, 1, options{}); e != nil {
		t.Fatal(e)
	}
	if e := <-done; e != nil {
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

func TestPayloadSelection(t *testing.T) {
	a, e := payloadLengths("0,64,512")
	if e != nil || len(a) != 3 || a[2] != 512 {
		t.Fatal(a, e)
	}
	for _, s := range []string{"-1", "513", "bad"} {
		if _, e = payloadLengths(s); e == nil {
			t.Fatalf("accepted %s", s)
		}
	}
}
