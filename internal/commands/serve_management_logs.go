package commands

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gookit/gcli/v3"
	"github.com/inhere/gofer/internal/servicemgr"
)

const maxServeLogRead = 4 << 20

func runServeManagedLogs(c *gcli.Command, _ []string) error {
	if logsOpts.lines < 1 || logsOpts.lines > 1000 {
		return errors.New("--lines must be between 1 and 1000")
	}
	m, err := managerFor(logsOpts.name)
	if err != nil {
		return err
	}
	if _, err := m.LoadSpec(); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if logsOpts.journal {
		if logsOpts.follow {
			return managedJournalFollow(ctx, m, logsOpts.lines, os.Stdout)
		}
		output, err := managedJournal(ctx, m, logsOpts.lines)
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(output)
		return err
	}
	path, err := managedApplicationLogFromManager(m)
	if err != nil {
		return err
	}
	last, offset, err := serveLogTail(path, logsOpts.lines)
	if err != nil {
		return err
	}
	if _, err := os.Stdout.Write(last); err != nil {
		return err
	}
	if !logsOpts.follow {
		return nil
	}
	c.Printf("following %s\n", path)
	return followServeLog(ctx, path, offset, os.Stdout)
}

func managedApplicationLogFromManager(m *servicemgr.Manager) (string, error) {
	spec, err := m.LoadSpec()
	if err != nil {
		return "", err
	}
	return managedApplicationLog(spec)
}

// serveLogTail reads only a bounded suffix of the application file.
func serveLogTail(path string, lines int) ([]byte, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	start := info.Size() - maxServeLogRead
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, 0, err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxServeLogRead))
	if err != nil {
		return nil, 0, err
	}
	if start > 0 {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		}
	}
	trimmed := bytes.TrimSuffix(data, []byte{'\n'})
	parts := bytes.Split(trimmed, []byte{'\n'})
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	result := bytes.Join(parts, []byte{'\n'})
	if len(result) > 0 {
		result = append(result, '\n')
	}
	return result, info.Size(), nil
}

func followServeLog(ctx context.Context, path string, offset int64, out io.Writer) error {
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	var previous os.FileInfo
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
		f, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return err
		}
		if previous != nil && !os.SameFile(previous, info) {
			offset = 0
		}
		previous = info
		if info.Size() < offset {
			offset = 0
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			f.Close()
			return err
		}
		n, err := io.Copy(out, io.LimitReader(f, maxServeLogRead))
		closeErr := f.Close()
		if err != nil {
			return fmt.Errorf("follow application log: %w", err)
		}
		if closeErr != nil {
			return closeErr
		}
		offset += n
	}
}
