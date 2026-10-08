package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"letsgo/meta"
)

// Playlist is an ordered list of tracks (paths relative to the music folder).
// Folder groups playlists in the UI; "" means top level.
type Playlist struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Folder string   `json:"folder"`
	Tracks []string `json:"tracks"`
}

// Lists is everything the user curates: favourite tracks, playlists, and the
// folders that group playlists. It is saved to disk after every change (atomic
// rename, so a crash can't leave half a file). Favourites double as a built-in
// playlist in the UIs.
type Lists struct {
	mu   sync.Mutex
	path string // "" = in memory only
	// Song names the song a track is (title and artist), "" when unknown. A playlist never gets a
	// second copy of a song saved in another folder. nil = only the very same file counts.
	Song func(track string) string `json:"-"`

	Favorites []string   `json:"favorites"`
	Folders   []string   `json:"folders"`
	Playlists []Playlist `json:"playlists"`
}

var errBad = errors.New("bad request")

// OpenLists loads the file at path (missing = empty). A corrupt file is kept as
// path+".bad" rather than overwritten, so nothing the user made is silently lost.
func OpenLists(path string) *Lists {
	l := &Lists{path: path}
	if path == "" {
		return l
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return l
	}
	if json.Unmarshal(b, l) != nil {
		os.Rename(path, path+".bad")
		*l = Lists{path: path}
	}
	return l
}

// Snapshot returns a deep copy safe to marshal while others mutate.
func (l *Lists) Snapshot() *Lists {
	l.mu.Lock()
	defer l.mu.Unlock()
	c := &Lists{
		Favorites: slices.Clone(l.Favorites),
		Folders:   slices.Clone(l.Folders),
		Playlists: make([]Playlist, len(l.Playlists)),
	}
	for i, p := range l.Playlists {
		c.Playlists[i] = p
		c.Playlists[i].Tracks = slices.Clone(p.Tracks)
	}
	// JSON should say [] not null
	if c.Favorites == nil {
		c.Favorites = []string{}
	}
	if c.Folders == nil {
		c.Folders = []string{}
	}
	for i := range c.Playlists {
		if c.Playlists[i].Tracks == nil {
			c.Playlists[i].Tracks = []string{}
		}
	}
	return c
}

// save must be called with l.mu held.
func (l *Lists) save() {
	if l.path == "" {
		return
	}
	b, _ := json.MarshalIndent(l, "", " ")
	os.MkdirAll(filepath.Dir(l.path), 0o755)
	tmp := l.path + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, l.path)
	}
}

func clean(s string) string { return strings.TrimSpace(s) }

func (l *Lists) find(id string) *Playlist {
	for i := range l.Playlists {
		if l.Playlists[i].ID == id {
			return &l.Playlists[i]
		}
	}
	return nil
}

func (l *Lists) hasFolder(name string) bool { return slices.Contains(l.Folders, name) }

// ensureFolder makes sure a non-empty folder name exists.
func (l *Lists) ensureFolder(name string) {
	if name != "" && !l.hasFolder(name) {
		l.Folders = append(l.Folders, name)
	}
}

// SetFavorite adds or removes one track from favourites (idempotent).
func (l *Lists) SetFavorite(track string, on bool) error { return l.SetFavorites([]string{track}, on) }

