package app

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// listReq is the JSON body every list/queue endpoint accepts; each endpoint
// reads only the fields it needs. name/folder are pointers so "leave as is" and
// "set to empty" can be told apart.
type listReq struct {
	ID      string   `json:"id"`
	Name    *string  `json:"name"`
	Folder  *string  `json:"folder"`
	To      string   `json:"to"`
	Track   string   `json:"track"`
	Tracks  []string `json:"tracks"` // nil = absent; [] = explicitly empty
	On      bool     `json:"on"`
	Index   int      `json:"index"`
	Context string   `json:"context"`
}

// all is track plus tracks: endpoints take one track or many (bulk actions).
func (q listReq) all() []string {
	if q.Track == "" {
		return q.Tracks
	}
	return append([]string{q.Track}, q.Tracks...)
}

func (q listReq) name() string {
	if q.Name == nil {
		return ""
	}
	return *q.Name
}

func (q listReq) folder() string {
	if q.Folder == nil {
		return ""
	}
	return *q.Folder
}

// listRoutes wires favourites, playlists, folders and "play this queue".
//
//	GET  /api/lists                                       everything the user curated, plus mostPlayed
//	POST /api/fav {track|tracks,on}                       favourite / unfavourite one or many
//	POST /api/playlist {name,folder}                      -> {"id":...}
//	POST /api/playlist/update {id,name?,folder?}          rename / move
//	POST /api/playlist/delete {id}
//	POST /api/playlist/add {id,tracks}
//	POST /api/playlist/remove {id,track|tracks}
//	POST /api/folder {name}   /api/folder/rename {name,to}   /api/folder/delete {name}
//	GET/POST /api/sources {add|remove}                    music folders (POST rescans)
//	POST /api/queue {tracks?,index,context}               play; no tracks = whole library
func (n *Node) listRoutes(mux *http.ServeMux) {
	do := func(f func(q listReq) (any, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "POST only", http.StatusMethodNotAllowed)
				return
			}
			var q listReq
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&q); err != nil && !errors.Is(err, io.EOF) {
				http.Error(w, "bad json", http.StatusBadRequest)
				return
			}
			out, err := f(q)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if out == nil {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			json.NewEncoder(w).Encode(out)
		}
	}
	L := n.lists
	mux.HandleFunc("/api/lists", func(w http.ResponseWriter, r *http.Request) {
		files := n.p.Files()
		top, counts := n.plays.Top(100, func(t string) bool { _, ok := files[t]; return ok })
		json.NewEncoder(w).Encode(struct {
			*Lists
			MostPlayed []string       `json:"mostPlayed"` // the built-in "Most Played" playlist, best first
			PlayCounts map[string]int `json:"playCounts"` // how many times each of those was played
		}{L.Snapshot(), top, counts})
	})
	mux.HandleFunc("/api/fav", do(func(q listReq) (any, error) { return nil, L.SetFavorites(q.all(), q.On) }))
	mux.HandleFunc("/api/playlist", do(func(q listReq) (any, error) {
		id, err := L.CreatePlaylist(q.name(), q.folder())
		if err != nil {
			return nil, err
		}
		return map[string]string{"id": id}, nil
	}))
	mux.HandleFunc("/api/playlist/update", do(func(q listReq) (any, error) {
		return nil, L.UpdatePlaylist(q.ID, q.Name, q.Folder)
	}))
	mux.HandleFunc("/api/playlist/delete", do(func(q listReq) (any, error) { return nil, L.DeletePlaylist(q.ID) }))
	mux.HandleFunc("/api/playlist/add", do(func(q listReq) (any, error) { return nil, L.AddToPlaylist(q.ID, q.Tracks) }))
	mux.HandleFunc("/api/playlist/remove", do(func(q listReq) (any, error) { return nil, L.RemoveFromPlaylist(q.ID, q.all()...) }))
	mux.HandleFunc("/api/folder", do(func(q listReq) (any, error) { return nil, L.CreateFolder(q.name()) }))
	mux.HandleFunc("/api/folder/rename", do(func(q listReq) (any, error) { return nil, L.RenameFolder(q.name(), q.To) }))
	mux.HandleFunc("/api/folder/delete", do(func(q listReq) (any, error) { return nil, L.DeleteFolder(q.name()) }))
	// Music folders: GET lists them, POST {"add":dir} / {"remove":dir} changes them and rescans.
	mux.HandleFunc("/api/sources", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var q struct{ Add, Remove string }
			if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&q) != nil {
				http.Error(w, "bad json", http.StatusBadRequest)
				return
			}
			var err error
			if q.Add != "" {
				err = n.sources.Add(q.Add)
			} else {
				err = n.sources.Remove(q.Remove)
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			n.p.SetRoots(n.sources.List())
			n.meta.Scan(n.p.Files())
		}
		json.NewEncoder(w).Encode(map[string]any{"dirs": n.sources.List()})
	})
	mux.HandleFunc("/api/queue", do(func(q listReq) (any, error) {
		if !n.p.SetQueue(q.Tracks, q.Index, q.Context) {
			return nil, errors.New("nothing playable in that queue")
		}
		n.kickSupervisor()
		return nil, nil
	}))
}
