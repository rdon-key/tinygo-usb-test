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
	"regexp"
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
	o.comm = *timeout
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
		iterationCtx, cancel := context.WithTimeout(ctx, *timeout+extraBudget(o))
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
				if status == "SKIPPED" {
					exit = 77
				}
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
	testID       = "t11"
	testName     = "USB Power Cycle Test"
	purpose      = "Verify CDC recovery after explicitly verified exclusive physical VBUS switching."
	procedure    = "Verify power configuration and target, PING/PONG, power OFF, observe USB removal, power ON, observe re-enumeration, reopen and PING/PONG."
	verification = "PASS requires an approved physical-switching receipt, observed disappearance/new address and exact PING/PONG recovery; unsupported setups are SKIPPED."
)

type options struct {
	PowerConfig    string
	Off, Reconnect time.Duration
	comm           time.Duration
}

func configure(o *options) {
	flag.StringVar(&o.PowerConfig, "power-config", "", "explicitly verified hub/port JSON receipt; absent=SKIPPED")
	flag.DurationVar(&o.Off, "off", time.Second, "physical VBUS off duration (250ms..10s)")
	flag.DurationVar(&o.Reconnect, "reconnect", 15*time.Second, "USB disappearance/re-enumeration deadline (max 1m)")
}
func validate(o options) error {
	if o.Off < 250*time.Millisecond || o.Off > 10*time.Second || o.Reconnect <= 0 || o.Reconnect > time.Minute {
		return errors.New("invalid off/reconnect duration")
	}
	return nil
}
func extraBudget(o options) time.Duration { return 2*o.Reconnect + o.Off + 4*o.comm + 9*time.Second }

type powerConfig struct {
	Hub                  string `json:"hub"`
	Port                 int    `json:"port"`
	HubVID               string `json:"hub_vid"`
	HubPID               string `json:"hub_pid"`
	USBConnection        string `json:"usb_connection"`
	Serial               string `json:"serial"`
	PhysicalVBUSVerified bool   `json:"physical_vbus_verified"`
	ExclusiveVerified    bool   `json:"exclusive_verified"`
	ExactHubOnlyVerified bool   `json:"exact_hub_only_verified"`
	VerifiedAt           string `json:"verified_at"`
	Evidence             string `json:"evidence"`
}

