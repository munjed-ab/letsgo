package app

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPlaysTopAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plays.json")
	p := OpenPlays(path)
	for _, tr := range []string{"a", "b", "b", "c", "c", "c", "gone", "gone", "gone", "gone"} {
		p.Record(tr)
	}
	p.Tracks["a"] = PlayStat{1, 100} // fix the times so ties are decided by recency
	p.Tracks["b"] = PlayStat{2, 100}
	p.Tracks["c"] = PlayStat{3, 100}
	p.Tracks["d"] = PlayStat{2, 200} // same count as b, played later
	keep := func(t string) bool { return t != "gone" }

	top, counts := p.Top(10, keep)
	if want := []string{"c", "d", "b", "a"}; !reflect.DeepEqual(top, want) {
		t.Errorf("top %v, want %v (most played first, ties newest first, deleted songs left out)", top, want)
	}
	if counts["c"] != 3 || counts["d"] != 2 || len(counts) != 4 {
		t.Errorf("counts %v", counts)
	}
	if top, _ := p.Top(2, keep); len(top) != 2 {
		t.Errorf("limit not applied: %v", top)
	}

	p.Record("a") // saves
	again := OpenPlays(path)
	if again.Tracks["a"].N != 2 || again.Tracks["c"].N != 3 {
		t.Errorf("counts were not kept across a restart: %v", again.Tracks)
	}
	os.WriteFile(path, []byte("{not json"), 0o644)
	if len(OpenPlays(path).Tracks) != 0 {
		t.Error("a corrupt file must start empty, not crash")
	}
}

func TestListsAPIHasMostPlayed(t *testing.T) {
	dir := t.TempDir()
	writeRampNamed(t, dir, "one.wav", 1)
	writeRampNamed(t, dir, "two.wav", 1)
	addr := freeAddr(t)
	n, err := Start([]string{dir}, "", freeAddr(t), addr, "node", 500)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Stop()
	for _, tr := range []string{"one.wav", "two.wav", "two.wav", "deleted.wav", "deleted.wav", "deleted.wav"} {
		n.plays.Record(tr)
	}
	resp, err := http.Get("http://" + addr + "/api/lists")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var l struct {
		Favorites  []string
		MostPlayed []string
		PlayCounts map[string]int
	}
	json.NewDecoder(resp.Body).Decode(&l)
	if want := []string{"two.wav", "one.wav"}; !reflect.DeepEqual(l.MostPlayed, want) {
		t.Errorf("mostPlayed %v, want %v", l.MostPlayed, want)
	}
	if l.PlayCounts["two.wav"] != 2 || l.Favorites == nil {
		t.Errorf("counts %v favorites %v", l.PlayCounts, l.Favorites)
	}
}
