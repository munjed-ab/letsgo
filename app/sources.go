package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// Sources is the list of music folders. It is saved when the user changes it;
// until then the defaults given at start-up apply (so a -music flag or the
// phone's Music folder keeps working, and only a deliberate change is remembered).
type Sources struct {
	mu   sync.Mutex
	path string   // "" = in memory only
	Dirs []string `json:"dirs"`
}

// OpenSources loads path; if there is no usable file it starts with defaults.
func OpenSources(path string, defaults []string) *Sources {
	s := &Sources{path: path, Dirs: slices.Clone(defaults)}
	if path == "" {
		return s
	}
	var saved Sources
	if b, err := os.ReadFile(path); err == nil && json.Unmarshal(b, &saved) == nil && saved.Dirs != nil {
		s.Dirs = saved.Dirs
	}
	return s
}

func (s *Sources) List() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.Dirs)
}

// inside reports whether path a is b or lies under b.
func inside(a, b string) bool {
	rel, err := filepath.Rel(b, a)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Add appends an existing folder. Folders that overlap one we already have are
// refused: the same songs would appear twice.
func (s *Sources) Add(dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return errBad
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return errBad
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return errors.New("not a folder that exists on this device: " + dir)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.Dirs {
		if inside(abs, d) || inside(d, abs) {
			return errors.New("already covered by " + d)
		}
	}
	s.Dirs = append(s.Dirs, abs)
	s.save()
	return nil
}

func (s *Sources) Remove(dir string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.Index(s.Dirs, dir)
	if i < 0 {
		return errBad
	}
	s.Dirs = slices.Delete(s.Dirs, i, i+1)
	s.save()
	return nil
}

func (s *Sources) save() {
	if s.path == "" {
		return
	}
	b, _ := json.MarshalIndent(s, "", " ")
	os.MkdirAll(filepath.Dir(s.path), 0o755)
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, s.path)
	}
}
