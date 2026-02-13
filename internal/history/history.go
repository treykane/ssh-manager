package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/treykane/ssh-manager/internal/appconfig"
	"github.com/treykane/ssh-manager/internal/model"
)

type store struct {
	LastUsed map[string]int64 `json:"last_used"`
}

func filePath() (string, error) {
	dir, err := appconfig.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "history.json"), nil
}

// Touch records successful activity for a host alias.
func Touch(alias string) error {
	st, err := load()
	if err != nil {
		return err
	}
	if st.LastUsed == nil {
		st.LastUsed = map[string]int64{}
	}
	st.LastUsed[alias] = time.Now().Unix()
	return save(st)
}

// LastUsed returns last successful activity timestamps by alias.
func LastUsed() (map[string]int64, error) {
	st, err := load()
	if err != nil {
		return nil, err
	}
	return st.LastUsed, nil
}

// SortHostsRecent returns a new slice sorted by recent activity (desc), then alias.
func SortHostsRecent(hosts []model.HostEntry, lastUsed map[string]int64) []model.HostEntry {
	out := append([]model.HostEntry(nil), hosts...)
	sort.Slice(out, func(i, j int) bool {
		ti := lastUsed[out[i].Alias]
		tj := lastUsed[out[j].Alias]
		if ti != tj {
			return ti > tj
		}
		return out[i].Alias < out[j].Alias
	})
	return out
}

func load() (store, error) {
	path, err := filePath()
	if err != nil {
		return store{}, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return store{LastUsed: map[string]int64{}}, nil
		}
		return store{}, err
	}
	var st store
	if err := json.Unmarshal(b, &st); err != nil {
		return store{LastUsed: map[string]int64{}}, nil
	}
	if st.LastUsed == nil {
		st.LastUsed = map[string]int64{}
	}
	return st, nil
}

func save(st store) error {
	path, err := filePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
