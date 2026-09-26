package app

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFavoritesAreIdempotent(t *testing.T) {
	l := OpenLists("")
	l.SetFavorite("a.mp3", true)
	l.SetFavorite("a.mp3", true)
	l.SetFavorite("b.mp3", true)
	if got := l.Snapshot().Favorites; !reflect.DeepEqual(got, []string{"a.mp3", "b.mp3"}) {
		t.Fatalf("favorites %v", got)
	}
	l.SetFavorite("a.mp3", false)
	l.SetFavorite("zzz.mp3", false) // not a favourite: no-op, no error
	if got := l.Snapshot().Favorites; !reflect.DeepEqual(got, []string{"b.mp3"}) {
		t.Fatalf("favorites after remove %v", got)
	}
	if l.SetFavorite("", true) == nil {
		t.Error("empty track accepted")
	}
}

func TestPlaylistsAndFolders(t *testing.T) {
	l := OpenLists("")
	road, err := l.CreatePlaylist("  Road trip ", "Trips") // trimmed; folder auto-created
	if err != nil {
		t.Fatal(err)
	}
	gym, _ := l.CreatePlaylist("Gym", "")
	if _, err := l.CreatePlaylist("   ", ""); err == nil {
		t.Error("blank name accepted")
	}

	l.AddToPlaylist(road, []string{"a.mp3", "b.mp3", "a.mp3", ""}) // dupes and blanks skipped
	l.AddToPlaylist(road, []string{"c.mp3"})
	s := l.Snapshot()
	if !reflect.DeepEqual(s.Playlists[0].Tracks, []string{"a.mp3", "b.mp3", "c.mp3"}) || s.Playlists[0].Name != "Road trip" {
		t.Fatalf("playlist = %+v", s.Playlists[0])
	}
	if !reflect.DeepEqual(s.Folders, []string{"Trips"}) {
		t.Fatalf("folders = %v", s.Folders)
	}

	l.RemoveFromPlaylist(road, "b.mp3")
	newName, folder := "Roadtrip 2", "Summer"
	l.UpdatePlaylist(gym, &newName, &folder) // rename + move into a new folder
	s = l.Snapshot()
	if !reflect.DeepEqual(s.Playlists[0].Tracks, []string{"a.mp3", "c.mp3"}) {
		t.Errorf("after remove: %v", s.Playlists[0].Tracks)
	}
	if s.Playlists[1].Name != "Roadtrip 2" || s.Playlists[1].Folder != "Summer" {
		t.Errorf("update: %+v", s.Playlists[1])
	}

	if err := l.RenameFolder("Trips", "Summer"); err == nil {
		t.Error("renaming onto an existing folder accepted")
	}
	if err := l.RenameFolder("Trips", "Journeys"); err != nil {
		t.Fatal(err)
	}
	if l.Snapshot().Playlists[0].Folder != "Journeys" {
		t.Error("playlist did not follow its renamed folder")
	}

	// Deleting a folder must never delete the playlists in it.
	l.DeleteFolder("Journeys")
	s = l.Snapshot()
	if len(s.Playlists) != 2 || s.Playlists[0].Folder != "" {
		t.Errorf("after folder delete: %+v", s.Playlists)
	}
	l.DeletePlaylist(road)
	if len(l.Snapshot().Playlists) != 1 {
		t.Error("playlist not deleted")
	}
	if l.DeletePlaylist("nope") == nil || l.AddToPlaylist("nope", []string{"x"}) == nil {
		t.Error("unknown playlist id accepted")
	}
}

func TestListsPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "lists.json") // dir is created on demand
	l := OpenLists(path)
	id, _ := l.CreatePlaylist("Mix", "F")
	l.AddToPlaylist(id, []string{"x/y.mp3"})
	l.SetFavorite("x/y.mp3", true)

	got := OpenLists(path).Snapshot()
	if !reflect.DeepEqual(got, l.Snapshot()) {
		t.Errorf("reloaded %+v, want %+v", got, l.Snapshot())
	}
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Error("temp file left behind")
	}
}

func TestCorruptListsFileIsKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lists.json")
	os.WriteFile(path, []byte("{not json"), 0o644)
	l := OpenLists(path)
	if len(l.Snapshot().Playlists) != 0 {
		t.Fatal("expected empty lists")
	}
	if b, err := os.ReadFile(path + ".bad"); err != nil || string(b) != "{not json" {
		t.Errorf("corrupt file not preserved: %v %q", err, b)
	}
	l.SetFavorite("a.mp3", true) // and we can carry on
	if len(OpenLists(path).Snapshot().Favorites) != 1 {
		t.Error("could not save after recovering")
	}
}

func TestBulkFavoritesAndRemove(t *testing.T) {
	l := OpenLists("")
	l.SetFavorite("a.mp3", true)
	if err := l.SetFavorites([]string{"a.mp3", "b.mp3", "c.mp3"}, true); err != nil { // a already there
		t.Fatal(err)
	}
	if got := l.Snapshot().Favorites; !reflect.DeepEqual(got, []string{"a.mp3", "b.mp3", "c.mp3"}) {
		t.Fatalf("bulk add: %v", got)
	}
	l.SetFavorites([]string{"a.mp3", "c.mp3", "zzz.mp3"}, false)
	if got := l.Snapshot().Favorites; !reflect.DeepEqual(got, []string{"b.mp3"}) {
		t.Fatalf("bulk remove: %v", got)
	}
	if l.SetFavorites(nil, true) == nil || l.SetFavorites([]string{"a", ""}, true) == nil {
		t.Error("empty selection / blank id accepted")
	}

	id, _ := l.CreatePlaylist("P", "")
	l.AddToPlaylist(id, []string{"1", "2", "3", "4"})
	l.RemoveFromPlaylist(id, "2", "4", "nope")
	if got := l.Snapshot().Playlists[0].Tracks; !reflect.DeepEqual(got, []string{"1", "3"}) {
		t.Fatalf("bulk remove from playlist: %v", got)
	}
}

func TestSources(t *testing.T) {
	music, other, nested := t.TempDir(), t.TempDir(), ""
	os.MkdirAll(filepath.Join(music, "sub"), 0o755)
	nested = filepath.Join(music, "sub")
	path := filepath.Join(t.TempDir(), "sources.json")

	s := OpenSources(path, []string{music})
	if !reflect.DeepEqual(s.List(), []string{music}) {
		t.Fatalf("defaults: %v", s.List())
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("defaults were written to disk before any change")
	}
	if err := s.Add(other); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "/does/not/exist", filepath.Join(other, "nope"), music, nested, filepath.Dir(music)} {
		if s.Add(bad) == nil {
			t.Errorf("Add(%q) accepted", bad)
		}
	}
	// a change is remembered and beats the defaults on the next start
	got := OpenSources(path, []string{"/somewhere/else"}).List()
	if !reflect.DeepEqual(got, []string{music, other}) {
		t.Errorf("reloaded %v", got)
	}
	if err := s.Remove(music); err != nil || s.Remove(music) == nil {
		t.Errorf("remove: first must work, second must fail")
	}
	if !reflect.DeepEqual(OpenSources(path, nil).List(), []string{other}) {
		t.Error("removal not persisted")
	}
	// removing everything is allowed and stays empty (defaults must not come back)
	s.Remove(other)
	if l := OpenSources(path, []string{music}).List(); len(l) != 0 {
		t.Errorf("empty list came back as %v", l)
	}
}
