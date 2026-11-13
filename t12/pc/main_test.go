package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type partialPort struct {
	input  []byte
	output []byte
	err    error
	calls  int
}

func (p *partialPort) Read(b []byte) (int, error) {
	p.calls++
	if len(p.input) == 0 {
		return 0, io.EOF
	}
	n := copy(b, p.input[:1])
	p.input = p.input[n:]
	return n, p.err
}

func (p *partialPort) Write(b []byte) (int, error) {
	p.calls++
	p.output = append(p.output, b[0])
	return 1, p.err
}

func TestTransferPartialIO(t *testing.T) {
	data := []byte("T12 partial transfer")
	p := &partialPort{input: append([]byte(nil), data...)}
	got := make([]byte, len(data))
	n, err := transfer(context.Background(), p, got, false, time.Now().Add(time.Second))
	if err != nil || n != len(data) || !bytes.Equal(got, data) {
		t.Fatalf("read: n=%d data=%q err=%v", n, got, err)
	}
	n, err = transfer(context.Background(), p, data, true, time.Now().Add(time.Second))
	if err != nil || n != len(data) || !bytes.Equal(p.output, data) {
		t.Fatalf("write: n=%d data=%q err=%v", n, p.output, err)
	}
}

func TestTransferStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name     string
		ctx      context.Context
		deadline time.Time
		want     error
	}{
		{"cancelled", ctx, time.Now().Add(time.Second), context.Canceled},
		{"expired", context.Background(), time.Now().Add(-time.Second), context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &partialPort{}
			n, err := transfer(tc.ctx, p, []byte{1}, true, tc.deadline)
			if n != 0 || !errors.Is(err, tc.want) || p.calls != 0 {
				t.Fatalf("n=%d err=%v calls=%d", n, err, p.calls)
			}
		})
	}
	p := &partialPort{err: io.ErrUnexpectedEOF}
	n, err := transfer(context.Background(), p, []byte{1, 2}, true, time.Now().Add(time.Second))
	if n != 1 || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("partial error: n=%d err=%v", n, err)
	}
}

func TestResultsDirectoryIndependentOfWorkingDirectory(t *testing.T) {
	want, err := resultsDirectory("t12/results")
	if err != nil {
		t.Fatal(err)
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	t.Cleanup(func() { _ = os.Chdir(previous) })
	if err := os.Chdir(other); err != nil {
		t.Fatal(err)
	}
	got, err := resultsDirectory("t12/results")
	if err != nil || got != want || !filepath.IsAbs(got) || filepath.Base(filepath.Dir(got)) != "t12" {
		t.Fatalf("want=%s got=%s err=%v", want, got, err)
	}
	got, err = resultsDirectory(other)
	if err != nil || got != other {
		t.Fatalf("absolute -out: got=%s err=%v", got, err)
	}
}

func TestPortRequired(t *testing.T) {
	if validPort("") {
		t.Fatal("empty port accepted")
	}
	port := "COM11"
	invalid := "/dev/ttyACM0"
	if runtime.GOOS == "linux" {
		port, invalid = "/dev/ttyACM0", "COM11"
	}
	if !validPort(port) || validPort(invalid) {
		t.Fatalf("port validation: valid=%q invalid=%q", port, invalid)
	}
}

func resultMessage(id, received uint32, hash [32]byte) []byte {
	b := make([]byte, 60)
	copy(b, "T2D!")
	for i, value := range []uint32{id, received, 1000100, 1000000, 3000} {
		binary.LittleEndian.PutUint32(b[4+i*4:], value)
	}
	copy(b[24:56], hash[:])
	seal(b)
	return b
}

func TestResultIntegrityIndependentOfPayloadSize(t *testing.T) {
	for _, size := range []int{1, 64, 512, 513, 4096, 65536} {
		data := bytes.Repeat([]byte{0xa5}, size)
		hash := sha256.Sum256(data)
		result, err := parseResult(resultMessage(7, uint32(size), hash), 7, 3000)
		if err != nil || !result.matches(size, hash) {
			t.Fatalf("size=%d result=%+v err=%v", size, result, err)
		}
		if result.matches(size+1, hash) {
			t.Fatalf("size=%d: length mismatch accepted", size)
		}
		wrongHash := hash
		wrongHash[0] ^= 1
		if result.matches(size, wrongHash) {
			t.Fatalf("size=%d: hash mismatch accepted", size)
		}
	}
}

func TestResultValidation(t *testing.T) {
	hash := sha256.Sum256([]byte("payload"))
	for _, tc := range []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{"wrong ID", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[4:8], 8); seal(b); return b }},
		{"bad CRC", func(b []byte) []byte { b[24] ^= 1; return b }},
		{"wrong observation", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[20:24], 1); seal(b); return b }},
		{"old format", func(b []byte) []byte { b = make([]byte, 68); copy(b, "T12D"); seal(b); return b }},
		{"truncated", func(b []byte) []byte { return b[:59] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseResult(tc.mutate(resultMessage(7, 7, hash)), 7, 3000); err == nil {
				t.Fatal("invalid result accepted")
			}
		})
	}
}

