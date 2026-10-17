package report

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/engine"
	"github.com/hxmbl/starpoint/internal/locator"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/resolve"
)

// legacyWrappers are the keys schema version 1 used to bury a payload under. A
// consumer written against version 2 reads the same data at the top level, so
// one of these appearing again is a regression, not an extension.
var legacyWrappers = []string{"data", "sync", "doctor", "explain", "lookup_wrapper"}

func sampleState() model.State {
	return model.State{
		Package: model.Package{Name: "ripgrep"},
		Res:     model.Resolution{Provider: "brew"},
		Current: &model.Installation{Name: "ripgrep", Path: "/usr/local/bin/rg", Provider: "brew", Scope: model.ScopeUser},
		Status:  model.StatusCorrect,
	}
}

func sampleAction() model.PlannedAction {
	return model.PlannedAction{
		Action: model.ActionInstall,
		State:  sampleState(),
		Note:   "installed",
	}
}

func sampleTransition() model.Transition {
	return model.Transition{
		Package: model.Package{Name: "ripgrep"},
		From:    model.ProviderRef{Name: "brew", Available: true},
		To:      model.ProviderRef{Name: "uv", Available: true},
		Phase:   model.TransitionCompleted,
	}
}

// renderers is every command response the report writer produces. A new
// renderer belongs here, so that "one flat document, at schema 2" is a property
// of the writer rather than a habit.
func renderers() map[string]func(w *Writer) error {
	return map[string]func(*Writer) error{
		"list": func(w *Writer) error {
			return w.List([]model.State{sampleState()}, []locator.Found{{Path: "/usr/local/bin/orphan"}})
		},
		"info": func(w *Writer) error {
			return w.Info(sampleState(), nil)
		},
		"info-with-transition": func(w *Writer) error {
			return w.Info(sampleState(), map[string]any{"transition": sampleTransition()})
		},
		"info-with-lookup": func(w *Writer) error {
			return w.Info(sampleState(), map[string]any{"lookup": engine.Survey{DurationMS: 1.5}})
		},
		"explain": func(w *Writer) error {
			return w.Explain(resolve.Explanation{
				Package: model.Package{Name: "ripgrep"},
				Final:   model.Resolution{Provider: "brew", Confidence: "certain"},
				Steps:   []resolve.Step{{Rule: "explicit", Outcome: "applied", Applied: true}},
			})
		},
		"sync": func(w *Writer) error {
			plan := model.Plan{States: []model.State{sampleState()}, Actions: []model.PlannedAction{sampleAction()}}
			return w.Sync(&engine.Result{Applied: []model.PlannedAction{sampleAction()}}, plan, nil)
		},
		"transitions": func(w *Writer) error {
			return w.Transitions([]model.Transition{sampleTransition()}, true)
		},
		"doctor": func(w *Writer) error {
			return w.Doctor(DoctorReport{
				Starpoint: StarpointInfo{Version: "v0.1.0"},
				Config:    ConfigInfo{Path: "/home/u/.starpoint.toml", Exists: true, Schema: 1, PackageCount: 1},
				Providers: []ProviderHealth{{Name: "brew", Available: true}},
				Healthy:   true,
			})
		},
		"install": func(w *Writer) error {
			return w.Result("install", &engine.Result{Applied: []model.PlannedAction{sampleAction()}})
		},
		"remove": func(w *Writer) error {
			return w.Result("remove", &engine.Result{Applied: []model.PlannedAction{sampleAction()}})
		},
		"update": func(w *Writer) error {
			return w.Update(&engine.Result{Applied: []model.PlannedAction{sampleAction()}})
		},
		"prune": func(w *Writer) error {
			return w.Prune(&engine.Result{Applied: []model.PlannedAction{sampleAction()}}, true)
		},
		"init": func(w *Writer) error {
			return w.Init("/home/u/.starpoint.toml", true, false)
		},
		"render": func(w *Writer) error {
			return w.Render(map[string]any{"operation": "recover", "interrupted": []any{}})
		},
	}
}

// Every command emits exactly one JSON object. Concatenated objects are not
// parseable, which is how `info --json` used to fail for every consumer.
func TestEveryRendererEmitsOneDocument(t *testing.T) {
	for name, render := range renderers() {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := render(New(&buf, true)); err != nil {
				t.Fatal(err)
			}
			dec := json.NewDecoder(bytes.NewReader(buf.Bytes()))
			var first map[string]any
			if err := dec.Decode(&first); err != nil {
				t.Fatalf("not a JSON document: %v\n%s", err, buf.String())
			}
			var extra map[string]any
			if err := dec.Decode(&extra); err != io.EOF {
				t.Errorf("more than one JSON document: %v\n%s", err, buf.String())
			}
			if first == nil {
				t.Errorf("null document:\n%s", buf.String())
			}
		})
	}
}

func TestEveryRendererCarriesTheSchemaVersion(t *testing.T) {
	for name, render := range renderers() {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := render(New(&buf, true)); err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if got := payload["schema_version"]; got != float64(SchemaVersion) {
				t.Errorf("schema_version = %v, want %d\n%s", got, SchemaVersion, buf.String())
			}
		})
	}
}

// Version 1 buried payloads under "data" or under a command name. Those keys
// are gone; the same fields live at the top level.
func TestNoRendererReintroducesAVersion1Wrapper(t *testing.T) {
	for name, render := range renderers() {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := render(New(&buf, true)); err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			for _, key := range legacyWrappers {
				if _, ok := payload[key]; ok {
					t.Errorf("%q is a version 1 wrapper; the payload belongs at the top level:\n%s", key, buf.String())
				}
			}
		})
	}
}

// Every action list is the documented ActionResult shape, not the engine's
// internal planned action. The documented type is the contract.
func TestActionListsUseTheDocumentedActionResult(t *testing.T) {
	for _, name := range []string{"install", "remove", "update", "prune", "sync"} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := renderers()[name](New(&buf, true)); err != nil {
				t.Fatal(err)
			}
			var payload struct {
				Actions []map[string]any `json:"actions"`
			}
			if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if len(payload.Actions) == 0 {
				t.Fatalf("expected at least one action:\n%s", buf.String())
			}
			for _, a := range payload.Actions {
				for _, key := range []string{"action", "package"} {
					if _, ok := a[key]; !ok {
						t.Errorf("action is missing %q: %v", key, a)
					}
				}
				if _, ok := a["state"]; ok {
					t.Errorf("action leaked the engine's planned-action shape: %v", a)
				}
			}
		})
	}
}

// An empty list is [] rather than null, so a consumer can iterate without
// special-casing a missing collection.
func TestEmptyCollectionsAreArraysNotNull(t *testing.T) {
	var buf bytes.Buffer
	if err := New(&buf, true).Prune(&engine.Result{}, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"actions": []`) {
		t.Errorf("an empty action list should be an empty array:\n%s", buf.String())
	}
}
