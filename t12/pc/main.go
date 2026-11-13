package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"
)

type portIO interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
}

func transfer(ctx context.Context, h portIO, b []byte, write bool, deadline time.Time) (int, error) {
	total := 0
	for len(b) > 0 {
		if e := ctx.Err(); e != nil {
			return total, e
		}
		if !time.Now().Before(deadline) {
			return total, context.DeadlineExceeded
		}
		chunk := b
		if len(chunk) > 4096 {
			chunk = chunk[:4096]
		}
		var n int
		var e error
		if write {
			n, e = h.Write(chunk)
		} else {
			n, e = h.Read(chunk)
		}
		total += int(n)
		b = b[n:]
		if e != nil {
			return total, e
		}
	}
	return total, nil
}

// Resolve relative output paths from the repository, independently of cwd.
// go run . retains the absolute source filename in the executable.
func resultsDirectory(path string) (string, error) {
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok || !filepath.IsAbs(source) {
		return "", errors.New("cannot locate T12 source directory; use an absolute -out path (build without -trimpath)")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", path)), nil
}
func seal(b []byte) { binary.LittleEndian.PutUint32(b[len(b)-4:], crc32.ChecksumIEEE(b[:len(b)-4])) }
func valid(b []byte, magic string, id uint32) bool {
	if len(b) < 12 {
		return false
	}
	return string(b[:4]) == magic && binary.LittleEndian.Uint32(b[4:8]) == id && binary.LittleEndian.Uint32(b[len(b)-4:]) == crc32.ChecksumIEEE(b[:len(b)-4])
}

type deviceResult struct {
	received, firstReadUS, actualPauseUS uint32
	hash                                 [32]byte
}

func parseResult(b []byte, id, observeMS uint32) (deviceResult, error) {
	var result deviceResult
	if len(b) != 60 || !valid(b, "T2D!", id) {
		return result, errors.New("invalid v2 result")
	}
	u := binary.LittleEndian.Uint32
	if u(b[20:24]) != observeMS {
		return result, errors.New("invalid observation time")
	}
	result.received, result.firstReadUS, result.actualPauseUS = u(b[8:12]), u(b[12:16]), u(b[16:20])
	copy(result.hash[:], b[24:56])
	return result, nil
}

func (r deviceResult) matches(size int, hash [32]byte) bool {
	return r.received == uint32(size) && r.hash == hash
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() (runErr error) {
	port := flag.String("port", "", "required serial port (COM11 or /dev/ttyACM0)")
	size := flag.Int("size", 4096, "payload bytes (1..16777216)")
	pause := flag.Duration("pause", time.Second, "RX read pause (0..60s, whole ms)")
	observe := flag.Duration("observe", 3*time.Second, "RX observation after pause (1ms..60s, whole ms)")
	count := flag.Uint64("count", 1, "iterations; 0 unlimited")
	timeout := flag.Duration("timeout", 5*time.Second, "READY/result grace deadline (100ms..60s)")
	out := flag.String("out", "t12/results", "new CSV directory")
	flag.Parse()
	if flag.NArg() != 0 || !validPort(*port) || *size < 1 || *size > 16*1024*1024 || *pause < 0 || *pause > 60*time.Second || *pause%time.Millisecond != 0 || *observe < time.Millisecond || *observe > 60*time.Second || *observe%time.Millisecond != 0 || *timeout < 100*time.Millisecond || *timeout > 60*time.Second || *out == "" {
		return errors.New("invalid arguments; see -help")
	}
	outputDir, err := resultsDirectory(*out)
	if err != nil {
		return err
	}
	data := make([]byte, *size)
	state := uint32(0x12345678)
	for i := range data {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		data[i] = byte(state)
	}
	want := sha256.Sum256(data)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if e := os.MkdirAll(outputDir, 0755); e != nil {
		return e
	}
	f, e := os.CreateTemp(outputDir, "t12-v2-"+time.Now().Format("20060102-150405")+"-*.csv")
	if e != nil {
		return e
	}
	defer f.Close()
	w := csv.NewWriter(f)
	emit := func(row ...string) error {
		if e := w.Write(row); e != nil {
			return e
		}
		w.Flush()
		if e := w.Error(); e != nil {
			return e
		}
		return f.Sync()
	}
	start := time.Now()
	totals := map[string]int{"PASS": 0, "FAIL": 0, "TIMEOUT": 0, "ABORTED": 0, "ERROR": 0}
	completed := uint64(0)
	printStart(os.Stdout, *port, *size, *pause, *observe, *timeout, *count, start)
	if e = emit("record", "test", "iteration", "start", "end", "elapsed_ns", "outcome", "port", "size", "pause_ms", "observe_ms", "host_written", "ready_to_write_ns", "write_elapsed_ns", "received", "sent_sha256", "received_sha256", "first_read_us", "actual_pause_us", "detail", "integrity"); e != nil {
		return e
	}
	defer func() {
		end := time.Now()
		denom := totals["PASS"] + totals["FAIL"] + totals["TIMEOUT"] + totals["ERROR"]
		rate := 0.0
		if denom > 0 {
			rate = 100 * float64(totals["FAIL"]+totals["TIMEOUT"]+totals["ERROR"]) / float64(denom)
		}
		summary := fmt.Sprintf("requested=%d completed=%d PASS=%d FAIL=%d TIMEOUT=%d ABORTED=%d ERROR=%d failure_rate=%.2f%% denominator=PASS+FAIL+TIMEOUT+ERROR=%d", *count, completed, totals["PASS"], totals["FAIL"], totals["TIMEOUT"], totals["ABORTED"], totals["ERROR"], rate, denom)
		printFinal(os.Stdout, totals, completed, *count, start, end, f.Name(), runErr)
		if err := emit("summary", "t12", "", start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano), strconv.FormatInt(end.Sub(start).Nanoseconds(), 10), "", "", "", "", "", "", "", "", "", "", "", "", "", summary, ""); err != nil {
			fmt.Fprintln(os.Stderr, "summary save:", err)
		}
	}()
	h, e := openPort(*port)
	if e != nil {
		totals["ERROR"]++
		_ = emit("error", "t12", "", start.Format(time.RFC3339Nano), time.Now().Format(time.RFC3339Nano), "", "ERROR", *port, "", "", "", "", "", "", "", "", "", "", "", e.Error(), "UNKNOWN")
		return e
	}
	defer h.Close()
	for i := uint64(1); *count == 0 || i <= *count; i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if i > uint64(^uint32(0)) {
			return errors.New("sequence exhausted")
		}
		began := time.Now()
		outcome, detail := "PASS", ""
		written := 0
		delay, writeTime := time.Duration(0), time.Duration(0)
		received, first, actual := "", "", ""
		integrity := "UNKNOWN"
		gotHash := ""
		trial := func() error {
			var cmd [24]byte
			copy(cmd[:], "T2C!")
			for j, v := range [...]uint32{uint32(i), uint32(*size), uint32(*pause / time.Millisecond), uint32(*observe / time.Millisecond)} {
				binary.LittleEndian.PutUint32(cmd[4+j*4:], v)
			}
			seal(cmd[:])
			if _, e := transfer(ctx, h, cmd[:], true, time.Now().Add(*timeout)); e != nil {
				return e
			}
			var ready [12]byte
			if _, e := transfer(ctx, h, ready[:], false, time.Now().Add(*timeout)); e != nil {
				return e
			}
			readyAt := time.Now()
			if !valid(ready[:], "T2R!", uint32(i)) {
				return errors.New("invalid READY")
			}
			writeStart := time.Now()
			delay = writeStart.Sub(readyAt)
			var err error
			// Finish sending within the observation budget. A late/partial write is
			// insufficient evidence; stop without sending any more control bytes.
			written, err = transfer(ctx, h, data, true, readyAt.Add(*pause+*observe))
			writeTime = time.Since(writeStart)
			if err != nil {
				return fmt.Errorf("payload write: %w", err)
			}
			var result [60]byte
			if _, e := transfer(ctx, h, result[:], false, readyAt.Add(*pause+*observe+*timeout)); e != nil {
				return e
			}
			measured, err := parseResult(result[:], uint32(i), uint32(*observe/time.Millisecond))
			if err != nil {
				return err
			}
			received = strconv.FormatUint(uint64(measured.received), 10)
			first = strconv.FormatUint(uint64(measured.firstReadUS), 10)
			actual = strconv.FormatUint(uint64(measured.actualPauseUS), 10)
			gotHash = fmt.Sprintf("%x", measured.hash)
			integrity = "FAIL"
			if !measured.matches(*size, want) {
				return errors.New("length/SHA-256 mismatch within observation window; cause not established")
			}
			integrity = "PASS"
			var ack [12]byte
			copy(ack[:], "T2A!")
			binary.LittleEndian.PutUint32(ack[4:], uint32(i))
			seal(ack[:])
			_, err = transfer(ctx, h, ack[:], true, time.Now().Add(*timeout))
			return err
		}
		err := trial()
		if err != nil {
			outcome = "ERROR"
			if integrity == "FAIL" {
				outcome = "FAIL"
			}
			detail = err.Error()
			if errors.Is(err, context.DeadlineExceeded) {
				outcome = "TIMEOUT"
			}
			if ctx.Err() != nil {
				outcome = "ABORTED"
			}
		}
		end := time.Now()
		totals[outcome]++
		completed++
		if e = emit("iteration", "t12", strconv.FormatUint(i, 10), began.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano), strconv.FormatInt(end.Sub(began).Nanoseconds(), 10), outcome, *port, strconv.Itoa(*size), strconv.FormatInt(int64(*pause/time.Millisecond), 10), strconv.FormatInt(int64(*observe/time.Millisecond), 10), strconv.Itoa(written), fmt.Sprint(delay.Nanoseconds()), fmt.Sprint(writeTime.Nanoseconds()), received, fmt.Sprintf("%x", want), gotHash, first, actual, detail, integrity); e != nil {
			return e
		}
		printTrial(os.Stdout, i, *count, outcome, end.Sub(began), written, received, fmt.Sprintf("%x", want), gotHash, detail)
		if err != nil {
			return err
		}
	}
	return nil
}

