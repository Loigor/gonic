package ctrladmin

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.senan.xyz/gonic/db"
	"go.senan.xyz/gonic/mockfs"
)

func TestServeShares(t *testing.T) {
	t.Parallel()
	m := mockfs.New(t)
	dbc := m.DB()
	var admin db.User
	require.NoError(t, dbc.Where("name=?", "admin").First(&admin).Error)

	expiry := time.Now().Add(-time.Hour)
	share := &db.Share{
		Token: "abcdefgh123456", UserID: admin.ID, Description: "d",
		VisitCount: 3, Downloadable: true, ExpiresAt: &expiry,
		Entries: []*db.ShareEntry{{Item: "tr-1"}, {Item: "tr-2"}},
	}
	require.NoError(t, dbc.Create(share).Error)

	c := &Controller{dbc: dbc}
	r := httptest.NewRequest(http.MethodGet, "/admin/shares", nil)
	r.Host = "gonic.example"
	resp := c.ServeShares(r)
	require.Empty(t, resp.err)
	require.Len(t, resp.data.AllShares, 1)

	v := resp.data.AllShares[0]
	require.Equal(t, "abcdefgh…", v.Token)
	require.Equal(t, "http://gonic.example/share/abcdefgh123456", v.URL)
	require.Equal(t, "admin", v.Owner)
	require.Equal(t, 2, v.Entries)
	require.Equal(t, 3, v.VisitCount)
	require.True(t, v.Downloadable)
	require.True(t, v.Expired)
}
