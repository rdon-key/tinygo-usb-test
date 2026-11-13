package main

import (
	"context"
	"syscall"
	"testing"
	"time"
)

// Unix sockets simulate byte transport only; these tests do not validate USB or termios.
func TestExchange(t *testing.T) {
	for _, tc := range []struct {
		name, reply, want string
		cancel            bool
	}{
		{"correct", "PONG 42\n", "PASS", false},
		{"wrong_sequence", "PONG 41\n", "FAIL", false},
		{"no_reply", "", "TIMEOUT", false},
		{"partial_reply", "PONG 42", "TIMEOUT", false},
		{"cancel", "", "ABORTED", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_NONBLOCK, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer syscall.Close(fds[0])
			defer syscall.Close(fds[1])
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			finished := make(chan string, 1)
			go func() { outcome, _ := exchange(ctx, fds[0], "/dev/tty", 42, 150*time.Millisecond); finished <- outcome }()
			var buf [32]byte
			var request string
			deadline := time.Now().Add(time.Second)
			for len(request) < 8 && time.Now().Before(deadline) {
				n, _ := syscall.Read(fds[1], buf[:])
				if n > 0 {
					request += string(buf[:n])
				}
				time.Sleep(time.Millisecond)
			}
			if request != "PING 42\n" {
				t.Fatalf("request=%q", request)
			}
			if tc.reply != "" {
				for _, b := range []byte(tc.reply) {
					if _, e := syscall.Write(fds[1], []byte{b}); e != nil {
						t.Fatal(e)
					}
					time.Sleep(time.Millisecond)
				}
			}
			if tc.cancel {
				cancel()
			}
			select {
			case got := <-finished:
				if got != tc.want {
					t.Fatalf("got %s want %s", got, tc.want)
				}
			case <-time.After(time.Second):
				t.Fatal("exchange did not terminate")
			}
		})
	}
}
