package job

import (
	"fmt"
	"path/filepath"

	"github.com/inhere/gofer/internal/store"
)

// TailLog returns the last maxBytes of a job's stdout/stderr (whole file when
// maxBytes<=0). It locates the FileStore base from the job's ResultDir, mirroring
// httpapi serveLog. Returns an error if the job id is unknown.
func (s *Service) TailLog(id string, stream store.Stream, maxBytes int64) ([]byte, error) {
	res, ok := s.Get(id)
	if !ok {
		return nil, fmt.Errorf("unknown job %q", id)
	}
	base := filepath.Dir(res.ResultDir)
	return store.NewFileStore(base).ReadLogTail(id, stream, maxBytes)
}
