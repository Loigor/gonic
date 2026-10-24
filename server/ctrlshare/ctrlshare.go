// Package ctrlshare serves public, tokenized share links.
// it sits outside the subsonic auth chain and delegates media serving
// to the subsonic controller with a synthetic context.
package ctrlshare

import (
	"context"
	"errors"
	"fmt"
	"html"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/jinzhu/gorm"

	"go.senan.xyz/gonic/db"
	"go.senan.xyz/gonic/server/ctrlsubsonic"
	paramsp "go.senan.xyz/gonic/server/ctrlsubsonic/params"
	"go.senan.xyz/gonic/server/ctrlsubsonic/spec"
	"go.senan.xyz/gonic/server/ctrlsubsonic/specid"
)

const actionDownload = "download"

type Controller struct {
	dbc      *db.DB
	subsonic *ctrlsubsonic.Controller
}

func New(dbc *db.DB, subsonic *ctrlsubsonic.Controller) *Controller {
	return &Controller{dbc: dbc, subsonic: subsonic}
}

func (c *Controller) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	token := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	share, err := shareGetValid(c.dbc, token)
	if err != nil {
		http.Error(w, "invalid or expired share", http.StatusNotFound)
		return
	}
	// expiry is enforced on every request, not just when listing
	if share.ExpiresAt != nil && time.Now().After(*share.ExpiresAt) {
		http.Error(w, "share has expired", http.StatusGone)
		return
	}
	if share.User == nil || len(share.Entries) == 0 {
		http.Error(w, "invalid share", http.StatusNotFound)
		return
	}
	entries := share.GetEntries()

	switch action {
	case "":
		c.serveLanding(w, share, entries)
	case "stream", actionDownload, "cover":
		if action == actionDownload && !share.Downloadable {
			http.Error(w, "share is not downloadable", http.StatusForbidden)
			return
		}
		params := paramsp.New(r)
		id, err := params.GetID("id")
		if err != nil {
			http.Error(w, "please provide an `id` parameter", http.StatusBadRequest)
			return
		}
		if !entryAllowed(c.dbc, entries, id) {
			http.Error(w, "entry not in share", http.StatusForbidden)
			return
		}
		overrides := map[string]string{"id": id.String()}
		if action == actionDownload {
			overrides["format"] = "raw"
			setDownloadDisposition(w, c.dbc, id)
		}
		switch action {
		case "cover":
			c.serveSubsonic(w, r, share.User, overrides, c.subsonic.ServeGetCoverArt)
		default:
			c.serveSubsonic(w, r, share.User, overrides, c.subsonic.ServeStream)
		}
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// setDownloadDisposition hints browsers to save the file instead of playing it inline.
func setDownloadDisposition(w http.ResponseWriter, dbc *db.DB, id specid.ID) {
	var name string
	switch id.Type {
	case specid.Track:
		var tr db.Track
		if err := dbc.Select("filename, tag_title, tag_track_artist").First(&tr, id.Value).Error; err == nil {
			name = sanitizeDownloadName(tr.Filename)
			if name == "" {
				name = sanitizeDownloadName(strings.TrimSpace(tr.TagTrackArtist + " - " + tr.TagTitle))
			}
		}
	case specid.PodcastEpisode:
		var pe db.PodcastEpisode
		if err := dbc.Select("title").First(&pe, id.Value).Error; err == nil {
			name = sanitizeDownloadName(pe.Title)
		}
	}
	if name == "" {
		return
	}
	if cd := mime.FormatMediaType("attachment", map[string]string{"filename": name}); cd != "" {
		w.Header().Set("Content-Disposition", cd)
	}
}

// sanitizeDownloadName strips path separators and non-printable characters so
// the name is safe in a Content-Disposition header.
func sanitizeDownloadName(name string) string {
	name = strings.TrimSpace(filepath.Base(name))
	if name == "." || name == string(filepath.Separator) {
		return ""
	}
	var b strings.Builder
	for _, r := range name {
		if r > 0x1f && r != 0x7f && r != '/' && r != '\\' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// entryAllowed allows exact entries plus tracks inside shared albums.
func entryAllowed(dbc *db.DB, entries []specid.ID, id specid.ID) bool {
	for _, entry := range entries {
		if entry == id {
			return true
		}
		if id.Type == specid.Track && entry.Type == specid.Album {
			var tr db.Track
			if err := dbc.Select("album_id").First(&tr, id.Value).Error; err != nil {
				continue
			}
			if tr.AlbumID == entry.Value {
				return true
			}
		}
	}
	return false
}

func shareGetValid(dbc *db.DB, token string) (*db.Share, error) {
	var share db.Share
	err := dbc.
		Preload("Entries").
		Preload("User").
		Where("token=?", token).
		First(&share).
		Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("share not found")
	}
	if err != nil {
		return nil, fmt.Errorf("find share: %w", err)
	}
	return &share, nil
}

func (c *Controller) serveSubsonic(w http.ResponseWriter, r *http.Request, owner *db.User, paramOverrides map[string]string, h func(http.ResponseWriter, *http.Request) *spec.Response) {
	q := r.URL.Query()
	for key, value := range paramOverrides {
		q.Set(key, value)
	}
	r2 := r.Clone(r.Context())
	r2.URL.RawQuery = q.Encode()

	ctx := r2.Context()
	ctx = context.WithValue(ctx, ctrlsubsonic.CtxParams, paramsp.Params(q))
	ctx = context.WithValue(ctx, ctrlsubsonic.CtxUser, owner)
	r2 = r2.WithContext(ctx)

	// no scrobble, no play count: ServeStream only serves bytes
	if resp := h(w, r2); resp != nil && resp.Error != nil {
		http.Error(w, resp.Error.Message, http.StatusInternalServerError)
	}
}

type shareItem struct {
	id     string
	title  string
	artist string
	album  string
}

func shareItems(dbc *db.DB, entries []specid.ID) []shareItem {
	var items []shareItem
	for _, id := range entries {
		switch id.Type {
		case specid.Track:
			var tr db.Track
			if err := dbc.Preload("Album").First(&tr, id.Value).Error; err != nil {
				continue
			}
			items = append(items, shareItem{
				id: id.String(), title: tr.TagTitle,
				artist: tr.TagTrackArtist, album: tr.Album.TagTitle,
			})
		case specid.Album:
			// streaming only works per track, so expand the album into its tracks
			var tracks []*db.Track
			if err := dbc.
				Preload("Album").
				Where("album_id=?", id.Value).
				Order("tag_disc_number, tag_track_number").
				Find(&tracks).Error; err != nil {
				continue
			}
			for _, tr := range tracks {
				items = append(items, shareItem{
					id: specid.ID{Type: specid.Track, Value: tr.ID}.String(), title: tr.TagTitle,
					artist: tr.TagTrackArtist, album: tr.Album.TagTitle,
				})
			}
		case specid.PodcastEpisode:
			var pe db.PodcastEpisode
			if err := dbc.First(&pe, id.Value).Error; err != nil {
				continue
			}
			items = append(items, shareItem{
				id: id.String(), title: pe.Title,
				artist: "podcast", album: pe.Title,
			})
		}
	}
	return items
}

func (c *Controller) serveLanding(w http.ResponseWriter, share *db.Share, entries []specid.ID) {
	_ = c.dbc.Model(share).Updates(map[string]any{
		"visit_count":     share.VisitCount + 1,
		"last_visited_at": time.Now(),
	})

	token := share.Token
	var b strings.Builder
	b.WriteString("<!DOCTYPE html><html><head><meta charset='utf-8'><meta name='viewport' content='width=device-width, initial-scale=1'>")
	b.WriteString("<title>Shared music</title><style>body{font-family:system-ui,sans-serif;background:#141414;color:#e6e6e6;max-width:720px;margin:0 auto;padding:2rem 1rem}h1{font-size:1.25rem}li{margin:.8rem 0;list-style:none}small{color:#999}audio{display:block;margin-top:.3rem;width:100%}a{color:#8ab4f8}</style></head><body>")
	if share.Description != "" {
		fmt.Fprintf(&b, "<h1>%s</h1>", html.EscapeString(share.Description))
	} else {
		b.WriteString("<h1>Shared music</h1>")
	}
	fmt.Fprintf(&b, "<small>shared by %s</small><ul>", html.EscapeString(share.User.Name))
	for _, item := range shareItems(c.dbc, entries) {
		fmt.Fprintf(&b,
			"<li><strong>%s</strong> &mdash; %s <small>(%s)</small>"+
				"<audio controls preload='none' src='%s/stream?id=%s'></audio>",
			html.EscapeString(item.title), html.EscapeString(item.artist), html.EscapeString(item.album),
			html.EscapeString(token), item.id)
		if share.Downloadable {
			fmt.Fprintf(&b, " <a href='%s/download?id=%s' download>download</a>", html.EscapeString(token), item.id)
		}
		b.WriteString("</li>")
	}
	if share.ExpiresAt != nil {
		fmt.Fprintf(&b, "</ul><small>expires %s</small>", share.ExpiresAt.Format(time.RFC1123))
	}
	b.WriteString("</body></html>")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}
