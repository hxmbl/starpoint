package cli

import (
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/testsupport"
)

// doctor counts unmanaged software per provider and points at adopt. It is
// information, not a problem: a machine full of software Starpoint does not
// manage yet is not unhealthy.
func TestDoctorCountsUnmanagedSoftwarePerProvider(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"jq\"\nprovider = \"brew\"\n")
	h.brew.With(model.Installation{Name: "jq"}).With(model.Installation{Name: "wget"}).With(model.Installation{Name: "libidn2"})
	h.env.Registry.Register(&testsupport.RequestedFake{FakeProvider: h.brew, Marks: map[string]bool{"jq": true, "wget": true, "libidn2": false}})
	h.sys.With(model.Installation{Name: "git", Path: "/usr/bin/git"})

	var doc struct {
		Healthy   bool `json:"healthy"`
		Unmanaged []struct {
			Provider  string `json:"provider"`
			Count     int    `json:"count"`
			Requested int    `json:"requested"`
		} `json:"unmanaged"`
		Problems []model.Warning `json:"problems"`
	}
	h.decode(h.mustRun("doctor", "--json"), &doc)
	if len(doc.Unmanaged) != 1 || doc.Unmanaged[0].Provider != "brew" || doc.Unmanaged[0].Count != 2 || doc.Unmanaged[0].Requested != 1 {
		t.Errorf("unmanaged = %+v, want brew: 2 unmanaged, 1 requested (system excluded)", doc.Unmanaged)
	}
	for _, p := range doc.Problems {
		if strings.Contains(p.Message, "unmanaged") {
			t.Errorf("unmanaged software is not a problem: %+v", p)
		}
	}
	out := h.mustRun("doctor")
	if !strings.Contains(out, "brew: 2 unmanaged, 1 installed on request") || !strings.Contains(out, "starpoint adopt --all --dry-run") {
		t.Errorf("doctor should show the count and the command:\n%s", out)
	}
	if strings.Contains(h.mustRun("sync", "--dry-run"), "unmanaged") {
		t.Error("sync does not carry the unmanaged count")
	}
}
