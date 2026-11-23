package ctrlsubsonic

import (
	"net/url"
	"strings"
	"testing"
	"testing/synctest"
)

func TestShares(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seq = true

	// deterministic tokens for golden files
	tokens := []string{"testtoken1", "testtoken2"}
	tokenIdx := 0
	origTokenFunc := shareTokenFunc
	shareTokenFunc = func() (string, error) {
		defer func() { tokenIdx++ }()
		return tokens[tokenIdx], nil
	}
	defer func() { shareTokenFunc = origTokenFunc }()

	// bubble pins gorm's auto-stamped CreatedAt/UpdatedAt
	synctest.Test(t, func(*testing.T) {
		f.query(t, f.contr.ServeCreateShare, f.admin, url.Values{
			"id":          {f.trackAB1.SID().String(), f.albumAB.SID().String()},
			"description": {"a nice share"},
		})
	})
	f.run(t, f.contr.ServeGetShares, f.admin,
		query{url.Values{}, "admin_after_create", false},
	)
	// alt owns no shares, and must not see admin's
	f.run(t, f.contr.ServeGetShares, f.alt,
		query{url.Values{}, "alt_after_create", false},
	)

	f.query(t, f.contr.ServeUpdateShare, f.admin, url.Values{
		"id":           {"testtoken1"},
		"description":  {"an updated share"},
		"downloadable": {"true"},
	})
	f.run(t, f.contr.ServeGetShares, f.admin,
		query{url.Values{}, "admin_after_update", false},
	)

	// alt can't update or delete admin's share
	body := f.query(t, f.contr.ServeUpdateShare, f.alt, url.Values{
		"id":          {"testtoken1"},
		"description": {"hijacked"},
	})
	if !strings.Contains(body, "aren't allowed") {
		t.Fatalf("expected auth error, got: %s", body)
	}
	body = f.query(t, f.contr.ServeDeleteShare, f.alt, url.Values{
		"id": {"testtoken1"},
	})
	if !strings.Contains(body, "aren't allowed") {
		t.Fatalf("expected auth error, got: %s", body)
	}

	// alt creates their own share
	synctest.Test(t, func(*testing.T) {
		f.query(t, f.contr.ServeCreateShare, f.alt, url.Values{
			"id": {f.trackVA0.SID().String()},
		})
	})
	// admin (as IsAdmin) can see both
	f.run(t, f.contr.ServeGetShares, f.admin,
		query{url.Values{}, "admin_after_alt_create", true},
	)

	// deleting the album share leaves alt's intact
	f.query(t, f.contr.ServeDeleteShare, f.admin, url.Values{"id": {"testtoken1"}})
	f.run(t, f.contr.ServeGetShares, f.admin,
		query{url.Values{}, "admin_after_delete", false},
	)
	f.run(t, f.contr.ServeGetShares, f.alt,
		query{url.Values{}, "alt_after_admin_delete", false},
	)
}
