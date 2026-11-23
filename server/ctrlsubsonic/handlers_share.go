package ctrlsubsonic

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jinzhu/gorm"

	"go.senan.xyz/gonic/db"
	paramsp "go.senan.xyz/gonic/server/ctrlsubsonic/params"
	"go.senan.xyz/gonic/server/ctrlsubsonic/spec"
	"go.senan.xyz/gonic/server/ctrlsubsonic/specid"
)

//nolint:gochecknoglobals
var shareTokenFunc = func() (string, error) {
	buf := make([]byte, 10)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random bytes: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func shareGet(dbc *db.DB, token string) (*db.Share, error) {
	var share db.Share
	err := dbc.
		Preload("Entries").
		Preload("User").
		Where("token=?", token).
		First(&share).
		Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find share: %w", err)
	}
	return &share, nil
}

func shareUserIsOwner(share *db.Share, user *db.User) bool {
	return share.UserID == user.ID || user.IsAdmin
}

func (c *Controller) ServeCreateShare(r *http.Request) *spec.Response {
	user := r.Context().Value(CtxUser).(*db.User)
	params := r.Context().Value(CtxParams).(paramsp.Params)
	ids, err := params.GetIDList("id")
	if err != nil {
		return spec.NewError(10, "please provide an `id` parameter")
	}

	share := &db.Share{
		UserID:       user.ID,
		Description:  params.GetOr("description", ""),
		Downloadable: params.GetOrBool("downloadable", false),
		ExpiresAt:    parseShareExpires(params.GetOr("expires", "")),
	}
	for _, id := range ids {
		switch id.Type {
		case specid.Track, specid.Album, specid.PodcastEpisode:
		default:
			return spec.NewError(0, "shares can only contain tracks, albums or podcast episodes, got %q", id.Type)
		}
		share.Entries = append(share.Entries, &db.ShareEntry{Item: id.String()})
	}

	for range 5 {
		token, err := shareTokenFunc()
		if err != nil {
			return spec.NewError(0, "generate token: %v", err)
		}
		share.Token = token
		if err := c.dbc.Create(share).Error; err == nil {
			break
		}
	}
	if share.ID == 0 {
		return spec.NewError(0, "save share")
	}

	if share, err = shareGet(c.dbc, share.Token); err != nil || share == nil {
		return spec.NewError(0, "load share: %v", err)
	}
	sub := spec.NewResponse()
	sub.Shares = &spec.Shares{List: []*spec.Share{}}
	rendered, err := c.shareRender(params, share)
	if err != nil {
		return spec.NewError(0, "render share: %v", err)
	}
	sub.Shares.List = append(sub.Shares.List, rendered)
	return sub
}

func (c *Controller) ServeGetShares(r *http.Request) *spec.Response {
	user := r.Context().Value(CtxUser).(*db.User)
	params := r.Context().Value(CtxParams).(paramsp.Params)

	q := c.dbc.
		Preload("Entries").
		Preload("User").
		Order("created_at")
	if !user.IsAdmin {
		q = q.Where("user_id=?", user.ID)
	}
	var shares []*db.Share
	if err := q.Find(&shares).Error; err != nil {
		return spec.NewError(0, "find shares: %v", err)
	}

	sub := spec.NewResponse()
	sub.Shares = &spec.Shares{List: []*spec.Share{}}
	for _, share := range shares {
		rendered, err := c.shareRender(params, share)
		if err != nil {
			return spec.NewError(0, "render share: %v", err)
		}
		sub.Shares.List = append(sub.Shares.List, rendered)
	}
	return sub
}

func (c *Controller) ServeUpdateShare(r *http.Request) *spec.Response {
	user := r.Context().Value(CtxUser).(*db.User)
	params := r.Context().Value(CtxParams).(paramsp.Params)
	token, err := params.Get("id")
	if err != nil {
		return spec.NewError(10, "please provide an `id` parameter")
	}
	share, err := shareGet(c.dbc, token)
	if err != nil {
		return spec.NewError(0, "find share: %v", err)
	}
	if share == nil {
		return spec.NewError(70, "share with id %s not found", token)
	}
	if !shareUserIsOwner(share, user) {
		return spec.NewError(50, "you aren't allowed to update that share")
	}
	if _, err := params.Get("description"); err == nil {
		share.Description = params.GetOr("description", "")
	}
	if expires := params.GetOr("expires", ""); expires != "" {
		share.ExpiresAt = parseShareExpires(expires)
	}
	if downloadable, err := params.GetBool("downloadable"); err == nil {
		share.Downloadable = downloadable
	}
	if err := c.dbc.Save(share).Error; err != nil {
		return spec.NewError(0, "save share: %v", err)
	}
	sub := spec.NewResponse()
	sub.Shares = &spec.Shares{List: []*spec.Share{}}
	rendered, err := c.shareRender(params, share)
	if err != nil {
		return spec.NewError(0, "render share: %v", err)
	}
	sub.Shares.List = append(sub.Shares.List, rendered)
	return sub
}

