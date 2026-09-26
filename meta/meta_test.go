package meta

import (
	"bytes"
	"encoding/json"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func waitDone(t *testing.T, x *Index) {
	for i := 0; i < 200; i++ {
		if _, done, total := x.All(); total > 0 && done == total {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("scan never finished")
}

func files(t *testing.T) map[string]string {
	abs := func(n string) string { p, _ := filepath.Abs(filepath.Join("testdata", n)); return p }
	return map[string]string{"Rock/song.mp3": abs("tagged.mp3"), "album/track.flac": abs("tagged.flac"), "plain.mp3": abs("plain.mp3")}
}

func TestReadsTagsAndCoverArt(t *testing.T) {
	x := Open("")
	x.Scan(files(t))
	waitDone(t, x)

	mp3, flac := x.Get("Rock/song.mp3"), x.Get("album/track.flac")
	for name, i := range map[string]Info{"mp3": mp3, "flac": flac} {
		if i.Title != "Tagged Song" || i.Artist != "The Artist" || i.Album != "The Album" || i.Art == "" {
			t.Errorf("%s: %+v", name, i)
		}
	}
	if mp3.Art != flac.Art {
		t.Errorf("the same picture in two files got two hashes: %s vs %s", mp3.Art, flac.Art)
	}
	if plain := x.Get("plain.mp3"); !plain.empty() {
		t.Errorf("untagged file has info: %+v", plain)
	}
	if all, done, total := x.All(); len(all) != 2 || done != 3 || total != 3 {
		t.Errorf("All() = %d tagged, %d/%d", len(all), done, total)
	}

	// original, and scaled to fit
	data, mime, ok := x.Art(mp3.Art, 0)
	if !ok || mime != "image/png" {
		t.Fatalf("original: ok=%v mime=%s", ok, mime)
	}
	if img, err := png.Decode(bytes.NewReader(data)); err != nil || img.Bounds().Dx() != 600 {
		t.Errorf("original not the 600px png: %v", err)
	}
	small, mime, ok := x.Art(mp3.Art, 96)
	if !ok || mime != "image/jpeg" {
		t.Fatalf("thumbnail: ok=%v mime=%s", ok, mime)
	}
	img, err := jpeg.Decode(bytes.NewReader(small))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 96 || b.Dy() != 96 {
		t.Errorf("thumbnail is %dx%d, want 96x96", b.Dx(), b.Dy())
	}
	if r, _, _, _ := img.At(48, 48).RGBA(); r>>8 < 200 { // the cover is solid red
		t.Errorf("thumbnail lost the picture (red channel %d)", r>>8)
	}
	if len(small) >= len(data)+2000 && len(small) > 20000 {
		t.Errorf("thumbnail is %d bytes", len(small))
	}
	if _, _, ok := x.Art("nope", 96); ok {
		t.Error("unknown art hash found")
	}
}

func TestFitKeepsAspectAndNeverEnlarges(t *testing.T) {
	wide := image.NewRGBA(image.Rect(0, 0, 400, 200))
	if b := fit(wide, 100).Bounds(); b.Dx() != 100 || b.Dy() != 50 {
		t.Errorf("400x200 -> %dx%d, want 100x50", b.Dx(), b.Dy())
	}
	tall := image.NewRGBA(image.Rect(0, 0, 100, 300))
	if b := fit(tall, 150).Bounds(); b.Dx() != 50 || b.Dy() != 150 {
		t.Errorf("100x300 -> %dx%d, want 50x150", b.Dx(), b.Dy())
	}
	tiny := image.NewRGBA(image.Rect(0, 0, 40, 40))
	if b := fit(tiny, 100).Bounds(); b.Dx() != 40 {
		t.Errorf("a 40px image was enlarged to %d", b.Dx())
	}
}

// The cache is what makes launching on a phone with 1200 songs quick: unchanged
// files are not opened again, changed files are.
func TestCacheIsUsedUntilTheFileChanges(t *testing.T) {
	dir := t.TempDir()
	src, _ := os.ReadFile("testdata/tagged.mp3")
	song := filepath.Join(dir, "song.mp3")
	os.WriteFile(song, src, 0o644)
	cache := filepath.Join(dir, "meta.json")
	f := map[string]string{"song.mp3": song}

	x := Open(cache)
	x.Scan(f)
	waitDone(t, x)
	time.Sleep(50 * time.Millisecond) // the cache is written right after the scan completes

	// Tamper with the cache: a fresh index must trust it for an unchanged file...
	var recs map[string]record
	b, err := os.ReadFile(cache)
	if err != nil || json.Unmarshal(b, &recs) != nil {
		t.Fatalf("no cache written: %v", err)
	}
	r := recs[song]
	r.Title = "FROM CACHE"
	recs[song] = r
	b, _ = json.Marshal(recs)
	os.WriteFile(cache, b, 0o644)

	y := Open(cache)
	y.Scan(f)
	waitDone(t, y)
	if got := y.Get("song.mp3").Title; got != "FROM CACHE" {
		t.Errorf("unchanged file was re-read (title %q)", got)
	}

	// ...but re-read it once the file changes.
	os.WriteFile(song, append(src, 0), 0o644)
	z := Open(cache)
	z.Scan(f)
	waitDone(t, z)
	if got := z.Get("song.mp3").Title; got != "Tagged Song" {
		t.Errorf("changed file not re-read (title %q)", got)
	}
}
