//go:build linux

package servicemgr

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func ownsListeningPort(pid int, host string, port uint16) (bool, error) {
	entries, err := os.ReadDir(filepath.Join("/proc", strconv.Itoa(pid), "fd"))
	if err != nil {
		return false, err
	}
	inodes := make(map[string]bool)
	for _, entry := range entries {
		link, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "fd", entry.Name()))
		if err == nil && strings.HasPrefix(link, "socket:[") && strings.HasSuffix(link, "]") {
			inodes[strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")] = true
		}
	}
	if len(inodes) == 0 {
		return false, nil
	}
	want := net.ParseIP(host)
	if want == nil {
		return false, fmt.Errorf("non-IP managed listen host %q", host)
	}
	for _, table := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		file, err := os.Open(table)
		if err != nil {
			return false, err
		}
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) < 10 || fields[3] != "0A" || !inodes[fields[9]] {
				continue
			}
			address, hexPort, ok := strings.Cut(fields[1], ":")
			if !ok {
				continue
			}
			parsedPort, err := strconv.ParseUint(hexPort, 16, 16)
			if err != nil || uint16(parsedPort) != port {
				continue
			}
			local, err := parseProcTCPIP(address)
			if err != nil {
				continue
			}
			if local.Equal(want) || (local.IsUnspecified() && !want.IsUnspecified()) {
				file.Close()
				return true, nil
			}
		}
		err = errors.Join(scanner.Err(), file.Close())
		if err != nil {
			return false, err
		}
	}
	return false, nil
}

func parseProcTCPIP(raw string) (net.IP, error) {
	bytes, err := hex.DecodeString(raw)
	if err != nil {
		return nil, err
	}
	if len(bytes) != 4 && len(bytes) != 16 {
		return nil, errors.New("unexpected /proc TCP address length")
	}
	for i := 0; i < len(bytes); i += 4 {
		bytes[i], bytes[i+3] = bytes[i+3], bytes[i]
		bytes[i+1], bytes[i+2] = bytes[i+2], bytes[i+1]
	}
	return net.IP(bytes), nil
}
