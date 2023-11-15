package automod

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	appbsky "github.com/bluesky-social/indigo/api/bsky"
	"github.com/bluesky-social/indigo/atproto/identity"
	"github.com/bluesky-social/indigo/atproto/syntax"
	"github.com/bluesky-social/indigo/xrpc"
)

// runtime for executing rules, managing state, and recording moderation actions.
//
// TODO: careful when initializing: several fields should not be null or zero, even though they are pointer type.
type Engine struct {
	Logger    *slog.Logger
	Directory identity.Directory
	Rules     RuleSet
	Counters  CountStore
	Sets      SetStore
	// used to persist moderation actions in mod service (optional)
	AdminClient *xrpc.Client
}

func (e *Engine) ProcessIdentityEvent(ctx context.Context, t string, did syntax.DID) error {
	// similar to an HTTP server, we want to recover any panics from rule execution
	defer func() {
		if r := recover(); r != nil {
			e.Logger.Error("automod event execution exception", "err", r)
		}
	}()

	ident, err := e.Directory.LookupDID(ctx, did)
	if err != nil {
		return fmt.Errorf("resolving identity: %w", err)
	}
	if ident == nil {
		return fmt.Errorf("identity not found for did: %s", did.String())
	}

	evt := IdentityEvent{
		Event{
			Engine:  e,
			Account: AccountMeta{Identity: ident},
		},
	}
	if err := e.Rules.CallIdentityRules(&evt); err != nil {
		return err
	}
	if evt.Err != nil {
		return evt.Err
	}
	evt.CanonicalLogLine()
	if err := evt.PersistAccountActions(ctx); err != nil {
		return err
	}
	if err := evt.PersistCounters(ctx); err != nil {
		return err
	}
	return nil
}

func (e *Engine) ProcessRecord(ctx context.Context, did syntax.DID, path, recCID string, rec any) error {
	// similar to an HTTP server, we want to recover any panics from rule execution
	defer func() {
		if r := recover(); r != nil {
			e.Logger.Error("automod event execution exception", "err", r)
		}
	}()

	ident, err := e.Directory.LookupDID(ctx, did)
	if err != nil {
		return fmt.Errorf("resolving identity: %w", err)
	}
	if ident == nil {
		return fmt.Errorf("identity not found for did: %s", did.String())
	}
	collection := strings.SplitN(path, "/", 2)[0]

	switch collection {
	case "app.bsky.feed.post":
		post, ok := rec.(*appbsky.FeedPost)
		if !ok {
			return fmt.Errorf("mismatch between collection (%s) and type", collection)
		}
		evt := e.NewPostEvent(ident, path, recCID, post)
		e.Logger.Debug("processing post", "did", ident.DID, "path", path)
		if err := e.Rules.CallPostRules(&evt); err != nil {
			return err
		}
		if evt.Err != nil {
			return evt.Err
		}
		evt.CanonicalLogLine()
		if err := evt.PersistAccountActions(ctx); err != nil {
			return err
		}
		if err := evt.PersistCounters(ctx); err != nil {
			return err
		}
	default:
		evt := e.NewRecordEvent(ident, path, recCID, rec)
		e.Logger.Debug("processing record", "did", ident.DID, "path", path)
		if err := e.Rules.CallRecordRules(&evt); err != nil {
			return err
		}
		if evt.Err != nil {
			return evt.Err
		}
		evt.CanonicalLogLine()
		if err := evt.PersistAccountActions(ctx); err != nil {
			return err
		}
		if err := evt.PersistCounters(ctx); err != nil {
			return err
		}
	}

	return nil
}

func (e *Engine) NewPostEvent(ident *identity.Identity, path, recCID string, post *appbsky.FeedPost) PostEvent {
	parts := strings.SplitN(path, "/", 2)
	return PostEvent{
		RecordEvent{
			Event{
				Engine:  e,
				Logger:  e.Logger.With("did", ident.DID, "collection", parts[0], "rkey", parts[1]),
				Account: AccountMeta{Identity: ident},
			},
			parts[0],
			parts[1],
			recCID,
			[]string{},
			false,
			[]ModReport{},
			[]string{},
		},
		post,
	}
}

func (e *Engine) NewRecordEvent(ident *identity.Identity, path, recCID string, rec any) RecordEvent {
	parts := strings.SplitN(path, "/", 2)
	return RecordEvent{
		Event{
			Engine:  e,
			Logger:  e.Logger.With("did", ident.DID, "collection", parts[0], "rkey", parts[1]),
			Account: AccountMeta{Identity: ident},
		},
		parts[0],
		parts[1],
		recCID,
		[]string{},
		false,
		[]ModReport{},
		[]string{},
	}
}

func (e *Engine) GetCount(key, period string) (int, error) {
	return e.Counters.GetCount(context.TODO(), key, period)
}

// checks if `val` is an element of set `name`
func (e *Engine) InSet(name, val string) (bool, error) {
	return e.Sets.InSet(context.TODO(), name, val)
}
