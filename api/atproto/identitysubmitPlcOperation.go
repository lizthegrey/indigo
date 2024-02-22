// Code generated by cmd/lexgen (see Makefile's lexgen); DO NOT EDIT.

package atproto

// schema: com.atproto.identity.submitPlcOperation

import (
	"context"

	"github.com/bluesky-social/indigo/lex/util"
	"github.com/bluesky-social/indigo/xrpc"
)

// IdentitySubmitPlcOperation_Input is the input argument to a com.atproto.identity.submitPlcOperation call.
type IdentitySubmitPlcOperation_Input struct {
	Operation *util.LexiconTypeDecoder `json:"operation" cborgen:"operation"`
}

// IdentitySubmitPlcOperation calls the XRPC method "com.atproto.identity.submitPlcOperation".
func IdentitySubmitPlcOperation(ctx context.Context, c *xrpc.Client, input *IdentitySubmitPlcOperation_Input) error {
	if err := c.Do(ctx, xrpc.Procedure, "application/json", "com.atproto.identity.submitPlcOperation", nil, input, nil); err != nil {
		return err
	}

	return nil
}