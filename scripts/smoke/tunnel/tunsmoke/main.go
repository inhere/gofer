// Command tunsmoke is a stdlib-only helper for the TUN-01 container smoke test
// (scripts/smoke/tunnel/run-smoke.sh). It is NOT part of gofer.
//
//	tunsmoke serve -addr 127.0.0.1:0 -portfile F   TCP echo server; writes the chosen port to F
//	tunsmoke roundtrip -addr H:P -size N           send N random bytes, verify the echo (exit 0/1)
//	tunsmoke hold -addr H:P -dur 5s                keep one echoed connection busy for dur
//	tunsmoke freeport                              print a currently free loopback port
package main

import (
	"bytes"
	"crypto/rand"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fail("usage: tunsmoke serve|roundtrip|hold|freeport ...")
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:0", "address")
	portfile := fs.String("portfile", "", "file to write the listen port to")
	size := fs.Int("size", 200*1024, "roundtrip payload size")
	dur := fs.Duration("dur", 5*time.Second, "hold duration")
	_ = fs.Parse(os.Args[2:])

	switch os.Args[1] {
	case "serve":
		serve(*addr, *portfile)
	case "roundtrip":
		roundtrip(*addr, *size)
	case "hold":
		hold(*addr, *dur)
	case "freeport":
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			fail(err.Error())
		}
		fmt.Println(ln.Addr().(*net.TCPAddr).Port)
		_ = ln.Close()
	default:
		fail("unknown mode " + os.Args[1])
	}
}

func serve(addr, portfile string) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fail(err.Error())
	}
	if portfile != "" {
		port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
		if err := os.WriteFile(portfile, []byte(port), 0o644); err != nil {
			fail(err.Error())
		}
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			_, _ = io.Copy(c, c)
		}()
	}
}

func roundtrip(addr string, size int) {
	c, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		fail("dial: " + err.Error())
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	want := make([]byte, size)
	_, _ = rand.Read(want)
	errc := make(chan error, 1)
	go func() {
		_, err := c.Write(want)
		errc <- err
	}()
	got := make([]byte, size)
	if _, err := io.ReadFull(c, got); err != nil {
		fail(fmt.Sprintf("read echo: %v", err))
	}
	if err := <-errc; err != nil {
		fail("write: " + err.Error())
	}
	if !bytes.Equal(want, got) {
		fail("echo mismatch")
	}
	fmt.Printf("roundtrip ok: %d bytes\n", size)
}

func hold(addr string, dur time.Duration) {
	c, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		fail("dial: " + err.Error())
	}
	defer c.Close()
	end := time.Now().Add(dur)
	buf := []byte("ping")
	got := make([]byte, len(buf))
	for time.Now().Before(end) {
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := c.Write(buf); err != nil {
			fail("hold write: " + err.Error())
		}
		if _, err := io.ReadFull(c, got); err != nil {
			fail("hold read: " + err.Error())
		}
		time.Sleep(200 * time.Millisecond)
	}
	fmt.Println("hold ok")
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "tunsmoke:", msg)
	os.Exit(1)
}