func countLabel(count uint64) string {
	if count == 0 {
		return "∞"
	}
	return strconv.FormatUint(count, 10)
}

func printStart(w io.Writer, port string, size int, pause, observe, timeout time.Duration, count uint64, start time.Time) {
	fmt.Fprintln(w, "==================================================")
	fmt.Fprintln(w, "T12 - USB CDC RX Data Integrity")
	fmt.Fprintln(w, "==================================================")
	fmt.Fprintln(w, "Purpose : Compare received length and SHA-256 with sent data")
	fmt.Fprintf(w, "Host    : %s/%s / %s\nGo      : %s\n", runtime.GOOS, runtime.GOARCH, port, runtime.Version())
	fmt.Fprintf(w, "Size    : %d B\nPause   : %d ms\nObserve : %s\nTimeout : %s\nCount   : %s\nStart   : %s\n", size, pause/time.Millisecond, observe, timeout, countLabel(count), start.Format(time.RFC3339Nano))
	fmt.Fprintln(w, "--------------------------------------------------")
}

func printTrial(w io.Writer, iteration, count uint64, outcome string, elapsed time.Duration, sent int, received, sentHash, receivedHash, detail string) {
	fmt.Fprintf(w, "[%d/%s] %-7s %.3fs\n", iteration, countLabel(count), outcome, elapsed.Seconds())
	if outcome == "FAIL" {
		hashStatus := "UNKNOWN"
		if receivedHash != "" {
			hashStatus = "MISMATCH"
			if sentHash == receivedHash {
				hashStatus = "MATCH"
			}
		}
		fmt.Fprintf(w, "       Sent     : %d B\n       Received : %s B\n       SHA-256  : %s\n", sent, received, hashStatus)
	}
	if outcome != "PASS" && detail != "" {
		fmt.Fprintf(w, "       Detail   : %s\n", detail)
	}
}

