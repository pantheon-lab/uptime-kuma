package ui

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

const maxTagField = 200

func (s *Server) getTags(ctx context.Context) (reply, error) {
	tags, err := s.store.ListTags(ctx)
	if err != nil {
		return nil, err
	}
	if tags == nil {
		tags = []store.Tag{}
	}
	return ok("tags", tags), nil
}

func parseTag(args []json.RawMessage) (store.Tag, error) {
	var in struct {
		ID    flexInt `json:"id"`
		Name  string  `json:"name"`
		Color string  `json:"color"`
	}
	if err := arg(args, 0, &in); err != nil {
		return store.Tag{}, err
	}
	tag := store.Tag{ID: int32(in.ID.value), Name: strings.TrimSpace(in.Name), Color: strings.TrimSpace(in.Color)}
	switch {
	case tag.Name == "" || len(tag.Name) > maxTagField:
		return tag, errors.New("the tag's name must be 1 to 200 characters")
	case tag.Color == "" || len(tag.Color) > maxTagField:
		return tag, errors.New("the tag needs a colour")
	}
	return tag, nil
}

func (s *Server) addTag(ctx context.Context, args []json.RawMessage) (reply, error) {
	tag, err := parseTag(args)
	if err != nil {
		return nil, userErr(err)
	}
	created, err := s.store.CreateTag(ctx, tag.Name, tag.Color)
	if err != nil {
		return nil, err
	}
	return ok("tag", created), nil
}

// monitorsWithTag returns the monitors carrying a tag, whose list entries show its name and colour.
func (s *Server) monitorsWithTag(ctx context.Context, tagID int32) ([]int32, error) {
	all, err := s.store.AllMonitorTags(ctx)
	if err != nil {
		return nil, err
	}
	var ids []int32
	for monitorID, tags := range all {
		for _, tag := range tags {
			if tag.TagID == tagID {
				ids = append(ids, monitorID)
				break
			}
		}
	}
	return ids, nil
}

func (s *Server) editTag(ctx context.Context, args []json.RawMessage) (reply, error) {
	tag, err := parseTag(args)
	if err != nil {
		return nil, userErr(err)
	}
	if tag.ID <= 0 {
		return nil, userErr(errors.New("invalid id"))
	}
	if err := s.store.UpdateTag(ctx, tag); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return reply{"ok": false, "msg": "tagNotFound", "msgi18n": true}, nil
		}
		return nil, err
	}
	affected, err := s.monitorsWithTag(ctx, tag.ID)
	if err != nil {
		return nil, err
	}
	s.broadcastMonitors(ctx, affected...)
	return okI18n("Saved.", "tag", tag), nil
}

func (s *Server) deleteTag(ctx context.Context, args []json.RawMessage) (reply, error) {
	id, err := idArg(args, 0)
	if err != nil {
		return nil, userErr(err)
	}
	// Read first: afterwards, nothing says which monitors had it.
	affected, err := s.monitorsWithTag(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.store.DeleteTag(ctx, id); err != nil {
		return nil, err
	}
	s.broadcastMonitors(ctx, affected...)
	return okI18n("successDeleted"), nil
}

// changeMonitorTag runs an add, edit or delete of a tag on a monitor. All take the same arguments.
func (s *Server) changeMonitorTag(ctx context.Context, args []json.RawMessage,
	change func(context.Context, int32, int32, string) error, msg string,
) (reply, error) {
	tagID, err := idArg(args, 0)
	if err != nil {
		return nil, userErr(err)
	}
	monitorID, err := idArg(args, 1)
	if err != nil {
		return nil, userErr(err)
	}
	// A null value means none, and a number is possible from older clients.
	var value any
	if err := arg(args, 2, &value); err != nil {
		return nil, userErr(err)
	}
	text := ""
	switch v := value.(type) {
	case nil:
	case string:
		text = strings.TrimSpace(v)
	default:
		data, _ := json.Marshal(v)
		text = string(data)
	}
	if len(text) > maxTagField {
		return nil, userErr(errors.New("the tag's value must be at most 200 characters"))
	}
	if err := change(ctx, tagID, monitorID, text); err != nil {
		return nil, err
	}
	s.broadcastMonitors(ctx, monitorID)
	return okI18n(msg), nil
}
