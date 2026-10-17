package elevation

import (
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
)

func TestNoWarningWhenNotElevated(t *testing.T) {
	if w := (Status{}).Warning(); w != nil {
		t.Fatalf("an ordinary run must not warn: %+v", w)
	}
}

func TestElevatedWarningNamesHomeAndInvokingUser(t *testing.T) {
	w := Status{Elevated: true, Mechanism: "effective user id 0, root", Home: "/root", InvokingUser: "alice"}.Warning()
	if w == nil {
		t.Fatal("an elevated run must warn")
	}
	if w.Code != model.WarnElevated || w.Severity != model.SeverityWarn {
		t.Errorf("wrong code or severity: %+v", w)
	}
	for _, want := range []string{"/root", "~/.local/bin", "alice", "outside the user's normal environment"} {
		if !strings.Contains(w.Message, want) {
			t.Errorf("message should mention %q: %s", want, w.Message)
		}
	}
}

func TestDetectMatchesTheProcess(t *testing.T) {
	s := Detect()
	if s.Elevated && s.Mechanism == "" {
		t.Error("an elevated status must say how it was detected")
	}
}
