// t0 host: Linux, standard library only. SENT means host write completion, not LED verification.
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

// Nonblocking writes have a deadline, including the final ALL OFF attempt.
func send(ctx context.Context, fd int, wire string) error {
	for len(wire) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := syscall.Write(fd, []byte(wire))
		if n > 0 {
			wire = wire[n:]
		}
		if err != nil && err != syscall.EAGAIN && err != syscall.EINTR {
			return err
		}
		if len(wire) > 0 {
			if !pause(ctx, time.Millisecond) {
				return ctx.Err()
			}
		}
	}
	return ctx.Err()
}
func outcome(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "ABORTED"
	case errors.Is(err, context.DeadlineExceeded):
		return "TIMEOUT"
	case err != nil:
		return "FAIL"
	default:
		return "SENT"
	}
}

const (
	testName     = "T0: USB CDC and LED Operation Check"
	purpose      = "Visually check USB CDC command control and LED wiring."
	procedure    = "Send ALL OFF; turn GP14, GP15, GP26, GP27 ON then OFF one at a time; repeat; send ALL OFF on exit."
	verification = "Visual inspection only; SENT confirms host writes, not LED operation."
)

func main() { os.Exit(run()) }
func run() (exit int) {
	count := flag.Uint64("count", 1, "cycles; 0=unlimited")
	port := flag.String("port", "", "USB CDC port; default discover")
	hold := flag.Duration("hold", time.Second, "time each LED stays on")
	flag.Parse()
	if *hold <= 0 {
		fmt.Fprintln(os.Stderr, "hold must be positive")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	start := time.Now()
	portLabel := *port
	if portLabel == "" {
		portLabel = "Automatic detection"
	}
	fmt.Printf("Test ID and Name: %s\nPurpose: %s\nTest Procedure: %s\nVerification Method: %s\nStart Time: %s\nIteration Count: %d (0 = unlimited)\nLED Hold Duration: %s\nUSB CDC Port: %s\n\n", testName, purpose, procedure, verification, start.Format(time.RFC3339), *count, *hold, portLabel)
	if err := os.MkdirAll("t0/results", 0755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	path := os.Getenv("CDCTEST_CSV")
	if path == "" {
		path = filepath.Join("t0/results", "t0-"+start.Format("20060102T150405.000000000-0700")+".csv")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
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
	fmt.Println("CSV File:", path)
	w := csv.NewWriter(f)
	var logErr error
	// Resolve the port before writing metadata; report any setup error below.
	d, setupErr := discover(*port)
	resolvedPort := d.port
	if resolvedPort == "" {
		resolvedPort = "Unavailable"
	}
	if _, err = fmt.Fprintf(f, "# Test ID and Name: %s\n# Purpose: %s\n# Host and Device: Raspberry Pi 3 (Linux ARM64, Go host) -> RP2040 Raspberry Pi Pico (TinyGo, target pico)\n# Test Procedure: %s\n# LED GPIO Assignment: GP14=LED 1; GP15=LED 2; GP26=LED 3; GP27=LED 4; GP28=heartbeat only\n# Heartbeat Behavior: GP28 ON for 500ms, OFF for 500ms, independent of USB CDC\n# Verification Method: %s\n# Test Parameters: count=%d; hold=%s; port=%s; resolved_port=%s\n# Test Start Time: %s\n", testName, purpose, procedure, verification, *count, *hold, strconv.Quote(portLabel), strconv.Quote(resolvedPort), start.Format(time.RFC3339Nano)); err != nil {
		fmt.Fprintln(os.Stderr, "CSV metadata:", err)
		return 2
	}
	var errorCount uint64
	iterationStart := start
	log := func(event string, seq uint64, begin time.Time, status, command, detail string) {
		end := time.Now()
		if status == "FAIL" || status == "TIMEOUT" || status == "ABORTED" {
			if event != "ITERATION" && event != "RUN_END" && status != "ABORTED" {
				errorCount++
			}
			if event != "RUN_END" {
				fmt.Fprintf(os.Stderr, "[%04d] %s %s: command=%q; %s\n", seq, event, status, command, detail)
			}
		} else if event == "COMMAND" {
			fields := strings.Fields(command)
			if len(fields) == 3 && fields[0] == "LED" {
				fmt.Printf("[%04d] GP%s %-3s sent  (+%s)\n", seq, fields[1], fields[2], end.Sub(iterationStart).Round(time.Millisecond))
			} else {
				fmt.Println("All LEDs OFF sent.")
			}
		}
		if event == "ITERATION" {
			fmt.Printf("[%04d] %s  elapsed=%s  %s\n\n", seq, status, end.Sub(begin).Round(time.Millisecond), detail)
		}
		row := []string{"t0", start.Format(time.RFC3339Nano), strconv.FormatUint(*count, 10), event, strconv.FormatUint(seq, 10), begin.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano), strconv.FormatInt(end.Sub(begin).Nanoseconds(), 10), status, command, detail, d.port, d.vid, d.pid, d.serial, d.product, d.sysPath}
		e := w.Write(row)
		w.Flush()
		if e == nil {
			e = w.Error()
		}
		if e == nil {
			e = f.Sync()
		}
		if e != nil {
			if logErr == nil {
				errorCount++
			}
			logErr = e
			fmt.Fprintln(os.Stderr, "CSV:", e)
		}
	}
	if err = w.Write([]string{"test", "run_start", "requested", "event", "iteration", "start", "end", "elapsed_ns", "outcome", "command", "detail", "port", "vid", "pid", "serial", "product", "usb_sysfs"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	totals := map[string]uint64{}
	var attempted uint64
	defer func() {
		denom := totals["SENT"] + totals["FAIL"] + totals["TIMEOUT"]
		rate := 0.0
		if denom > 0 {
			rate = 100 * float64(totals["FAIL"]+totals["TIMEOUT"]) / float64(denom)
		}
		detail := fmt.Sprintf("requested=%d attempted=%d completed=%d SENT=%d FAIL=%d TIMEOUT=%d ABORTED=%d send_failure_rate=%.2f%% denominator=SENT+FAIL+TIMEOUT=%d interrupted=%t; LED result=VISUAL_UNCONFIRMED", *count, attempted, totals["SENT"], totals["SENT"], totals["FAIL"], totals["TIMEOUT"], totals["ABORTED"], rate, denom, ctx.Err() != nil)
		status := "DONE"
		if exit != 0 {
			status = "ERROR"
		}
		if ctx.Err() != nil {
			status = "ABORTED"
		}
		log("RUN_END", 0, start, status, "", detail)
		fmt.Printf("\nTest Result: %s\nStart Time: %s\nEnd Time: %s\nTotal Duration: %s\nRequested Iterations: %d (0 = unlimited)\nAttempted Iterations: %d\nCompleted Iterations: %d\nSENT: %d  FAIL: %d  TIMEOUT: %d  ABORTED: %d\nErrors: %d\nSend Failure Rate: %.2f%% (denominator: SENT + FAIL + TIMEOUT = %d)\nInterrupted: %t\nLED Verification: VISUAL_UNCONFIRMED\nCSV File: %s\n", status, start.Format(time.RFC3339), time.Now().Format(time.RFC3339), time.Since(start).Round(time.Millisecond), *count, attempted, totals["SENT"], totals["SENT"], totals["FAIL"], totals["TIMEOUT"], totals["ABORTED"], errorCount, rate, denom, ctx.Err() != nil, path)
		if logErr != nil && exit == 0 {
			exit = 2
		}
	}()
	log("RUN_START", 0, start, "START", "", "LED result requires visual inspection")
	if logErr != nil {
		return 2
	}
	err = setupErr
	fd := -1
	if err == nil {
		fmt.Printf("Selected USB CDC Port: %s\n\n", d.port)
		fd, err = openSerial(d.port)
	}
	if err != nil {
		log("SETUP", 0, time.Now(), "FAIL", "", err.Error())
		return 1
	}
	// Use a fresh context so Ctrl+C cannot cancel cleanup before it is attempted.
	transmit := func(parent context.Context, seq uint64, wire string) error {
		begin := time.Now()
		bounded, cancel := context.WithTimeout(parent, 2*time.Second)
		defer cancel()
		e := send(bounded, fd, wire)
		detail := "host write only; LED unconfirmed"
		if e != nil {
			detail = e.Error()
		}
		log("COMMAND", seq, begin, outcome(e), wire, detail)
		return e
	}
	defer func() {
		// A leading LF terminates any partial command left by a failed write.
		if e := transmit(context.Background(), 0, "\nALL OFF\n"); e != nil && exit == 0 {
			exit = 1
		}
		if e := syscall.Close(fd); e != nil {
			errorCount++
			fmt.Fprintln(os.Stderr, "Close error:", e)
			if exit == 0 {
				exit = 1
			}
		}
	}()
	if err = transmit(ctx, 0, "\nALL OFF\n"); err != nil {
		if ctx.Err() != nil {
			return 130
		}
		return 1
	}
	if logErr != nil {
		return 2
	}
	for seq := uint64(1); *count == 0 || seq <= *count; seq++ {
		if ctx.Err() != nil {
			return 130
		}
		begin := time.Now()
		iterationStart = begin
		attempted++
		var cycleErr error
		for _, pin := range [...]int{14, 15, 26, 27} {
			if cycleErr = transmit(ctx, seq, fmt.Sprintf("LED %d ON\n", pin)); cycleErr != nil {
				break
			}
			if logErr != nil {
				cycleErr = logErr
				break
			}
			if !pause(ctx, *hold) {
				cycleErr = ctx.Err()
				break
			}
			if cycleErr = transmit(ctx, seq, fmt.Sprintf("LED %d OFF\n", pin)); cycleErr != nil {
				break
			}
			if logErr != nil {
				cycleErr = logErr
				break
			}
		}
		status := outcome(cycleErr)
		totals[status]++
		detail := fmt.Sprintf("completed=%d SENT=%d FAIL=%d TIMEOUT=%d ABORTED=%d", totals["SENT"], totals["SENT"], totals["FAIL"], totals["TIMEOUT"], totals["ABORTED"])
		if cycleErr != nil {
			detail += " error=" + cycleErr.Error()
		}
		log("ITERATION", seq, begin, status, "", detail)
		if logErr != nil {
			return 2
		}
		if cycleErr != nil {
			if ctx.Err() != nil {
				return 130
			}
			return 1
		}
		if *count != 0 && seq == *count {
			break
		}
	}
	return 0
}