// SetFavorites adds or removes many tracks at once (one save). Tracks already in
// the wanted state are skipped; blank ids are an error.
func (l *Lists) SetFavorites(tracks []string, on bool) error {
	if len(tracks) == 0 || slices.Contains(tracks, "") {
		return errBad
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	changed := false
	for _, t := range tracks {
		i := slices.Index(l.Favorites, t)
		switch {
		case on && i < 0:
			l.Favorites = append(l.Favorites, t)
			changed = true
		case !on && i >= 0:
			l.Favorites = slices.Delete(l.Favorites, i, i+1)
			changed = true
		}
	}
	if changed {
		l.save()
	}
	return nil
}

// CreatePlaylist makes an empty playlist and returns its id. A folder that does
// not exist yet is created.
func (l *Lists) CreatePlaylist(name, folder string) (string, error) {
	name, folder = clean(name), clean(folder)
	if name == "" || len(name) > 100 || len(folder) > 100 {
		return "", errBad
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	id := strconv.FormatInt(time.Now().UnixNano(), 36)
	for l.find(id) != nil { // two creates in the same nanosecond
		id += "x"
	}
	l.ensureFolder(folder)
	l.Playlists = append(l.Playlists, Playlist{ID: id, Name: name, Folder: folder, Tracks: []string{}})
	l.save()
	return id, nil
}

// UpdatePlaylist renames and/or moves a playlist; nil leaves a field as it is.
func (l *Lists) UpdatePlaylist(id string, name, folder *string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	p := l.find(id)
	if p == nil {
		return errBad
	}
	if name != nil {
		n := clean(*name)
		if n == "" || len(n) > 100 {
			return errBad
		}
		p.Name = n
	}
	if folder != nil {
		f := clean(*folder)
		if len(f) > 100 {
			return errBad
		}
		l.ensureFolder(f)
		p.Folder = f
	}
	l.save()
	return nil
}

func (l *Lists) DeletePlaylist(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	i := slices.IndexFunc(l.Playlists, func(p Playlist) bool { return p.ID == id })
	if i < 0 {
		return errBad
	}
	l.Playlists = slices.Delete(l.Playlists, i, i+1)
	l.save()
	return nil
}

// AddToPlaylist appends tracks, skipping ones already in the playlist: the same file, or the same
// song from another file (see Song).
func (l *Lists) AddToPlaylist(id string, tracks []string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	p := l.find(id)
	if p == nil {
		return errBad
	}
	songs := map[string]bool{}
	song := func(t string) string {
		if l.Song == nil {
			return ""
		}
		return l.Song(t)
	}
	for _, t := range p.Tracks {
		songs[song(t)] = true
	}
	for _, t := range tracks {
		if t == "" || slices.Contains(p.Tracks, t) {
			continue
		}
		if s := song(t); s != "" {
			if songs[s] {
				continue
			}
			songs[s] = true
		}
		p.Tracks = append(p.Tracks, t)
	}
	l.save()
	return nil
}

// RemoveFromPlaylist drops the given tracks from a playlist (absent ones are ignored).
func (l *Lists) RemoveFromPlaylist(id string, tracks ...string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	p := l.find(id)
	if p == nil {
		return errBad
	}
	before := len(p.Tracks)
	p.Tracks = slices.DeleteFunc(p.Tracks, func(t string) bool { return slices.Contains(tracks, t) })
	if len(p.Tracks) != before {
		l.save()
	}
	return nil
}

func (l *Lists) CreateFolder(name string) error {
	name = clean(name)
	if name == "" || len(name) > 100 {
		return errBad
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ensureFolder(name)
	l.save()
	return nil
}

// RenameFolder renames a folder and the playlists in it.
func (l *Lists) RenameFolder(name, to string) error {
	to = clean(to)
	l.mu.Lock()
	defer l.mu.Unlock()
	i := slices.Index(l.Folders, name)
	if i < 0 || to == "" || len(to) > 100 || (to != name && l.hasFolder(to)) {
		return errBad
	}
	l.Folders[i] = to
	for j := range l.Playlists {
		if l.Playlists[j].Folder == name {
			l.Playlists[j].Folder = to
		}
	}
	l.save()
	return nil
}

// DeleteFolder removes a folder; its playlists move to the top level (never deleted).
func (l *Lists) DeleteFolder(name string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	i := slices.Index(l.Folders, name)
	if i < 0 {
		return errBad
	}
	l.Folders = slices.Delete(l.Folders, i, i+1)
	for j := range l.Playlists {
		if l.Playlists[j].Folder == name {
			l.Playlists[j].Folder = ""
		}
	}
	l.save()
	return nil
}

// songKey is what makes two files the same song: the title and first artist from their tags, with
// case, spacing and punctuation ignored. A file without a title tag is only ever itself.
// ponytail: tags only; file names alone gave false matches (two takes of a song on one album).
func songKey(i meta.Info) string {
	if strings.TrimSpace(i.Title) == "" {
		return ""
	}
	artist, _, _ := strings.Cut(i.Artist, ",")
	return squash(i.Title) + "\x00" + squash(artist)
}

func squash(s string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}), " ")
}