func loadPower(path string) (powerConfig, string, error) {
	var c powerConfig
	if path == "" {
		return c, "", skipped("No power-config: an explicitly verified exclusive hub port is required")
	}
	info, e := os.Stat(path)
	if e != nil {
		return c, "", skipped("Power configuration unavailable: " + e.Error())
	}
	if !info.Mode().IsRegular() || info.Size() > 8192 {
		return c, "", skipped("Power configuration must be a regular JSON file of at most 8192 bytes")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return c, "", skipped(e.Error())
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if e = decoder.Decode(&c); e != nil {
		return c, "", skipped("Invalid power configuration: " + e.Error())
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return c, "", skipped("Trailing power configuration data")
	}
	if !c.PhysicalVBUSVerified || !c.ExclusiveVerified || !c.ExactHubOnlyVerified {
		return c, "", skipped("Physical VBUS, exclusive scope and exact-hub-only switching have not all been verified")
	}
	if !regexp.MustCompile(`^[0-9]+(-[0-9]+(\.[0-9]+)*)?$`).MatchString(c.Hub) || c.Port < 1 || c.Port > 31 {
		return c, "", skipped("Invalid explicit hub location or single port (1..31)")
	}
	if !regexp.MustCompile(`^[0-9a-f]{4}$`).MatchString(c.HubVID) || !regexp.MustCompile(`^[0-9a-f]{4}$`).MatchString(c.HubPID) {
		return c, "", skipped("Expected hub VID/PID must be four lowercase hexadecimal digits")
	}
	at, e := time.Parse(time.RFC3339, c.VerifiedAt)
	if e != nil || at.After(time.Now()) || len(c.Evidence) == 0 || len(c.Evidence) > 2048 {
		return c, "", skipped("A non-future verification timestamp and 1..2048 byte measurement/scope evidence are required")
	}
	if c.USBConnection != childConnection(c.Hub, c.Port) {
		return c, "", skipped("Configured Pico connection does not match the explicit hub/port")
	}
	return c, fmt.Sprintf("%x", sha256.Sum256(b)), nil
}
func childConnection(hub string, port int) string {
	if strings.Contains(hub, "-") {
		return hub + "." + strconv.Itoa(port)
	}
	return hub + "-" + strconv.Itoa(port)
}

var powerAttribute = attr
var powerTool = exec.LookPath
var hubCommand = func(ctx context.Context, args ...string) (string, error) {
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	b, e := exec.CommandContext(bounded, "uhubctl", args...).CombinedOutput()
	if bounded.Err() != nil {
		return string(b), bounded.Err()
	}
	return string(b), e
}

func hubIdentity(c powerConfig) error {
	base := "/sys/bus/usb/devices/" + c.Hub
	vid, pid := powerAttribute(base, "idVendor"), powerAttribute(base, "idProduct")
	if vid != c.HubVID || pid != c.HubPID || powerAttribute(base, "bDeviceClass") != "09" {
		return skipped("Current hub identity/class does not match the verification receipt")
	}
	if vid == "1d6b" || (vid == "0424" && (pid == "9512" || pid == "9514" || pid == "2514")) {
		return skipped("Root/shared Raspberry Pi hub is not allowed; use a verified exclusive external hub")
	}
	return nil
}
func powerQuery(ctx context.Context, c powerConfig) (string, error) {
	if _, e := powerTool("uhubctl"); e != nil {
		return "", skipped("uhubctl is unavailable: " + e.Error())
	}
	if e := hubIdentity(c); e != nil {
		return "", e
	}
	q, e := hubCommand(ctx, "-e", "-l", c.Hub, "-p", strconv.Itoa(c.Port))
	if e != nil {
		if ctx.Err() != nil {
			return q, ctx.Err()
		}
		return q, skipped("Cannot verify uhubctl capability/permissions: " + e.Error())
	}
	header := regexp.MustCompile(`(?m)^Current status for hub ` + regexp.QuoteMeta(c.Hub) + `(?:\s|$)`)
	portLine := regexp.MustCompile(`(?m)^\s*Port\s+` + strconv.Itoa(c.Port) + `:\s`)
	if !header.MatchString(q) || !regexp.MustCompile(`\bppps\b`).MatchString(q) || !portLine.MatchString(q) {
		return q, skipped("uhubctl did not confirm exact hub, individual port power switching (ppps) and the selected port")
	}
	return q, nil
}
func preflight(ctx context.Context, o options) error {
	c, _, e := loadPower(o.PowerConfig)
	if e != nil {
		return e
	}
	_, e = powerQuery(ctx, c)
	return e
}
func verifyPowerTarget(c powerConfig, d device) error {
	if d.vid != "2e8a" || d.pid != "000a" || filepath.Base(d.sysPath) != c.USBConnection || d.serial != c.Serial {
		return skipped(fmt.Sprintf("Pico identity/topology does not match the exclusive power receipt: actual=%+v", d))
	}
	return nil
}
func setPower(ctx context.Context, c powerConfig, action string) (string, error) {
	if e := hubIdentity(c); e != nil {
		return "", e
	}
	return hubCommand(ctx, "-e", "-l", c.Hub, "-p", strconv.Itoa(c.Port), "-a", action)
}
func iteration(ctx context.Context, l *link, round uint32, o options) (err error) {
	c, hash, e := loadPower(o.PowerConfig)
	if e != nil {
		return e
	}
	if e = verifyPowerTarget(c, l.d); e != nil {
		return e
	}
	q, e := powerQuery(ctx, c)
	if e != nil {
		return e
	}
	receipt, _ := json.Marshal(c)
	l.note("power_receipt_sha256=" + hash + " receipt=" + string(receipt))
	l.note("hub verification: " + strings.TrimSpace(q))
	if v, e := hubCommand(ctx, "-v"); e == nil {
		l.note("uhubctl version: " + strings.TrimSpace(v))
	}
	comm, cancel := context.WithTimeout(ctx, o.comm)
	e = ping(comm, l, round*65536, 32)
	cancel()
	if e != nil {
		return e
	}
	before := l.d
	l.close()
	restore := true
	defer func() {
		if restore {
			fresh, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			response, e := setPower(fresh, c, "on")
			l.note(fmt.Sprintf("bounded safety power-ON attempt result=%q error=%v", response, e))
			if e != nil && err == nil {
				err = fmt.Errorf("power restoration failed: %w", e)
			}
		}
	}()
	l.note("physical VBUS OFF requested at " + time.Now().Format(time.RFC3339Nano))
	response, e := setPower(ctx, c, "off")
	l.note(fmt.Sprintf("uhubctl OFF result=%q error=%v; OFF report alone does not prove VBUS removal", response, e))
	if e != nil {
		return e
	}
	gone, goneCancel := context.WithTimeout(ctx, o.Reconnect)
	e = waitGone(gone, before)
	goneCancel()
	if e != nil {
		return e
	}
	l.last = "physical-switch operation followed by observed USB disappearance"
	l.note("expected physical disconnect observed at " + time.Now().Format(time.RFC3339Nano))
	if !pause(ctx, o.Off) {
		return ctx.Err()
	}
	response, e = setPower(ctx, c, "on")
	l.note(fmt.Sprintf("physical VBUS ON requested at %s result=%q error=%v", time.Now().Format(time.RFC3339Nano), response, e))
	if e != nil {
		return e
	}
	restore = false
	reconnect, reconnectCancel := context.WithTimeout(ctx, o.Reconnect)
	d, e := waitReturn(reconnect, before)
	reconnectCancel()
	if e != nil {
		return e
	}
	l.d = d
	l.note(fmt.Sprintf("USB returned at %s: %+v", time.Now().Format(time.RFC3339Nano), d))
	l.fd, e = openReconnected(d.port)
	if e != nil {
		return e
	}
	comm, cancel = context.WithTimeout(ctx, o.comm)
	defer cancel()
	if e = ping(comm, l, round*65536+1, 32); e != nil {
		return fmt.Errorf("post-power-cycle PING/PONG: %w", e)
	}
	l.note("physical VBUS exclusivity relies on the explicit measured receipt; per-cycle voltage is not instrumented")
	return nil
}

var openReconnected = openSerial

var usbPresent = func(d device) bool { _, e := os.Stat(d.sysPath); return e == nil }
var scanCDC = func() []device {
	paths, _ := filepath.Glob("/sys/class/tty/ttyACM*")
	out := make([]device, 0, len(paths))
	for _, p := range paths {
		d, e := identify("/dev/" + filepath.Base(p))
		if e == nil {
			out = append(out, d)
		}
	}
	return out
}

func returnedDevice(before device, all []device) (device, bool) {
	var found device
	count := 0
	for _, d := range all {
		if d.sysPath == before.sysPath && d.vid == before.vid && d.pid == before.pid && (before.serial == "" || d.serial == before.serial) && (d.bus != before.bus || d.address != before.address) {
			found = d
			count++
		}
	}
	return found, count == 1
}
func waitGone(ctx context.Context, before device) error {
	for {
		if e := ctx.Err(); e != nil {
			return fmt.Errorf("USB disappearance not observed: %w", e)
		}
		if !usbPresent(before) {
			return nil
		}
		if !pause(ctx, 10*time.Millisecond) {
			return fmt.Errorf("USB disappearance not observed: %w", ctx.Err())
		}
	}
}
func waitReturn(ctx context.Context, before device) (device, error) {
	var stable device
	var since time.Time
	for {
		if e := ctx.Err(); e != nil {
			return device{}, fmt.Errorf("USB re-enumeration/CDC port deadline: %w", e)
		}
		d, ok := returnedDevice(before, scanCDC())
		if ok {
			if d != stable {
				stable = d
				since = time.Now()
			}
			if time.Since(since) >= 100*time.Millisecond {
				return d, nil
			}
		} else {
			stable = device{}
			since = time.Time{}
		}
		if !pause(ctx, 10*time.Millisecond) {
			return device{}, ctx.Err()
		}
	}
}
