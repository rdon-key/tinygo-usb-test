package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
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

func receipt(t *testing.T) (powerConfig, string) {
	t.Helper()
	c := powerConfig{Hub: "1-1.4", Port: 2, HubVID: "1234", HubPID: "5678", USBConnection: "1-1.4.2", Serial: "abc", PhysicalVBUSVerified: true, ExclusiveVerified: true, ExactHubOnlyVerified: true, VerifiedAt: "2026-01-01T00:00:00Z", Evidence: "Operator measured target VBUS removal and exclusive scope with exact -e switching."}
	b, _ := json.Marshal(c)
	path := filepath.Join(t.TempDir(), "power.json")
	if e := os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	return c, path
}
func mockHub(t *testing.T, c powerConfig) {
	t.Helper()
	oldAttr, oldTool := powerAttribute, powerTool
	t.Cleanup(func() { powerAttribute = oldAttr; powerTool = oldTool })
	powerTool = func(string) (string, error) { return "/mock/uhubctl", nil }
	powerAttribute = func(path, name string) string {
		if path != "/sys/bus/usb/devices/"+c.Hub {
			return ""
		}
		switch name {
		case "idVendor":
			return c.HubVID
		case "idProduct":
			return c.HubPID
		case "bDeviceClass":
			return "09"
		}
		return ""
	}
}
func TestMissingOrUnverifiedPowerSkipsBeforeTool(t *testing.T) {
	old := hubCommand
	defer func() { hubCommand = old }()
	called := false
	hubCommand = func(context.Context, ...string) (string, error) { called = true; return "", nil }
	if e := preflight(context.Background(), options{}); classify(e) != "SKIPPED" {
		t.Fatal(e)
	}
	_, path := receipt(t)
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	b = bytes.Replace(b, []byte(`"exclusive_verified":true`), []byte(`"exclusive_verified":false`), 1)
	os.WriteFile(path, b, 0600)
	if e = preflight(context.Background(), options{PowerConfig: path}); classify(e) != "SKIPPED" {
		t.Fatal(e)
	}
	if called {
		t.Fatal("unverified config invoked uhubctl")
	}
}
func TestPowerReceiptAndTopology(t *testing.T) {
	c, path := receipt(t)
	if _, hash, e := loadPower(path); e != nil || len(hash) != 64 {
		t.Fatal(hash, e)
	}
	d := device{vid: "2e8a", pid: "000a", sysPath: "/sys/devices/usb/" + c.USBConnection, serial: c.Serial}
	if e := verifyPowerTarget(c, d); e != nil {
		t.Fatal(e)
	}
	d.sysPath = "/sys/devices/usb/1-1.4.3"
	if e := verifyPowerTarget(c, d); classify(e) != "SKIPPED" {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(path)
	b = append(b[:len(b)-1], []byte(`,"unknown_field":1}`)...)
	os.WriteFile(path, b, 0600)
	if _, _, e := loadPower(path); classify(e) != "SKIPPED" {
		t.Fatal("unknown config key accepted", e)
	}
}
func TestSharedHubAndUnsupportedCapabilitySkip(t *testing.T) {
	c, _ := receipt(t)
	mockHub(t, c)
	old := hubCommand
	defer func() { hubCommand = old }()
	hubCommand = func(context.Context, ...string) (string, error) {
		return "Current status for hub 1-1.4 [1234:5678, ganged]\n Port 2: 0103 power enable connect\n", nil
	}
	if _, e := powerQuery(context.Background(), c); classify(e) != "SKIPPED" {
		t.Fatal(e)
	}
	c.HubVID = "0424"
	c.HubPID = "9514"
	mockHub(t, c)
	if e := hubIdentity(c); classify(e) != "SKIPPED" {
		t.Fatal("Pi shared hub accepted", e)
	}
}
func TestPowerRecoveryAndExactSinglePort(t *testing.T) {
	host, oldDev, ctx := socketLink(t)
	fresh, newDev, _ := socketLink(t)
	c, path := receipt(t)
	mockHub(t, c)
	before := device{port: "/dev/ttyACM7", vid: "2e8a", pid: "000a", sysPath: "/sys/devices/usb/" + c.USBConnection, serial: c.Serial, bus: "1", address: "5"}
	after := before
	after.address = "6"
	after.port = "/dev/ttyACM8"
	host.d = before
	oldPresent, oldScan, oldOpen, oldCommand := usbPresent, scanCDC, openReconnected, hubCommand
	defer func() { usbPresent = oldPresent; scanCDC = oldScan; openReconnected = oldOpen; hubCommand = oldCommand }()
	var powered atomic.Bool
	powered.Store(true)
	var returned atomic.Bool
	actions := []string{}
	usbPresent = func(device) bool { return powered.Load() }
	scanCDC = func() []device {
		if returned.Load() {
			return []device{after}
		}
		return nil
	}
	openReconnected = func(port string) (int, error) {
		if port != after.port {
			return -1, errors.New("wrong reopen port")
		}
		return fresh.fd, nil
	}
	hubCommand = func(_ context.Context, args ...string) (string, error) {
		if len(args) == 1 && args[0] == "-v" {
			return "mock version", nil
		}
		if len(args) < 5 || args[0] != "-e" || args[1] != "-l" || args[2] != c.Hub || args[3] != "-p" || args[4] != "2" {
			t.Errorf("unsafe hub arguments: %v", args)
		}
		if len(args) == 5 {
			return "Current status for hub 1-1.4 [1234:5678, ppps]\n Port 2: 0103 power enable connect\n", nil
		}
		if len(args) != 7 || args[5] != "-a" {
			return "", errors.New("bad action")
		}
		actions = append(actions, args[6])
		if args[6] == "off" {
			powered.Store(false)
		} else if args[6] == "on" {
			powered.Store(true)
			returned.Store(true)
		} else {
			t.Errorf("unexpected action %v", args)
		}
		return strings.Join(args, " "), nil
	}
	done := make(chan error, 2)
	go func() {
		f, e := oldDev.receive(ctx)
		if e == nil {
			f.Kind = 'R'
			e = oldDev.send(ctx, f)
		}
		done <- e
	}()
	go func() {
		f, e := newDev.receive(ctx)
		if e == nil {
			f.Kind = 'R'
			e = newDev.send(ctx, f)
		}
		done <- e
	}()
	if e := iteration(ctx, host, 1, options{PowerConfig: path, Off: 250 * time.Millisecond, Reconnect: time.Second, comm: time.Second}); e != nil {
		t.Fatal(e)
	}
	if strings.Join(actions, ",") != "off,on" {
		t.Fatal(actions)
	}
	if host.fd != fresh.fd {
		t.Fatal("old FD reused")
	}
	for j := 0; j < 2; j++ {
		if e := <-done; e != nil {
			t.Fatal(e)
		}
	}
}
func TestOFFFailureStillAttemptsBoundedON(t *testing.T) {
	host, dev, ctx := socketLink(t)
	c, path := receipt(t)
	mockHub(t, c)
	host.d = device{vid: "2e8a", pid: "000a", sysPath: "/sys/devices/usb/" + c.USBConnection, serial: c.Serial}
	old := hubCommand
	defer func() { hubCommand = old }()
	actions := []string{}
	hubCommand = func(_ context.Context, args ...string) (string, error) {
		if len(args) == 1 {
			return "mock", nil
		}
		if len(args) == 5 {
			return "Current status for hub 1-1.4 [1234:5678, ppps]\n Port 2: power\n", nil
		}
		action := args[6]
		actions = append(actions, action)
		if action == "off" {
			return "partial command failure", errors.New("simulated OFF failure")
		}
		return "ON accepted", nil
	}
	done := make(chan error, 1)
	go func() {
		f, e := dev.receive(ctx)
		if e == nil {
			f.Kind = 'R'
			e = dev.send(ctx, f)
		}
		done <- e
	}()
	e := iteration(ctx, host, 1, options{PowerConfig: path, Off: time.Second, Reconnect: time.Second, comm: time.Second})
	if e == nil || !strings.Contains(e.Error(), "OFF failure") {
		t.Fatal(e)
	}
	if strings.Join(actions, ",") != "off,on" {
		t.Fatal(actions)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
}
