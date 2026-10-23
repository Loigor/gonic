package ctrladmin

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"go.senan.xyz/gonic/db"
)

// authenticateProxyUser resolves a user from reverse-proxy auth headers.
// When authHeader is configured and present, the user is looked up (or created)
// and IsAdmin is synced from group membership. Returns nil, nil when proxy auth
// is disabled or the username header is absent.
func (c *Controller) authenticateProxyUser(r *http.Request) (*db.User, error) {
	if c.authHeader == "" {
		return nil, nil
	}
	username := strings.TrimSpace(r.Header.Get(c.authHeader))
	if username == "" {
		return nil, nil
	}

	user, err := c.dbc.GetUserByName(username)
	if err != nil {
		return nil, fmt.Errorf("find user by name: %w", err)
	}
	if user == nil {
		password, err := randomPassword()
		if err != nil {
			return nil, fmt.Errorf("generate password: %w", err)
		}
		user = &db.User{
			Name:     username,
			Password: password,
		}
		if err := c.dbc.Create(user).Error; err != nil {
			return nil, fmt.Errorf("create user %q: %w", username, err)
		}
	}

	if err := c.syncProxyAdmin(r, user); err != nil {
		return nil, err
	}
	return user, nil
}

func (c *Controller) syncProxyAdmin(r *http.Request, user *db.User) error {
	if c.authAdminGroup == "" || c.authGroupsHeader == "" {
		return nil
	}
	groups := parseGroups(r.Header.Get(c.authGroupsHeader))
	isAdmin := slices.Contains(groups, c.authAdminGroup)
	if user.IsAdmin == isAdmin {
		return nil
	}
	user.IsAdmin = isAdmin
	if err := c.dbc.Save(user).Error; err != nil {
		return fmt.Errorf("sync admin for %q: %w", user.Name, err)
	}
	return nil
}

func parseGroups(raw string) []string {
	if raw == "" {
		return nil
	}
	raw = strings.ReplaceAll(raw, "|", ",")
	var groups []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			groups = append(groups, part)
		}
	}
	return groups
}

func randomPassword() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