func (c *Controller) ServeDeleteShare(r *http.Request) *spec.Response {
	user := r.Context().Value(CtxUser).(*db.User)
	params := r.Context().Value(CtxParams).(paramsp.Params)
	token, err := params.Get("id")
	if err != nil {
		return spec.NewError(10, "please provide an `id` parameter")
	}
	share, err := shareGet(c.dbc, token)
	if err != nil {
		return spec.NewError(0, "find share: %v", err)
	}
	if share == nil {
		return spec.NewError(70, "share with id %s not found", token)
	}
	if !shareUserIsOwner(share, user) {
		return spec.NewError(50, "you aren't allowed to delete that share")
	}
	if err := c.dbc.Delete(share).Error; err != nil {
		return spec.NewError(0, "delete share: %v", err)
	}
	return spec.NewResponse()
}

//nolint:gocyclo
func (c *Controller) shareRender(params paramsp.Params, share *db.Share) (*spec.Share, error) {
	user := share.User
	if user == nil {
		user = &db.User{}
	}
	client := params.GetOr("c", "")
	transcodeMeta := streamGetTranscodeMeta(c.dbc, user.ID, client)

	ret := &spec.Share{
		ID:           share.Token,
		URL:          c.resolveProxyPath("/share/" + share.Token),
		Description:  share.Description,
		Username:     user.Name,
		Created:      share.CreatedAt,
		VisitCount:   share.VisitCount,
		LastVisited:  share.LastVisitedAt,
		Expires:      share.ExpiresAt,
		Downloadable: share.Downloadable,
		Entries:      []*spec.TrackChild{},
	}

	var trackIDs []int
	var albumIDs []int
	var peIDs []int
	for _, id := range share.GetEntries() {
		switch id.Type {
		case specid.Track:
			trackIDs = append(trackIDs, id.Value)
		case specid.Album:
			albumIDs = append(albumIDs, id.Value)
		case specid.PodcastEpisode:
			peIDs = append(peIDs, id.Value)
		}
	}

	var tracks []*spec.TrackRow
	q := c.dbc.Scopes(spec.LoadTrackByTags(user.ID))
	switch {
	case len(trackIDs) > 0 && len(albumIDs) > 0:
		q = q.Where("tracks.id IN (?) OR tracks.album_id IN (?)", trackIDs, albumIDs)
	case len(trackIDs) > 0:
		q = q.Where("tracks.id IN (?)", trackIDs)
	case len(albumIDs) > 0:
		q = q.Where("tracks.album_id IN (?)", albumIDs)
	default:
		return ret, nil
	}
	if err := q.
		Order("tracks.album_id, tracks.tag_disc_number, tracks.tag_track_number").
		Find(&tracks).Error; err != nil {
		return nil, fmt.Errorf("find share tracks: %w", err)
	}
	for _, t := range tracks {
		trch := spec.NewTrackByTags(client, t, t.Album)
		trch.TranscodeMeta = transcodeMeta
		ret.Entries = append(ret.Entries, trch)
	}

	for _, peID := range peIDs {
		var pe db.PodcastEpisode
		if err := c.dbc.Preload("Podcast").First(&pe, peID).Error; err != nil {
			continue
		}
		ret.Entries = append(ret.Entries, spec.NewTCPodcastEpisode(&pe))
	}
	return ret, nil
}

func parseShareExpires(in string) *time.Time {
	if in == "" {
		return nil
	}
	if ms, err := strconv.Atoi(in); err == nil {
		t := time.Unix(0, int64(ms)*1e6)
		return &t
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, in); err == nil {
			return &t
		}
	}
	return nil
}
