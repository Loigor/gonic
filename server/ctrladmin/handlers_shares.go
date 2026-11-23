package ctrladmin

import (
	"fmt"
	"net/http"
	"time"

	"go.senan.xyz/gonic/db"
	"go.senan.xyz/gonic/handlerutil"
)

// shareView is a read-only view of a share for the admin UI.
type shareView struct {
	Token string
	// URL is the full public share URL for the current request host.
	URL string
	// Owner is empty if the share's user was deleted.
	Owner        string
	Description  string
	Entries      int
	VisitCount   int
	LastVisited  time.Time
	Expires      time.Time
	Expired      bool
	Downloadable bool
}

func (c *Controller) ServeShares(r *http.Request) *Response {
	var shares []*db.Share
	if err := c.dbc.Preload("Entries").Preload("User").Find(&shares).Error; err != nil {
		return &Response{code: 500, err: fmt.Sprintf("error finding shares: %v", err)}
	}

	baseURL := handlerutil.BaseURL(r)
	views := make([]*shareView, 0, len(shares))
	for _, share := range shares {
		view := &shareView{
			Token:        shortToken(share.Token),
			URL:          fmt.Sprintf("%s/share/%s", baseURL, share.Token),
			Description:  share.Description,
			Entries:      len(share.Entries),
			VisitCount:   share.VisitCount,
			Downloadable: share.Downloadable,
		}
		if share.User != nil {
			view.Owner = share.User.Name
		}
		if share.LastVisitedAt != nil {
			view.LastVisited = *share.LastVisitedAt
		}
		if share.ExpiresAt != nil {
			view.Expires = *share.ExpiresAt
			view.Expired = time.Now().After(*share.ExpiresAt)
		}
		views = append(views, view)
	}
	return &Response{template: "shares.tmpl", data: &templateData{AllShares: views}}
}

func shortToken(token string) string {
	if len(token) > 8 {
		return token[:8] + "…"
	}
	return token
}
