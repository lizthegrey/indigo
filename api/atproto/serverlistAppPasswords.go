// Code generated by cmd/lexgen (see Makefile's lexgen); DO NOT EDIT.

package atproto

import (
	"context"

	"github.com/bluesky-social/indigo/xrpc"
)

// schema: com.atproto.server.listAppPasswords

type ServerListAppPasswords_AppPassword struct {
	CreatedAt string `json:"createdAt" cborgen:"createdAt"`
	Name      string `json:"name" cborgen:"name"`
}

type ServerListAppPasswords_Output struct {
	Passwords []*ServerListAppPasswords_AppPassword `json:"passwords" cborgen:"passwords"`
}

func ServerListAppPasswords(ctx context.Context, c *xrpc.Client) (*ServerListAppPasswords_Output, error) {
	var out ServerListAppPasswords_Output
	if err := c.Do(ctx, xrpc.Query, "", "com.atproto.server.listAppPasswords", nil, nil, &out); err != nil {
		return nil, err
	}

	return &out, nil
}