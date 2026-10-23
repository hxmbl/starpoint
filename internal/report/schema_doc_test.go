package report

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
)

// docs/json-schema.md is the contract consumers read. It must state the
// version the writer emits, document every example at that version, and list
// the warning codes the newest version added.
func TestSchemaDocumentMatchesTheEmittedVersion(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "json-schema.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	if want := fmt.Sprintf("Current schema version: `%d`", SchemaVersion); !strings.Contains(doc, want) {
		t.Errorf("docs/json-schema.md must say %q", want)
	}
	if old := fmt.Sprintf(`"schema_version": %d,`, SchemaVersion-1); strings.Contains(doc, old) {
		t.Errorf("docs/json-schema.md still has an example at %s", old)
	}
	if want := fmt.Sprintf("## Migrating from schema %d to %d", SchemaVersion-1, SchemaVersion); !strings.Contains(doc, want) {
		t.Errorf("docs/json-schema.md must have %q", want)
	}
	for _, code := range []string{model.WarnPruneSkipped, model.WarnPipOutsideVirtualenv} {
		if !strings.Contains(doc, "| `"+code+"` |") {
			t.Errorf("docs/json-schema.md must list the warning code %s", code)
		}
	}
}
