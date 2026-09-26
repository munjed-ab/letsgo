// Package meta reads song titles, artists, albums and embedded cover art from
// the tags in audio files (ID3, FLAC, Ogg, MP4), so the apps can show "Song –
// Artist" with its artwork instead of a file name.
//
// Reading a whole library is slow on a phone, so Scan works in the background
// and remembers what it read (keyed by file size and modification time). Until
// a track has been read, callers fall back to its file name.
package meta

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/jpeg"
	_ "image/png" // decoders for embedded artwork
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/dhowden/tag"
)

// Info is what the tags say about one track. Art is a short hash of the embedded
// picture (empty = none); tracks of one album share it, so clients fetch it once.
type Info struct {
	Title  string `json:"t,omitempty"`
	Artist string `json:"a,omitempty"`
	Album  string `json:"al,omitempty"`
	Art    string `json:"art,omitempty"`
}

func (i Info) empty() bool { return i == Info{} }

type record struct {
	Info
	Size int64 `json:"s"`
	Mod  int64 `json:"m"`
}

// Index holds tag info for the current library.
type Index struct {
	mu        sync.Mutex
	cachePath string // "" = don't persist
	recs      map[string]record
	files     map[string]string // track id -> file
	artFrom   map[string]string // art hash -> a file that has that picture
	done      int
	total     int
	gen       int // bumped by every Scan so a superseded scan stops early
	thumbs    map[string]thumb
}

type thumb struct {
	data []byte
	mime string
}

// Open loads the cache at cachePath (missing or unreadable = start empty).
func Open(cachePath string) *Index {
	x := &Index{cachePath: cachePath, recs: map[string]record{}, files: map[string]string{}, artFrom: map[string]string{}, thumbs: map[string]thumb{}}
	if b, err := os.ReadFile(cachePath); err == nil && cachePath != "" {
		json.Unmarshal(b, &x.recs)
	}
	return x
}

// Scan (re)reads tags for files (track id -> path) in the background and returns
// at once. Files unchanged since the cache was written are not opened again.
func (x *Index) Scan(files map[string]string) {
	x.mu.Lock()
	x.gen++
	gen := x.gen
	x.files = files
	x.total, x.done = len(files), 0
	x.mu.Unlock()
	go x.run(gen, files)
}

func (x *Index) run(gen int, files map[string]string) {
	ids := make([]string, 0, len(files))
	for id := range files {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	fresh := map[string]record{}
	for n, id := range ids {
		path := files[id]
		fi, err := os.Stat(path)
		if err != nil {
			continue
		}
		x.mu.Lock()
		if x.gen != gen {
			x.mu.Unlock()
			return
		}
		rec, ok := x.recs[path]
		x.mu.Unlock()
		if !ok || rec.Size != fi.Size() || rec.Mod != fi.ModTime().UnixNano() {
			rec = record{Info: read(path), Size: fi.Size(), Mod: fi.ModTime().UnixNano()}
		}
		fresh[path] = rec
		x.mu.Lock()
		if x.gen != gen {
			x.mu.Unlock()
			return
		}
		x.recs[path] = rec
		if rec.Art != "" {
			x.artFrom[rec.Art] = path
		}
		x.done = n + 1
		x.mu.Unlock()
		if (n+1)%250 == 0 {
			x.save(nil)
		}
	}
	x.save(fresh) // drop cache entries for files that are gone
}

// save writes the cache; if keep is non-nil only those paths are kept.
func (x *Index) save(keep map[string]record) {
	if x.cachePath == "" {
		return
	}
	x.mu.Lock()
	if keep != nil {
		x.recs = keep
	}
	b, _ := json.Marshal(x.recs)
	x.mu.Unlock()
	os.MkdirAll(filepath.Dir(x.cachePath), 0o755)
	tmp := x.cachePath + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, x.cachePath)
	}
}

// read pulls the tags out of one file. A file with no or broken tags yields an
// empty Info (and is remembered as such, so it is not retried every launch).
func read(path string) Info {
	f, err := os.Open(path)
	if err != nil {
		return Info{}
	}
	defer f.Close()
	m, err := tag.ReadFrom(f)
	if err != nil {
		return Info{}
	}
	i := Info{Title: m.Title(), Artist: m.Artist(), Album: m.Album()}
	if p := m.Picture(); p != nil && len(p.Data) > 0 {
		sum := sha1.Sum(p.Data)
		i.Art = hex.EncodeToString(sum[:6])
	}
	return i
}

// Get returns what is known about a track (zero Info if not read yet or untagged).
func (x *Index) Get(id string) Info {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.recs[x.files[id]].Info
}

// All returns the tagged tracks read so far, plus scan progress.
func (x *Index) All() (tracks map[string]Info, done, total int) {
	x.mu.Lock()
	defer x.mu.Unlock()
	tracks = make(map[string]Info, len(x.files))
	for id, path := range x.files {
		if r, ok := x.recs[path]; ok && !r.empty() {
			tracks[id] = r.Info
		}
	}
	return tracks, x.done, x.total
}

// Art returns the picture with the given hash scaled to fit size x size pixels
// (size <= 0 = original), as JPEG when it could be re-encoded. ok is false when
// no such picture is known.
func (x *Index) Art(hash string, size int) (data []byte, mime string, ok bool) {
	key := hash + "@" + string(rune(size))
	x.mu.Lock()
	if t, hit := x.thumbs[key]; hit {
		x.mu.Unlock()
		return t.data, t.mime, true
	}
	path := x.artFrom[hash]
	x.mu.Unlock()
	if path == "" {
		return nil, "", false
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, "", false
	}
	defer f.Close()
	m, err := tag.ReadFrom(f)
	if err != nil || m.Picture() == nil {
		return nil, "", false
	}
	data, mime = m.Picture().Data, m.Picture().MIMEType
	if size > 0 {
		if img, _, err := image.Decode(bytes.NewReader(data)); err == nil {
			var buf bytes.Buffer
			if jpeg.Encode(&buf, fit(img, size), &jpeg.Options{Quality: 85}) == nil {
				data, mime = buf.Bytes(), "image/jpeg"
			}
		}
	}
	x.mu.Lock()
	if len(x.thumbs) > 300 { // ponytail: wipe when full; a real LRU if this ever thrashes
		x.thumbs = map[string]thumb{}
	}
	x.thumbs[key] = thumb{data, mime}
	x.mu.Unlock()
	return data, mime, true
}

// fit scales img down (never up) so it fits in max x max, averaging the source
// pixels under each destination pixel (box filter: no aliasing, good enough for art).
func fit(src image.Image, max int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= max && h <= max {
		return src
	}
	nw, nh := max, max
	if w > h {
		nh = h * max / w
	} else {
		nw = w * max / h
	}
	nw, nh = maxInt(nw, 1), maxInt(nh, 1)
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		y0, y1 := y*h/nh, maxInt((y+1)*h/nh, y*h/nh+1)
		for x := 0; x < nw; x++ {
			x0, x1 := x*w/nw, maxInt((x+1)*w/nw, x*w/nw+1)
			var r, g, bl, a, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					cr, cg, cb, ca := src.At(b.Min.X+sx, b.Min.Y+sy).RGBA()
					r, g, bl, a, n = r+uint64(cr), g+uint64(cg), bl+uint64(cb), a+uint64(ca), n+1
				}
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = byte(r/n>>8), byte(g/n>>8), byte(bl/n>>8), byte(a/n>>8)
		}
	}
	return dst
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
