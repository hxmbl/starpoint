package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestConfirmAcceptsYes(t *testing.T) {
	var out bytes.Buffer
	got, err := PromptConfirm(strings.NewReader("y\n"), &out)("Proceed?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("y should confirm")
	}
}

func TestConfirmIsQuietWhenNotATerminal(t *testing.T) {
	// Reading a scripted answer should not print a prompt to nobody.
	var out bytes.Buffer
	if _, err := PromptConfirm(strings.NewReader("y\n"), &out)("Proceed?"); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("no prompt should be written without a terminal, got %q", out.String())
	}
}

func TestConfirmPromptsOnATerminal(t *testing.T) {
	// A character device is what the terminal check looks for.
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Skipf("cannot open a character device: %v", err)
	}
	defer f.Close()
	if !isTerminal(f) {
		t.Skip("this platform has no character device to test with")
	}
	var out bytes.Buffer
	if _, err := PromptConfirm(f, &out)("Proceed?"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "[y/N]") {
		t.Errorf("the prompt should be visible on a terminal: %q", out.String())
	}
}

func TestConfirmTreatsAnythingElseAsNo(t *testing.T) {
	for _, answer := range []string{"n\n", "\n", "maybe\n", "no thanks\n"} {
		var out bytes.Buffer
		got, err := PromptConfirm(strings.NewReader(answer), &out)("Proceed?")
		if err != nil {
			t.Fatalf("%q: unexpected error: %v", answer, err)
		}
		if got {
			t.Errorf("%q should not confirm", answer)
		}
	}
}

func TestConfirmEndOfInputIsNotConsent(t *testing.T) {
	// A stream that ends without answering has not said yes. The error tells the
	// caller what to do instead.
	var out bytes.Buffer
	got, err := PromptConfirm(strings.NewReader(""), &out)("Proceed?")
	if err == nil {
		t.Fatal("end of input is not consent")
	}
	if got {
		t.Error("end of input must not confirm")
	}
}

func TestConfirmWithNoAnswerNamesTheFlagToUse(t *testing.T) {
	// An empty stream is not consent, and the caller should be told what to do.
	var out bytes.Buffer
	_, err := PromptConfirm(bytes.NewReader(nil), &out)("Proceed?")
	if err == nil {
		t.Fatal("expected an error when nothing answers")
	}
	if !strings.Contains(err.Error(), "--no-confirm") {
		t.Errorf("the error should name the flag: %v", err)
	}
}

func TestConfirmReadsAScriptedAnswer(t *testing.T) {
	// `yes | starpoint sync` is a legitimate way to script this.
	var out bytes.Buffer
	got, err := PromptConfirm(bytes.NewReader([]byte("yes\n")), &out)("Proceed?")
	if err != nil || !got {
		t.Errorf("a piped answer should be honoured, got (%v, %v)", got, err)
	}
}

func TestChooseByNumber(t *testing.T) {
	var out bytes.Buffer
	got, err := PromptChoose(strings.NewReader("2\n"), &out)("Which?", []string{"brew", "apt"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "apt" {
		t.Errorf("chose %q, want apt", got)
	}
}

func TestChooseDefaultsToTheFirstOption(t *testing.T) {
	var out bytes.Buffer
	got, err := PromptChoose(strings.NewReader("\n"), &out)("Which?", []string{"brew", "apt"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "brew" {
		t.Errorf("chose %q, want brew", got)
	}
}

func TestChooseByName(t *testing.T) {
	var out bytes.Buffer
	got, err := PromptChoose(strings.NewReader("apt\n"), &out)("Which?", []string{"brew", "apt"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "apt" {
		t.Errorf("chose %q, want apt", got)
	}
}

func TestChooseRejectsNonsense(t *testing.T) {
	for _, answer := range []string{"9\n", "nonsense\n"} {
		var out bytes.Buffer
		if _, err := PromptChoose(strings.NewReader(answer), &out)("Which?", []string{"brew", "apt"}); err == nil {
			t.Errorf("%q should be rejected", answer)
		}
	}
}

func TestChooseWithNoAnswerNamesTheFlagToUse(t *testing.T) {
	var out bytes.Buffer
	_, err := PromptChoose(bytes.NewReader(nil), &out)("Which?", []string{"brew"})
	if err == nil {
		t.Fatal("expected an error when nothing answers")
	}
	if !strings.Contains(err.Error(), "--provider") {
		t.Errorf("the error should name the flag: %v", err)
	}
}

func TestChooseWithNoOptionsIsAnError(t *testing.T) {
	var out bytes.Buffer
	if _, err := PromptChoose(bytes.NewReader([]byte("1\n")), &out)("Which?", nil); err == nil {
		t.Error("an empty option list cannot be answered")
	}
}
