package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The Home Assistant connector reads a home's devices and acts on them
// through Home Assistant's REST API, at the installation's own URL, which
// the admin who installs it gives, with a long-lived access token of a
// Home Assistant user: flows see and control what that user does, so a
// user of its own, with only what flows need, is best.

var homeAssistantConnector = &connector{
	Name:     "homeassistant",
	Title:    "Home Assistant",
	Category: "Home",
	Description: "Home Assistant's lights, sensors, switches and other entities: read their states and history, " +
		"and call services that act on them, such as turning a light on.",
	URLExample: "http://homeassistant.local:8123",
	TokenHelp: "A long-lived access token, made in Home Assistant under your profile, Security. Flows see and " +
		"control what its user does, so give them a user of their own.",
	Operations: []operation{
		{Name: "list_states", Title: "List entities", Description: "Lists entities with their states, of one domain, such as light or sensor, or all of them.",
			Params: []param{{Name: "domain", Type: paramString, Pattern: haDomain, Description: "Only entities of this domain, such as light, switch or sensor"},
				{Name: "limit", Type: paramInteger, Description: "How many to list, at most 500; 100 when left out"}},
			run: haListStates},
		{Name: "get_state", Title: "Get an entity's state", Description: "Gets an entity's state and attributes.",
			Params: []param{haEntity}, run: haGetState},
		{Name: "get_history", Title: "Get an entity's history", Description: "Gets the states an entity was in, oldest first.",
			Params: []param{haEntity, {Name: "since", Type: paramString, Description: "From this time, as 2026-01-31T00:00:00Z; a day ago when left out"}},
			run:    haGetHistory},

		{Name: "call_service", Title: "Call a service", Description: "Calls a service, such as light.turn_on, on entities, and lists the states it changed.", Write: true,
			Params: []param{{Name: "domain", Type: paramString, Required: true, Pattern: haDomain, Description: "The service's domain, such as light"},
				{Name: "service", Type: paramString, Required: true, Pattern: haDomain, Description: "The service, such as turn_on"},
				{Name: "entity_id", Type: paramStrings, Description: "The entities it acts on, such as light.kitchen"},
				{Name: "data", Type: paramString, Description: `What else the service takes, as a JSON object, such as {"brightness_pct": 50}`}},
			run: haCallService},
	},
}

var (
	haDomain        = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
	haEntityPattern = regexp.MustCompile(`^[a-z0-9_]{1,64}\.[a-z0-9_]{1,255}$`)
	haEntity        = param{Name: "entity_id", Type: paramString, Required: true, Pattern: haEntityPattern, Description: "The entity, such as sensor.living_room_temperature"}
)

type haStateJSON struct {
	EntityID    string         `json:"entity_id"`
	State       string         `json:"state"`
	Attributes  map[string]any `json:"attributes"`
	LastChanged string         `json:"last_changed"`
}

// An entity's state, with key, its entity id, telling it apart as a
// shared record.
type haState struct {
	Key         string         `json:"key"`
	EntityID    string         `json:"entityId"`
	Name        string         `json:"name"`
	State       string         `json:"state"`
	Unit        string         `json:"unit,omitempty"`
	LastChanged string         `json:"lastChanged"`
	Attributes  map[string]any `json:"attributes"`
}

func (s haStateJSON) trim() haState {
	name, _ := s.Attributes["friendly_name"].(string)
	unit, _ := s.Attributes["unit_of_measurement"].(string)
	if s.Attributes == nil {
		s.Attributes = map[string]any{}
	}
	return haState{Key: s.EntityID, EntityID: s.EntityID, Name: name, State: s.State, Unit: unit, LastChanged: s.LastChanged, Attributes: s.Attributes}
}

func haListStates(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	limit := int64(100)
	if a.has("limit") {
		limit = a.int("limit")
	}
	if limit < 1 || limit > 500 {
		return nil, errors.New("limit must be from 1 to 500")
	}
	var states []haStateJSON
	if err := c.do(ctx, http.MethodGet, "/api/states", nil, nil, &states); err != nil {
		return nil, err
	}
	slices.SortFunc(states, func(x, y haStateJSON) int { return strings.Compare(x.EntityID, y.EntityID) })
	out := []haState{}
	for _, s := range states {
		if d := a.str("domain"); d != "" && !strings.HasPrefix(s.EntityID, d+".") {
			continue
		}
		if int64(len(out)) == limit {
			break
		}
		out = append(out, s.trim())
	}
	return out, nil
}

func haGetState(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	var s haStateJSON
	if err := c.do(ctx, http.MethodGet, "/api/states/"+url.PathEscape(a.str("entity_id")), nil, nil, &s); err != nil {
		return nil, err
	}
	return s.trim(), nil
}

func haGetHistory(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	since := time.Now().Add(-24 * time.Hour).UTC()
	if s := a.str("since"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return nil, fmt.Errorf("since must be a time such as 2026-01-31T00:00:00Z, not %s", quote(s))
		}
		since = t.UTC()
	}
	q := url.Values{"filter_entity_id": {a.str("entity_id")}, "minimal_response": {"true"}, "no_attributes": {"true"}}
	var history [][]struct {
		State       string `json:"state"`
		LastChanged string `json:"last_changed"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/history/period/"+url.PathEscape(since.Format(time.RFC3339)), q, nil, &history); err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, series := range history {
		for _, s := range series {
			out = append(out, map[string]any{"state": s.State, "changedAt": s.LastChanged})
		}
	}
	return map[string]any{"entityId": a.str("entity_id"), "states": out}, nil
}

func haCallService(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	data := map[string]any{}
	if raw := strings.TrimSpace(a.str("data")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &data); err != nil {
			return nil, errors.New(`data must be a JSON object, such as {"brightness_pct": 50}`)
		}
	}
	if ids := a.strings("entity_id"); len(ids) > 0 {
		for _, id := range ids {
			if !haEntityPattern.MatchString(id) {
				return nil, fmt.Errorf("%s isn't an entity, such as light.kitchen", quote(id))
			}
		}
		data["entity_id"] = ids
	}
	var changed []haStateJSON
	path := "/api/services/" + url.PathEscape(a.str("domain")) + "/" + url.PathEscape(a.str("service"))
	if err := c.do(ctx, http.MethodPost, path, nil, data, &changed); err != nil {
		return nil, err
	}
	out := []haState{}
	for _, s := range changed {
		out = append(out, s.trim())
	}
	return out, nil
}