func TestConsoleStart(t *testing.T) {
	var output bytes.Buffer
	printStart(&output, "COM12", 1024, 0, 3*time.Second, 5*time.Second, 0, time.Unix(0, 0).UTC())
	for _, want := range []string{"T12 - USB CDC RX Data Integrity", "Purpose :", runtime.GOOS + "/" + runtime.GOARCH, "COM12", "Size    : 1024 B", "Pause   : 0 ms", "Observe : 3s", "Timeout : 5s", "Count   : ∞"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("missing %q in:\n%s", want, output.String())
		}
	}
}

func TestConsoleTrial(t *testing.T) {
	for _, tc := range []struct {
		outcome, receivedHash, detail, want string
	}{
		{"PASS", "same", "", "[1/10] PASS    3.002s\n"},
		{"FAIL", "different", "length/hash mismatch", "SHA-256  : MISMATCH"},
		{"FAIL", "same", "length mismatch", "SHA-256  : MATCH"},
		{"TIMEOUT", "", "context deadline exceeded", "Detail   : context deadline exceeded"},
		{"ERROR", "", "invalid v2 result", "Detail   : invalid v2 result"},
		{"ABORTED", "", "context canceled", "Detail   : context canceled"},
	} {
		t.Run(tc.outcome+tc.receivedHash, func(t *testing.T) {
			var output bytes.Buffer
			printTrial(&output, 1, 10, tc.outcome, 3002*time.Millisecond, 1024, "576", "same", tc.receivedHash, tc.detail)
			text := output.String()
			if !strings.Contains(text, tc.want) {
				t.Fatalf("missing %q in:\n%s", tc.want, text)
			}
			if tc.outcome == "PASS" && strings.Count(text, "\n") != 1 {
				t.Fatalf("PASS must be one line: %q", text)
			}
			if tc.outcome == "FAIL" && (!strings.Contains(text, "Sent     : 1024 B") || !strings.Contains(text, "Received : 576 B")) {
				t.Fatalf("missing failure measurements: %q", text)
			}
		})
	}
	var output bytes.Buffer
	printTrial(&output, 2, 0, "PASS", time.Second, 64, "64", "same", "same", "")
	if !strings.HasPrefix(output.String(), "[2/∞]") {
		t.Fatal(output.String())
	}
}

func TestConsoleFinal(t *testing.T) {
	for _, outcome := range []string{"PASS", "FAIL", "TIMEOUT", "ERROR", "ABORTED"} {
		t.Run(outcome, func(t *testing.T) {
			var output bytes.Buffer
			totals := map[string]int{outcome: 1}
			start := time.Unix(0, 0).UTC()
			printFinal(&output, totals, 1, 10, start, start.Add(3014*time.Millisecond), "results.csv", nil)
			for _, want := range []string{"RESULT: " + outcome, outcome + "=1", "Completed: 1/10", "Duration : 3.014s", "CSV      : results.csv", "Failure rate:"} {
				if !strings.Contains(output.String(), want) {
					t.Errorf("missing %q in:\n%s", want, output.String())
				}
			}
			if totals[outcome] != 1 || len(totals) != 1 {
				t.Fatal("display changed totals")
			}
		})
	}
	// Cancellation between trials must not display PASS or change recorded counts.
	var output bytes.Buffer
	totals := map[string]int{"PASS": 2}
	printFinal(&output, totals, 2, 0, time.Time{}, time.Time{}, "results.csv", context.Canceled)
	if !strings.Contains(output.String(), "RESULT: ABORTED") || !strings.Contains(output.String(), "Completed: 2/∞") || totals["ABORTED"] != 0 {
		t.Fatal(output.String())
	}
}
