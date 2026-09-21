package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "missing command")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "exit":
		code, _ := strconv.Atoi(arg(2))
		os.Exit(code)
	case "stderr-exit":
		// stderr-exit <code> <text>: print text to stderr, exit with code — a
		// stand-in for an agent CLI dying with a provider error.
		code, _ := strconv.Atoi(arg(2))
		fmt.Fprintln(os.Stderr, arg(3))
		os.Exit(code)
	case "sleep":
		d, err := time.ParseDuration(arg(2))
		if err != nil {
			fatal(err)
		}
		time.Sleep(d)
	case "env-cwd":
		fmt.Println(os.Getenv("DAB_TEST_VAR"))
		wd, _ := os.Getwd()
		fmt.Println(wd)
	case "write-result-artifact":
		resultDir := mustEnv("GOFER_RESULT_DIR")
		must(os.MkdirAll(filepath.Join(resultDir, "artifacts"), 0o755))
		must(os.WriteFile(filepath.Join(resultDir, "result.json"), []byte(`{"ok":true}`), 0o644))
		must(os.WriteFile(filepath.Join(resultDir, "artifacts", "out.txt"), []byte("hi"), 0o644))
	case "write-role-result":
		role := os.Getenv("GOFER_AGENT_ROLE")
		must(os.WriteFile(filepath.Join(mustEnv("GOFER_RESULT_DIR"), "result.json"), []byte(fmt.Sprintf(`{"role":"%s"}`, role)), 0o644))
	case "stdout-bytes":
		pattern := arg(2)
		n, _ := strconv.Atoi(arg(3))
		var b strings.Builder
		for b.Len() < n {
			b.WriteString(pattern)
		}
		fmt.Print(b.String()[:n])
	case "stdout-lines":
		prefix := arg(2)
		n, _ := strconv.Atoi(arg(3))
		delay := time.Duration(0)
		if len(os.Args) > 4 {
			var err error
			delay, err = time.ParseDuration(os.Args[4])
			if err != nil {
				fatal(err)
			}
		}
		for i := 1; i <= n; i++ {
			fmt.Printf("%s%d\n", prefix, i)
			if delay > 0 {
				time.Sleep(delay)
			}
		}
	case "argv":
		// argv: echo the arguments this process ACTUALLY received, one per line. A
		// test asserting the argv gofer built reads it from the child's own view
		// rather than from what gofer intended to send.
		for _, a := range os.Args[2:] {
			fmt.Println(a)
		}
	case "printf":
		fmt.Print(arg(2))
	case "pty-echo":
		// pty-echo <banner> [tick]: print the banner, then echo every stdin line as
		// "ECHO:<line>" — the stand-in for an agent TUI in the pty tests. A tick
		// duration makes the child keep printing "TICK:<n>" forever, i.e. a terminal
		// that never goes quiet (the InitialInput timeout case).
		fmt.Println(arg(2))
		if len(os.Args) > 3 {
			d, err := time.ParseDuration(os.Args[3])
			must(err)
			if d > 0 {
				go func() {
					for i := 1; ; i++ {
						fmt.Printf("TICK:%d\n", i)
						time.Sleep(d)
					}
				}()
			}
		}
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			fmt.Println("ECHO:" + sc.Text())
		}
	case "cat-file":
		// cat-file <path>: replay a fixture byte-for-byte on stdout (used by tests
		// that need a realistic multi-line agent stream without argv size limits).
		b, err := os.ReadFile(arg(2))
		must(err)
		_, err = os.Stdout.Write(b)
		must(err)
	case "stdout-sleep":
		fmt.Println(arg(2))
		d, err := time.ParseDuration(arg(3))
		if err != nil {
			fatal(err)
		}
		time.Sleep(d)
	case "write-result-file":
		name := arg(2)
		content := arg(3)
		must(os.WriteFile(filepath.Join(mustEnv("GOFER_RESULT_DIR"), name), []byte(content), 0o644))
	case "write-files":
		// write-files <path> <content> [<path> <content> ...]: create each file
		// (parents included) relative to the process cwd — the job-side fixture for
		// the XFER-01 X2 collect tests, where a job's output is what a glob matches.
		for i := 2; i+1 < len(os.Args); i += 2 {
			p := os.Args[i]
			if dir := filepath.Dir(p); dir != "." {
				must(os.MkdirAll(dir, 0o755))
			}
			must(os.WriteFile(p, []byte(os.Args[i+1]), 0o644))
		}
	case "copy-file":
		// copy-file <src> <dst>: copy a file (parents included) relative to the
		// process cwd. A collect test uses it to prove a --upload landed BEFORE the
		// agent ran: the bytes it reads are the bytes the collected file carries.
		src := arg(2)
		dst := arg(3)
		b, err := os.ReadFile(src)
		must(err)
		if dir := filepath.Dir(dst); dir != "." {
			must(os.MkdirAll(dir, 0o755))
		}
		must(os.WriteFile(dst, b, 0o644))
	case "append-file-sleep":
		mustAppend(arg(2), arg(3))
		d, err := time.ParseDuration(arg(4))
		if err != nil {
			fatal(err)
		}
		time.Sleep(d)
	case "cat-equals":
		b, err := os.ReadFile(arg(2))
		must(err)
		if string(b) != arg(3) {
			os.Exit(1)
		}
	case "excl-guard":
		// excl-guard <path> [hold]: create <path> EXCLUSIVELY (O_EXCL), hold it for the
		// duration, then remove it. It is the job-side witness of MUTUAL EXCLUSION: a
		// second process that finds the marker already there exits 3, so a test can prove
		// two jobs never overlapped the same working directory.
		path := arg(2)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintln(os.Stderr, "directory already held: "+path)
			os.Exit(3)
		}
		_, _ = f.WriteString("held\n")
		_ = f.Close()
		time.Sleep(durationArg(3))
		must(os.Remove(path))
	case "rendezvous":
		// rendezvous <dir> <id> [wait]: drop <dir>/<id>, then wait until a SECOND file
		// appears in <dir> (or the window elapses). It is the complementary witness — that
		// two jobs DID overlap: run the same dir with different ids from two jobs and each
		// exits 0 only if both were alive at once, while a serialized pair times out (4).
		dir := arg(2)
		id := arg(3)
		must(os.MkdirAll(dir, 0o755))
		must(os.WriteFile(filepath.Join(dir, id), []byte(id), 0o644))
		deadline := time.Now().Add(durationArg(4))
		for time.Now().Before(deadline) {
			ents, err := os.ReadDir(dir)
			must(err)
			if len(ents) >= 2 {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		fmt.Fprintf(os.Stderr, "rendezvous timed out: no peer job arrived in %s\n", dir)
		os.Exit(4)
	case "interaction-wrapper":
		interactionWrapper(arg(2))
	case "acp-fake":
		// acp-fake [--stop-reason X] [--slow] [--refuse-load] [--delay D]: the
		// in-repo fake ACP agent (internal/acp/acptest) used as a test double by
		// the acp client and acp-agent end-to-end tests.
		os.Exit(acptest.Main(os.Args[2:]))
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		os.Exit(2)
	}
}

