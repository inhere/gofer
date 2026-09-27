package tracker

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

const idSpace = 36 * 36 * 36 * 36

func (s *Store) NextIssueID(prefix, parent string) (string, error) {
	issues, err := s.ReadIssues()
	if err != nil {
		return "", err
	}
	return GenerateIssueID(issues, prefix, parent)
}

func GenerateIssueID(items []Issue, prefix, parent string) (string, error) {
	if parent != "" {
		found, max := false, 0
		for _, item := range items {
			if item.ID == parent {
				found = true
			}
			if n, err := strconv.Atoi(strings.TrimPrefix(item.ID, parent+".")); err == nil && strings.HasPrefix(item.ID, parent+".") && n > max {
				max = n
			}
		}
		if !found {
			return "", fmt.Errorf("parent issue %s not found", parent)
		}
		return fmt.Sprintf("%s.%d", parent, max+1), nil
	}
	if prefix == "" {
		return "", errors.New("empty issue prefix")
	}
	used := make(map[string]bool, len(items))
	for _, item := range items {
		used[item.ID] = true
	}
	for attempt := 0; attempt < idSpace; attempt++ {
		n, err := rand.Int(rand.Reader, big.NewInt(idSpace))
		if err != nil {
			return "", err
		}
		id := fmt.Sprintf("%s-%04s", prefix, strconv.FormatInt(n.Int64(), 36))
		id = strings.ReplaceAll(id, " ", "0")
		if !used[id] {
			return id, nil
		}
	}
	return "", errors.New("issue ID space exhausted")
}
