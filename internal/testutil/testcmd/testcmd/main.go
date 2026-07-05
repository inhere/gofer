package main

import (
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
	case "printf":
		fmt.Print(arg(2))
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
	case "interaction-wrapper":
		interactionWrapper(arg(2))
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
