// Linux host. Independent test; no one-read/one-frame assumption.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
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
		return f, fmt.Errorf("CRC32 mismatch expected=%08x actual=%08x", checksum(b[:n+10]), binary.LittleEndian.Uint32(b[n+10:]))
	}
	f.Kind = b[2]
	f.Source = b[3]
	f.Seq = binary.LittleEndian.Uint32(b[4:8])
	f.N = uint16(n)
	copy(f.Data[:], b[10:10+n])
	return f, nil
}

type device struct{ port, vid, pid, serial, product, sysPath, bus, address string }

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
			return device{real, v, attr(p, "idProduct"), attr(p, "serial"), attr(p, "product"), p, attr(p, "busnum"), attr(p, "devnum")}, nil
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

type link struct {
	fd    int
	d     device
	last  string
	notes string
}

func (l *link) close() {
	if l.fd >= 0 {
		syscall.Close(l.fd)
		l.fd = -1
	}
}
func (l *link) note(s string) {
	if len(l.notes) < 8192 {
		l.notes += s + "; "
	}
}
func openDevice(port string) (*link, error) {
	d, e := discover(port)
	if e != nil {
		return nil, e
	}
	if d.vid != "2e8a" || d.pid != "000a" {
		return nil, fmt.Errorf("unexpected USB identity: %+v", d)
	}
	fd, e := openSerial(d.port)
	if e != nil {
		return nil, e
	}
	return &link{fd: fd, d: d, last: "port opened"}, nil
}
func transfer(ctx context.Context, fd int, b []byte, writing bool) error {
	used := 0
	for used < len(b) {
		if e := ctx.Err(); e != nil {
			return e
		}
		var n int
		var e error
		if writing {
			n, e = syscall.Write(fd, b[used:])
		} else {
			n, e = syscall.Read(fd, b[used:])
		}
		if n > 0 {
			used += n
		}
		if e != nil && e != syscall.EAGAIN && e != syscall.EINTR {
			return fmt.Errorf("transfer write=%t bytes=%d/%d: %w", writing, used, len(b), e)
		}
		if used < len(b) && !pause(ctx, time.Millisecond) {
			return fmt.Errorf("transfer write=%t bytes=%d/%d last=%x: %w", writing, used, len(b), b[:min(used, 64)], ctx.Err())
		}
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	return nil
}
func (l *link) send(ctx context.Context, f frame) error { return transfer(ctx, l.fd, encode(f), true) }
func (l *link) receive(ctx context.Context) (frame, error) {
	var b [526]byte
	var empty frame
	if e := transfer(ctx, l.fd, b[:10], false); e != nil {
		return empty, e
	}
	n := int(binary.LittleEndian.Uint16(b[8:10]))
	if n > 512 {
		return empty, fmt.Errorf("length=%d exceeds 512 header=%x", n, b[:10])
	}
	if e := transfer(ctx, l.fd, b[10:14+n], false); e != nil {
		return empty, e
	}
	f, e := decode(b[:14+n])
	if e != nil {
		return empty, fmt.Errorf("%w actual=%x", e, b[:min(14+n, 64)])
	}
	return f, nil
}
func checkFrame(f frame, kind, src byte, seq uint32, n int) error {
	if f.Kind != kind || f.Source != src || f.Seq != seq || int(f.N) != n {
		return fmt.Errorf("unexpected/missing/duplicate/reordered frame: expected kind=%c source=%d seq=%d length=%d; actual kind=%c source=%d seq=%d length=%d", kind, src, seq, n, f.Kind, f.Source, f.Seq, f.N)
	}
	want := f
	pattern(&want)
	if !bytes.Equal(f.Data[:f.N], want.Data[:want.N]) {
		for i := 0; i < n; i++ {
			if f.Data[i] != want.Data[i] {
				return fmt.Errorf("payload mismatch offset=%d expected=%02x actual=%02x", i, want.Data[i], f.Data[i])
			}
		}
	}
	return nil
}
func ping(ctx context.Context, l *link, seq uint32, n int) error {
	f := frame{Kind: 'P', Seq: seq, N: uint16(n)}
	pattern(&f)
	if e := l.send(ctx, f); e != nil {
		return e
	}
	r, e := l.receive(ctx)
	if e != nil {
		return e
	}
	if r.Kind == 'E' {
		r.Kind = 'R'
		if e = checkFrame(r, 'R', 0, seq, n); e != nil {
			return fmt.Errorf("Pico RX rejection: %w", e)
		}
		return errors.New("Pico reported a protocol error")
	}
	if e = checkFrame(r, 'R', 0, seq, n); e != nil {
		return e
	}
	l.last = fmt.Sprintf("PING/PONG seq=%d length=%d payload_crc=%08x", seq, n, checksum(r.Data[:r.N]))
	return nil
}
func classify(e error) string {
	if e == nil {
		return "PASS"
	}
	if errors.Is(e, context.Canceled) {
		return "ABORTED"
	}
	if errors.Is(e, context.DeadlineExceeded) {
		return "TIMEOUT"
	}
	var skip skipped
	if errors.As(e, &skip) {
		return "SKIPPED"
	}
	return "FAIL"
}

type skipped string

func (s skipped) Error() string { return string(s) }

var output io.Writer = os.Stdout

func say(format string, args ...any) { fmt.Fprintf(output, format, args...) }
func main()                          { os.Exit(run()) }
func run() int {
	count := flag.Uint64("count", 100, "iterations; 0=unlimited")
	port := flag.String("port", "", "verified USB CDC port; empty=auto")
	timeout := flag.Duration("timeout", 5*time.Second, "communication/iteration deadline")
	interval := flag.Duration("interval", 100*time.Millisecond, "interval between iterations")
	csvPath := flag.String("csv", "", "new CSV path")
	o := options{}
	configure(&o)
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "Unexpected positional arguments")
		return 2
	}
	if *timeout <= 0 || *interval < 0 {
		fmt.Fprintln(os.Stderr, "Invalid timeout/interval")
		return 2
	}
	if e := validate(o); e != nil {
		fmt.Fprintln(os.Stderr, e)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	start := time.Now()
	metaPath := os.Getenv("CDCTEST_METADATA")
	own := metaPath == ""
	meta := map[string]any{}
	if own {
		if e := os.MkdirAll(testID+"/results", 0755); e != nil {
			fmt.Fprintln(os.Stderr, e)
			return 2
		}
		dir, e := os.MkdirTemp(testID+"/results", start.Format("20060102T150405-")+"*")
		if e != nil {
			fmt.Fprintln(os.Stderr, e)
			return 2
		}
		metaPath = filepath.Join(dir, "metadata.json")
		if *csvPath == "" {
			*csvPath = filepath.Join(dir, "results.csv")
		}
		log, e := os.OpenFile(filepath.Join(dir, "console.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if e != nil {
			fmt.Fprintln(os.Stderr, e)
			return 2
		}
		defer log.Close()
		output = io.MultiWriter(os.Stdout, log)
		defer func() { output = os.Stdout }()
		versionCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		v, e := exec.CommandContext(versionCtx, "tinygo", "version").CombinedOutput()
		cancel()
		if e != nil {
			v = []byte("unavailable: " + e.Error())
		}
		hashes := map[string]string{}
		for _, p := range []string{testID + "/pc/main.go", testID + "/pico/main.go"} {
			b, e := os.ReadFile(p)
			if e == nil {
				hashes[p] = fmt.Sprintf("%x", sha256.Sum256(b))
			} else {
				hashes[p] = "unavailable: " + e.Error()
			}
		}
		meta = map[string]any{"test": testID, "start": start.Format(time.RFC3339Nano), "go_version": runtime.Version(), "host": runtime.GOOS + "/" + runtime.GOARCH, "device_expected": "RP2040 Pico", "target_requested": "pico", "tinygo_and_llvm_version": strings.TrimSpace(string(v)), "target": "unknown", "scheduler": "unknown", "firmware_flash_status": "unknown", "parameters": map[string]any{"count": *count, "port": *port, "timeout": timeout.String(), "interval": interval.String(), "options": o}, "source_sha256": hashes, "csv": *csvPath}
		b, _ := json.Marshal(meta)
		if e = os.WriteFile(metaPath, b, 0644); e != nil {
			say("Metadata error: %v\n", e)
			return 2
		}
	}
	if *csvPath == "" {
		*csvPath = os.Getenv("CDCTEST_CSV")
	}
	if *csvPath == "" {
		say("CSV path is required with launcher metadata\n")
		return 2
	}
	metadata, e := os.ReadFile(metaPath)
	if e != nil {
		say("Metadata error: %v\n", e)
		return 2
	}
	scheduler := "unknown"
	var m struct {
		Firmware struct {
			Scheduler string `json:"scheduler"`
		} `json:"firmware"`
	}
	if json.Unmarshal(metadata, &m) == nil && m.Firmware.Scheduler != "" {
		scheduler = m.Firmware.Scheduler
	}
	requested := strconv.FormatUint(*count, 10)
	target := fmt.Sprintf("%04d", *count)
	if *count == 0 {
		requested = "Unlimited (count=0)"
		target = "unlimited"
	}
	say("Test ID and Name: %s: %s\nPurpose: %s\nTest Procedure: %s\nVerification Method: %s\nScheduler: %s (flash record only; no runtime readback)\nTest Parameters: count=%s timeout=%s interval=%s options=%+v\nStart Time: %s\nUSB CDC Port: %s (empty=automatic)\n", testID, testName, purpose, procedure, verification, scheduler, requested, *timeout, *interval, o, start.Format(time.RFC3339), *port)
	f, e := os.OpenFile(*csvPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if e != nil {
		say("CSV error: %v\n", e)
		return 2
	}
	defer f.Close()
	for _, line := range strings.Split(strings.TrimSpace(string(metadata)), "\n") {
		fmt.Fprintf(f, "# Run Environment: %s\n", line)
	}
	fmt.Fprintf(f, "# Test ID and Name: %s: %s\n# Purpose: %s\n# Test Procedure: %s\n# Verification Method: %s\n# Protocol: bounded binary frame, explicit length and CRC32 IEEE\n# Heartbeat: GP28 500ms ON / 500ms OFF, not proof of USB health\n# Parameters: count=%s timeout=%s interval=%s options=%+v\n# Start Time: %s\n", testID, testName, purpose, procedure, verification, requested, *timeout, *interval, o, start.Format(time.RFC3339Nano))
	w := csv.NewWriter(f)
	save := func(row []string) error {
		if e := w.Write(row); e != nil {
			return e
		}
		w.Flush()
		if e := w.Error(); e != nil {
			return e
		}
		return f.Sync()
	}
	if e = save([]string{"test", "sequence", "start", "end", "elapsed_ns", "outcome", "error", "last_success", "usb_before", "usb_after", "details"}); e != nil {
		say("CSV error: %v\n", e)
		return 2
	}
	totals := map[string]uint64{}
	var total uint64
	exit := 0
	l := &link{fd: -1, last: "none"}
	defer func() { l.close() }()
	for i := uint64(1); *count == 0 || i <= *count; i++ {
		if ctx.Err() != nil {
			break
		}
		begin := time.Now()
		l.notes = ""
		before := l.d
		iterationCtx, cancel := context.WithTimeout(ctx, *timeout)
		var err error
		if e := preflight(iterationCtx, o); e != nil {
			err = e
		} else {
			if l.fd < 0 {
				var opened *link
				opened, err = openDevice(*port)
				if err == nil {
					l = opened
					before = l.d
					if i == 1 {
						say("Selected USB CDC Port: %s\n", l.d.port)
					}
				}
			}
			if err == nil {
				err = iteration(iterationCtx, l, uint32(i), o)
			}
		}
		cancel()
		end := time.Now()
		if err != nil && l.d.sysPath != "" {
			_, presenceErr := os.Stat(l.d.sysPath)
			l.note(fmt.Sprintf("USB sysfs after failed iteration: %v", presenceErr))
		}
		status := classify(err)
		total++
		totals[status]++
		detail := ""
		if err != nil {
			detail = err.Error()
		}
		if e = save([]string{testID, strconv.FormatUint(i, 10), begin.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano), strconv.FormatInt(end.Sub(begin).Nanoseconds(), 10), status, detail, l.last, fmt.Sprintf("%+v", before), fmt.Sprintf("%+v", l.d), l.notes}); e != nil {
			say("CSV error: %v\n", e)
			exit = 2
		}
		observation := ""
		if detail != "" {
			observation = "  " + detail
		}
		say("[%04d/%s] %-8s %s%s\n", i, target, status, end.Sub(begin).Round(100*time.Microsecond), observation)
		if status != "PASS" || exit != 0 {
			if exit == 0 {
				exit = 1
			}
			break
		}
		if *count != 0 && i == *count {
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
	if totals["SKIPPED"] > 0 {
		result = "SKIPPED"
	}
	if ctx.Err() != nil {
		result = "INTERRUPTED"
		exit = 130
	}
	say("\nResult: %s\nStart Time: %s\nEnd Time: %s\nTotal Duration: %s\nRequested Iterations: %s\nCompleted Iterations: %d (recorded outcomes)\nPASS: %d FAIL: %d TIMEOUT: %d ABORTED: %d SKIPPED: %d\nFailure Rate: %.2f%% (denominator: PASS+FAIL+TIMEOUT=%d)\nCSV File: %s\n", result, start.Format(time.RFC3339), end.Format(time.RFC3339), end.Sub(start), requested, total, totals["PASS"], totals["FAIL"], totals["TIMEOUT"], totals["ABORTED"], totals["SKIPPED"], rate, denom, *csvPath)
	if own {
		meta["end"] = end.Format(time.RFC3339Nano)
		meta["elapsed_ns"] = end.Sub(start).Nanoseconds()
		meta["exit_code"] = exit
		meta["outcomes"] = totals
		meta["last_success"] = l.last
		b, _ := json.Marshal(meta)
		if e = os.WriteFile(metaPath, b, 0644); e != nil {
			say("Metadata error: %v\n", e)
			exit = 2
		}
	}
	return exit
}

const (
	testID       = "t4"
	testName     = "Full-Duplex Test"
	purpose      = "Detect corruption or starvation when both directions transmit independently."
	procedure    = "Arm a finite stream; send host frames concurrently with receiving Pico frames."
	verification = "Both independent sequence streams and Pico receive-count summary complete before deadline."
)

type options struct{ Frames int }

func configure(o *options) { flag.IntVar(&o.Frames, "frames", 32, "frames per direction (1..256)") }
func validate(o options) error {
	if o.Frames < 1 || o.Frames > 256 {
		return errors.New("frames must be 1..256")
	}
	return nil
}

func preflight(ctx context.Context, o options) error { return nil }

func iteration(ctx context.Context, l *link, round uint32, o options) error {
	c := frame{Kind: 'C', Seq: round, N: 2}
	binary.LittleEndian.PutUint16(c.Data[:2], uint16(o.Frames))
	if e := l.send(ctx, c); e != nil {
		return e
	}
	a, e := l.receive(ctx)
	if e != nil {
		return e
	}
	if a.Kind != 'A' || a.Source != 0 || a.Seq != round || a.N != 0 {
		return fmt.Errorf("invalid arm ACK kind=%c source=%d seq=%d length=%d", a.Kind, a.Source, a.Seq, a.N)
	}
	both, cancel := context.WithCancel(ctx)
	defer cancel()
	tx := make(chan error, 1)
	go func() {
		for j := 0; j < o.Frames; j++ {
			f := frame{Kind: 'P', Seq: round*65536 + uint32(j), N: 32}
			pattern(&f)
			if e := l.send(both, f); e != nil {
				tx <- e
				return
			}
		}
		tx <- nil
	}()
	for j := 0; j < o.Frames; j++ {
		f, e := l.receive(both)
		if e == nil {
			e = checkFrame(f, 'D', 1, round*65536+uint32(j), 32)
		}
		if e != nil {
			cancel()
			txErr := <-tx
			return fmt.Errorf("RX direction at frame=%d TX error=%v: %w", j, txErr, e)
		}
		l.last = fmt.Sprintf("Pico->host frame=%d/%d", j+1, o.Frames)
	}
	if e = <-tx; e != nil {
		return fmt.Errorf("host->Pico: %w", e)
	}
	summary, e := l.receive(ctx)
	if e != nil {
		return e
	}
	if summary.Kind != 'V' || summary.Source != 0 || summary.Seq != round || summary.N != 4 || binary.LittleEndian.Uint32(summary.Data[:4]) != uint32(o.Frames) {
		return fmt.Errorf("invalid RX verification summary: kind=%c seq=%d count=%d", summary.Kind, summary.Seq, binary.LittleEndian.Uint32(summary.Data[:4]))
	}
	l.last = fmt.Sprintf("both directions verified frames=%d", o.Frames)
	return nil
}