func printFinal(w io.Writer, totals map[string]int, completed, requested uint64, start, end time.Time, csvPath string, runErr error) {
	result := "PASS"
	for _, outcome := range []string{"FAIL", "TIMEOUT", "ERROR", "ABORTED"} {
		if totals[outcome] > 0 {
			result = outcome
			break
		}
	}
	if result == "PASS" && runErr != nil {
		result = "ERROR"
		if errors.Is(runErr, context.Canceled) {
			result = "ABORTED"
		}
	}
	fmt.Fprintln(w, "==================================================")
	fmt.Fprintf(w, "RESULT: %s\n", result)
	fmt.Fprintln(w, "==================================================")
	fmt.Fprintf(w, "PASS=%d  FAIL=%d  TIMEOUT=%d  ERROR=%d  ABORTED=%d\n", totals["PASS"], totals["FAIL"], totals["TIMEOUT"], totals["ERROR"], totals["ABORTED"])
	fmt.Fprintf(w, "Completed: %d/%s\nDuration : %.3fs\nStart    : %s\nEnd      : %s\nCSV      : %s\n", completed, countLabel(requested), end.Sub(start).Seconds(), start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano), filepath.Clean(csvPath))
	denom := totals["PASS"] + totals["FAIL"] + totals["TIMEOUT"] + totals["ERROR"]
	rate := 0.0
	if denom > 0 {
		rate = 100 * float64(totals["FAIL"]+totals["TIMEOUT"]+totals["ERROR"]) / float64(denom)
	}
	fmt.Fprintf(w, "Failure rate: %.2f%% (FAIL+TIMEOUT+ERROR)/(PASS+FAIL+TIMEOUT+ERROR), n=%d\n", rate, denom)
	if runErr != nil {
		fmt.Fprintf(w, "Detail   : %s\n", runErr)
	}
}