func arg(i int) string {
	if len(os.Args) <= i {
		fmt.Fprintf(os.Stderr, "missing arg %d\n", i)
		os.Exit(2)
	}
	return os.Args[i]
}

// durationArg parses os.Args[i] as a Go duration, or 0 when the argument is absent
// (an optional trailing "how long" for the witness modes).
func durationArg(i int) time.Duration {
	if len(os.Args) <= i {
		return 0
	}
	d, err := time.ParseDuration(os.Args[i])
	if err != nil {
		fatal(err)
	}
	return d
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		fmt.Fprintf(os.Stderr, "missing env %s\n", key)
		os.Exit(2)
	}
	return v
}

func must(err error) {
	if err != nil {
		fatal(err)
	}
}

func mustAppend(path, text string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	must(err)
	defer f.Close()
	_, err = f.WriteString(text)
	must(err)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(2)
}

func interactionWrapper(jobID string) {
	base := mustEnv("BRIDGE_BASE")
	token := mustEnv("BRIDGE_TOKEN")
	body := []byte(`{"type":"question","prompt":"need input"}`)
	req, err := http.NewRequest(http.MethodPost, base+"/v1/jobs/"+jobID+"/interactions", bytes.NewReader(body))
	must(err)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	must(err)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "interaction create status=%d\n", resp.StatusCode)
		os.Exit(1)
	}
	for i := 0; i < 200; i++ {
		req, err := http.NewRequest(http.MethodGet, base+"/v1/jobs/"+jobID+"/interactions", nil)
		must(err)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		must(err)
		var obj struct {
			Interactions []struct {
				Answer string `json:"answer"`
			} `json:"interactions"`
		}
		err = json.NewDecoder(resp.Body).Decode(&obj)
		_ = resp.Body.Close()
		must(err)
		for _, it := range obj.Interactions {
			if it.Answer != "" {
				fmt.Println("ANSWER=" + it.Answer)
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	fmt.Fprintln(os.Stderr, "no-answer")
	os.Exit(1)
}
