package bundle

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/treykane/ssh-manager/internal/appconfig"
	"gopkg.in/yaml.v3"
)

// Entry describes one tunnel startup target in a bundle.
type Entry struct {
	HostAlias       string `yaml:"host_alias" json:"host_alias"`
	ForwardSelector string `yaml:"forward_selector,omitempty" json:"forward_selector,omitempty"`
}

// Definition is a named sequence of bundle entries.
type Definition struct {
	Name    string  `yaml:"name" json:"name"`
	Entries []Entry `yaml:"entries" json:"entries"`
}

type fileModel struct {
	Bundles map[string]Definition `yaml:"bundles"`
}

func filePath() (string, error) {
	dir, err := appconfig.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "bundles.yaml"), nil
}

// LoadAll returns all bundles sorted by name.
func LoadAll() ([]Definition, error) {
	fm, err := loadFile()
	if err != nil {
		return nil, err
	}
	out := make([]Definition, 0, len(fm.Bundles))
	for _, b := range fm.Bundles {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get fetches one bundle by name.
func Get(name string) (Definition, error) {
	fm, err := loadFile()
	if err != nil {
		return Definition{}, err
	}
	b, ok := fm.Bundles[name]
	if !ok {
		return Definition{}, fmt.Errorf("bundle not found: %s", name)
	}
	return b, nil
}

// Create adds or replaces a bundle definition.
func Create(name string, entries []Entry) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("bundle name cannot be empty")
	}
	if len(entries) == 0 {
		return fmt.Errorf("bundle must include at least one host entry")
	}
	for i := range entries {
		entries[i].HostAlias = strings.TrimSpace(entries[i].HostAlias)
		entries[i].ForwardSelector = strings.TrimSpace(entries[i].ForwardSelector)
		if entries[i].HostAlias == "" {
			return fmt.Errorf("bundle entry %d missing host alias", i)
		}
	}

	fm, err := loadFile()
	if err != nil {
		return err
	}
	fm.Bundles[name] = Definition{Name: name, Entries: entries}
	return saveFile(fm)
}

// Delete removes a bundle by name.
func Delete(name string) error {
	fm, err := loadFile()
	if err != nil {
		return err
	}
	if _, ok := fm.Bundles[name]; !ok {
		return fmt.Errorf("bundle not found: %s", name)
	}
	delete(fm.Bundles, name)
	return saveFile(fm)
}

func loadFile() (fileModel, error) {
	path, err := filePath()
	if err != nil {
		return fileModel{}, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fileModel{Bundles: map[string]Definition{}}, nil
		}
		return fileModel{}, err
	}
	var fm fileModel
	if err := yaml.Unmarshal(b, &fm); err != nil {
		return fileModel{}, fmt.Errorf("parse bundles: %w", err)
	}
	if fm.Bundles == nil {
		fm.Bundles = map[string]Definition{}
	}
	return fm, nil
}

func saveFile(fm fileModel) error {
	path, err := filePath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := yaml.Marshal(fm)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
