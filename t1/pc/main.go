// t1 host: Linux serial access using only the Go standard library.
package main

import (
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

type device struct{ port, vid, pid, serial, product, sysPath string }

func attr(p, name string) string {
	b, _ := os.ReadFile(filepath.Join(p, name))
	return strings.TrimSpace(string(b))
}
func identify(port string) (device, error) {
	real, err := filepath.EvalSymlinks(port)
	if err != nil {
		return device{}, err
	}
	p, err := filepath.EvalSymlinks(filepath.Join("/sys/class/tty", filepath.Base(real), "device"))
	if err != nil {
		return device{}, err
	}
	for p != "/" && p != "." {
		if v := attr(p, "idVendor"); v != "" {
			return device{real, v, attr(p, "idProduct"), attr(p, "serial"), attr(p, "product"), p}, nil
		}
		p = filepath.Dir(p)
	}
	return device{}, fmt.Errorf("%s: USB parent not found", port)
}
func discover(port string) (device, error) {
	if port != "" {
		return identify(port)
	}
	paths, _ := filepath.Glob("/sys/class/tty/ttyACM*")
	var candidates []device
	for _, p := range paths {
		d, e := identify("/dev/" + filepath.Base(p))
		if e == nil {
			if d.vid == "2e8a" && d.pid == "000a" {
				candidates = append(candidates, d)
			}
		}
	}
	if len(candidates) != 1 {
		return device{}, fmt.Errorf("Pico 2e8a:000a CDC candidates=%d; specify -port after checking USB identity", len(candidates))
	}
	return candidates[0], nil
}
func ioctl(fd int, request uintptr, p unsafe.Pointer) error {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), request, uintptr(p))
	if e != 0 {
		return e
	}
	return nil
}
func openSerial(port string) (int, error) {
	fd, e := syscall.Open(port, syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0)
	if e != nil {
		return -1, e
	}
	fail := func(e error) (int, error) { syscall.Close(fd); return -1, e }
	if e = ioctl(fd, syscall.TIOCEXCL, nil); e != nil {
		return fail(e)
	}
	var t syscall.Termios
	if e = ioctl(fd, syscall.TCGETS, unsafe.Pointer(&t)); e != nil {
		return fail(e)
	}
	t.Iflag = 0
	t.Oflag = 0
	t.Lflag = 0
	t.Cflag = syscall.B115200 | syscall.CS8 | syscall.CREAD | syscall.CLOCAL
	t.Cc[syscall.VMIN] = 0
	t.Cc[syscall.VTIME] = 0
	if e = ioctl(fd, syscall.TCSETS, unsafe.Pointer(&t)); e != nil {
		return fail(e)
	}
	bits := int32(syscall.TIOCM_DTR | syscall.TIOCM_RTS)
	if e = ioctl(fd, syscall.TIOCMBIS, unsafe.Pointer(&bits)); e != nil {
		return fail(e)
	}
	return fd, nil
}
func pause(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
func exchange(ctx context.Context, fd int, port string, seq uint64, timeout time.Duration) (string, string) {
	deadline := time.Now().Add(timeout)
	request := []byte(fmt.Sprintf("PING %d\n", seq))
	expected := fmt.Sprintf("PONG %d\n", seq)
	var response [64]byte
	used, sent := 0, 0
	nextPresenceCheck := time.Now()
	for {
		if ctx.Err() != nil {
			return "ABORTED", fmt.Sprintf("%v; sent=%d response=%q", ctx.Err(), sent, response[:used])
		}
		if !time.Now().Before(deadline) {
			return "TIMEOUT", fmt.Sprintf("sent=%d response=%q", sent, response[:used])
		}
		if !time.Now().Before(nextPresenceCheck) {
			if _, err := os.Stat("/sys/class/tty/" + filepath.Base(port)); err != nil {
				return "FAIL", "device disappeared: " + err.Error()
			}
			nextPresenceCheck = time.Now().Add(100 * time.Millisecond)
		}
		if sent < len(request) {
			n, e := syscall.Write(fd, request[sent:])
			if n > 0 {
				sent += n
			}
			if e != nil && e != syscall.EAGAIN && e != syscall.EINTR {
				return "FAIL", fmt.Sprintf("write: %v", e)
			}
		} else {
			n, e := syscall.Read(fd, response[used:])
			if e != nil && e != syscall.EAGAIN && e != syscall.EINTR {
				return "FAIL", fmt.Sprintf("read: %v; response=%q", e, response[:used])
			}
			if n > 0 {
				used += n
				if strings.Contains(string(response[:used]), "\n") {
					if string(response[:used]) != expected {
						return "FAIL", fmt.Sprintf("expected=%q received=%q", expected, response[:used])
					}
					if time.Now().Before(deadline) {
						return "PASS", ""
					}
					return "TIMEOUT", "response arrived after deadline"
				}
				if used == len(response) {
					return "FAIL", fmt.Sprintf("response overflow: %q", response[:used])
				}
			}
		}
		// Nonblocking syscalls plus short cancellable waits bound communication and cancellation.
		wait := min(time.Millisecond, time.Until(deadline))
		if wait > 0 {
			pause(ctx, wait)
		}
	}
}

const (
	testName     = "T1: USB CDC Baseline Test"
	purpose      = "Repeatedly verify normal USB CDC request/response communication and data integrity."
	procedure    = "Send a numbered PING, wait for the matching PONG, record the outcome, then wait for the test interval; stop at the first abnormal outcome."
	verification = "PASS requires an exact matching PONG line before the communication deadline; inspect the heartbeat visually."
)

func main() { os.Exit(run()) }
func run() int {
	count := flag.Uint64("count", 100, "iterations; 0=unlimited")
	port := flag.String("port", "", "USB CDC device; default discover")
	timeout := flag.Duration("timeout", 5*time.Second, "whole exchange deadline")
	interval := flag.Duration("interval", 100*time.Millisecond, "interval between exchanges")
	output := flag.String("csv", "", "new CSV path; never overwrite")
	flag.Parse()
	if *timeout <= 0 || *interval < 0 {
		fmt.Fprintln(os.Stderr, "timeout must be positive; interval must be nonnegative")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	start := time.Now()
	portLabel := *port
	if portLabel == "" {
		portLabel = "Automatic detection"
	}
	requested := strconv.FormatUint(*count, 10)
	target := fmt.Sprintf("%04d", *count)
	if *count == 0 {
		requested = "Unlimited (count=0)"
		target = "unlimited"
	}
	fmt.Printf("Test ID and Name: %s\nPurpose: %s\nTest Procedure: %s\nVerification Method: %s\nStart Time: %s\nIteration Count: %s\nCommunication Timeout: %s\nTest Interval: %s\nUSB CDC Port: %s\n\n", testName, purpose, procedure, verification, start.Format(time.RFC3339), requested, *timeout, *interval, portLabel)
	if *output == "" {
		if e := os.MkdirAll("t1/results", 0755); e != nil {
			fmt.Fprintln(os.Stderr, e)
			return 2
		}
		*output = filepath.Join("t1/results", "t1-"+start.Format("20060102T150405.000000000-0700")+".csv")
	}
	f, e := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		return 2
	}
	defer f.Close()
	// Optional launcher metadata; standalone command-line behavior is unchanged.
	if metadataPath := os.Getenv("CDCTEST_METADATA"); metadataPath != "" {
		metadata, err := os.ReadFile(metadataPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Run metadata:", err)
			return 2
		}
		for _, line := range strings.Split(strings.TrimSpace(string(metadata)), "\n") {
			if _, err := fmt.Fprintf(f, "# Run Environment: %s\n", strings.TrimSuffix(line, "\r")); err != nil {
				fmt.Fprintln(os.Stderr, "Run metadata:", err)
				return 2
			}
		}
	}
	if _, e = fmt.Fprintf(f, "# Test ID and Name: %s\n# Purpose: %s\n# Host and Device: Raspberry Pi 3 (Linux ARM64, Go host) <-> RP2040 Raspberry Pi Pico (TinyGo, target pico)\n# Test Procedure: %s\n# Protocol: ASCII PING <sequence> + LF; reply PONG <same sequence> + LF\n# Verification Method: %s\n# GP28 Heartbeat: ON for 500ms, OFF for 500ms, independent of USB CDC; heartbeat alone does not prove CDC health\n# Test Parameters: count=%d; timeout=%s; interval=%s; port=%s; csv=%s\n# Test Start Time: %s\n", testName, purpose, procedure, verification, *count, *timeout, *interval, strconv.Quote(portLabel), strconv.Quote(*output), start.Format(time.RFC3339Nano)); e != nil {
		fmt.Fprintln(os.Stderr, "CSV metadata:", e)
		return 2
	}
	w := csv.NewWriter(f)
	write := func(row []string) error {
		if e := w.Write(row); e != nil {
			return e
		}
		w.Flush()
		if e := w.Error(); e != nil {
			return e
		}
		return f.Sync()
	}
	if e = write([]string{"test", "run_start", "requested", "sequence", "start", "end", "elapsed_ns", "outcome", "error", "port", "vid", "pid", "serial", "product", "usb_sysfs"}); e != nil {
		fmt.Fprintln(os.Stderr, e)
		return 2
	}
	fmt.Println("CSV File:", *output)
	totals := map[string]uint64{}
	var total uint64
	fd := -1
	var d device
	defer func() {
		if fd >= 0 {
			syscall.Close(fd)
		}
	}()
	exit := 0
	for seq := uint64(1); *count == 0 || seq <= *count; seq++ {
		if ctx.Err() != nil {
			break
		}
		begin := time.Now()
		outcome, detail := "", ""
		if fd < 0 {
			d, e = discover(*port)
			if e == nil {
				fmt.Printf("Selected USB CDC Port: %s\n\n", d.port)
				fd, e = openSerial(d.port)
			}
		}
		if e != nil {
			outcome = "FAIL"
			detail = "setup: " + e.Error()
		} else {
			outcome, detail = exchange(ctx, fd, d.port, seq, *timeout)
		}
		end := time.Now()
		total++
		totals[outcome]++
		row := []string{"t1", start.Format(time.RFC3339Nano), strconv.FormatUint(*count, 10), strconv.FormatUint(seq, 10), begin.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano), strconv.FormatInt(end.Sub(begin).Nanoseconds(), 10), outcome, detail, d.port, d.vid, d.pid, d.serial, d.product, d.sysPath}
		if err := write(row); err != nil {
			fmt.Fprintln(os.Stderr, "CSV:", err)
			exit = 2
		}
		elapsed := end.Sub(begin).Round(100 * time.Microsecond)
		if elapsed >= time.Second {
			elapsed = end.Sub(begin).Round(time.Millisecond)
		}
		observation := ""
		if detail != "" {
			observation = "  " + detail
		}
		if outcome == "TIMEOUT" {
			observation = "  waiting for matching PONG; " + detail
		}
		fmt.Printf("[%04d/%s] %-7s %s%s\n", seq, target, outcome, elapsed, observation)
		// Preserve the first failure: no flush, reopen, retry, or silent recovery.
		if outcome != "PASS" || exit != 0 {
			if exit == 0 {
				exit = 1
			}
			break
		}
		if *count != 0 && seq == *count {
			break
		}
		if !pause(ctx, *interval) {
			break
		}
	}
	end := time.Now()
	denom := totals["PASS"] + totals["FAIL"] + totals["TIMEOUT"]
	rate := 0.0
	if denom > 0 {
		rate = 100 * float64(totals["FAIL"]+totals["TIMEOUT"]) / float64(denom)
	}
	result := "COMPLETED"
	if exit != 0 {
		result = "FAILED"
	}
	if ctx.Err() != nil {
		result = "INTERRUPTED"
	}
	fmt.Printf("\nResult: %s\nStart Time: %s\nEnd Time: %s\nTotal Duration: %s\nRequested Iterations: %s\nCompleted Iterations: %d (recorded outcomes, including ABORTED)\nPASS: %d  FAIL: %d  TIMEOUT: %d  ABORTED: %d\nFailure Rate: %.2f%% (denominator: PASS + FAIL + TIMEOUT = %d)\nCSV File: %s\n", result, start.Format(time.RFC3339), end.Format(time.RFC3339), end.Sub(start).Round(time.Millisecond), requested, total, totals["PASS"], totals["FAIL"], totals["TIMEOUT"], totals["ABORTED"], rate, denom, *output)
	if errors.Is(ctx.Err(), context.Canceled) {
		return 130
	}
	return exit
}
