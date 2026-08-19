package truenas

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// FakeMiddleware is an in-memory stand-in for the TrueNAS middleware, so the
// destructive path can be exercised against fixtures instead of the NAS. It
// follows the same convention as k8s.NewFake: a real type in the package rather
// than a copy in each test file that needs one.
type FakeMiddleware struct {
	Datasets []Dataset
	Extents  []Extent
	Targets  []Target
	Mappings []TargetExtent
	Shares   []NFSShare
	Sessions []Session

	// Calls records every method invoked, in order, which is how a test asserts
	// that deletes happened in dependency order rather than merely happened.
	Calls []string
	// Fail returns an error for one method, standing in for a middleware that
	// refuses or a connection that drops mid-plan.
	Fail map[string]error

	closed bool
}

func (f *FakeMiddleware) Close() error { f.closed = true; return nil }

// Closed reports whether the caller shut the connection down.
func (f *FakeMiddleware) Closed() bool { return f.closed }

func (f *FakeMiddleware) Call(_ context.Context, method string, params []any, out any) error {
	f.Calls = append(f.Calls, method)
	if err, ok := f.Fail[method]; ok {
		return err
	}

	if strings.HasSuffix(method, ".delete") {
		return f.delete(method, params)
	}
	rows, err := f.rowsFor(method)
	if err != nil {
		return err
	}
	return encodeInto(filterRows(rows, params), out)
}

func (f *FakeMiddleware) rowsFor(method string) ([]map[string]any, error) {
	switch method {
	case "auth.login_with_api_key":
		return nil, nil
	case "pool.dataset.query":
		out := make([]map[string]any, 0, len(f.Datasets))
		for _, d := range f.Datasets {
			out = append(out, map[string]any{
				"id": d.ID, "name": d.Name, "type": d.Type,
				"mountpoint": d.Mountpoint, "used": map[string]any{"parsed": float64(d.Used)},
			})
		}
		return out, nil
	case "iscsi.extent.query":
		out := make([]map[string]any, 0, len(f.Extents))
		for _, e := range f.Extents {
			out = append(out, map[string]any{
				"id": float64(e.ID), "name": e.Name, "type": e.Type, "disk": e.Disk, "path": e.Path,
			})
		}
		return out, nil
	case "iscsi.target.query":
		out := make([]map[string]any, 0, len(f.Targets))
		for _, t := range f.Targets {
			out = append(out, map[string]any{"id": float64(t.ID), "name": t.Name, "alias": t.Alias})
		}
		return out, nil
	case "iscsi.targetextent.query":
		out := make([]map[string]any, 0, len(f.Mappings))
		for _, m := range f.Mappings {
			out = append(out, map[string]any{
				"id": float64(m.ID), "target": float64(m.TargetID), "extent": float64(m.ExtentID),
			})
		}
		return out, nil
	case "sharing.nfs.query":
		out := make([]map[string]any, 0, len(f.Shares))
		for _, s := range f.Shares {
			out = append(out, map[string]any{"id": float64(s.ID), "path": s.Path})
		}
		return out, nil
	case "iscsi.global.sessions":
		out := make([]map[string]any, 0, len(f.Sessions))
		for _, s := range f.Sessions {
			out = append(out, map[string]any{"target": s.Target, "initiator": s.Initiator})
		}
		return out, nil
	default:
		return nil, fmt.Errorf("fake middleware has no method %s", method)
	}
}

func (f *FakeMiddleware) delete(method string, params []any) error {
	if len(params) == 0 {
		return fmt.Errorf("%s called with no id", method)
	}
	switch method {
	case "pool.dataset.delete":
		id, _ := params[0].(string)
		kept := f.Datasets[:0]
		for _, d := range f.Datasets {
			if d.ID != id {
				kept = append(kept, d)
			}
		}
		f.Datasets = kept
	case "iscsi.extent.delete":
		f.Extents = deleteByID(f.Extents, params[0], func(e Extent) int { return e.ID })
	case "iscsi.target.delete":
		f.Targets = deleteByID(f.Targets, params[0], func(t Target) int { return t.ID })
	case "iscsi.targetextent.delete":
		f.Mappings = deleteByID(f.Mappings, params[0], func(m TargetExtent) int { return m.ID })
	case "sharing.nfs.delete":
		f.Shares = deleteByID(f.Shares, params[0], func(s NFSShare) int { return s.ID })
	default:
		return fmt.Errorf("fake middleware has no method %s", method)
	}
	return nil
}

func deleteByID[T any](rows []T, want any, idOf func(T) int) []T {
	id, ok := want.(int)
	if !ok {
		return rows
	}
	kept := make([]T, 0, len(rows))
	for _, r := range rows {
		if idOf(r) != id {
			kept = append(kept, r)
		}
	}
	return kept
}

// filterRows applies the single-condition query filters this package issues:
// an equality on id, or the middleware's starts-with operator on id.
func filterRows(rows []map[string]any, params []any) []map[string]any {
	if len(params) == 0 {
		return rows
	}
	filters, ok := params[0].([]any)
	if !ok || len(filters) == 0 {
		return rows
	}
	cond, ok := filters[0].([]any)
	if !ok || len(cond) != 3 {
		return rows
	}
	field, _ := cond[0].(string)
	op, _ := cond[1].(string)

	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		if matchesCondition(r[field], op, cond[2]) {
			out = append(out, r)
		}
	}
	return out
}

func matchesCondition(have any, op string, want any) bool {
	switch op {
	case "=":
		return fmt.Sprint(have) == fmt.Sprint(want)
	case "^":
		s, ok := have.(string)
		prefix, pok := want.(string)
		return ok && pok && strings.HasPrefix(s, prefix)
	default:
		return true
	}
}

func encodeInto(rows []map[string]any, out any) error {
	if out == nil {
		return nil
	}
	if rows == nil {
		// The only non-collection call this package makes is the login, whose
		// result is the boolean the dial path checks.
		return json.Unmarshal([]byte("true"), out)
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}
