package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"
	"strings"
)

// PromptConfirm returns a confirmation function that reads from in and writes the
// question to out.
//
// It never assumes yes. A terminal is prompted normally; a pipe is read once, so
// `yes | str sync` works; and a stream that ends without an answer is an
// error naming --no-confirm, because silently declining would leave the caller
// unsure whether the operation happened.
func PromptConfirm(in io.Reader, out io.Writer) func(string) (bool, error) {
	return func(question string) (bool, error) {
		interactive := isTerminal(in)
		if !interactive {
			// Not a terminal: a scripted answer is still an answer, but an absent
			// one must be an explicit decision rather than an assumption.
			answer, err := readLine(in)
			if err != nil {
				if errors.Is(err, io.EOF) {
					return false, fmt.Errorf(
						"this operation needs confirmation and stdin provided no answer; pass --no-confirm")
				}
				return false, err
			}
			return isYes(answer), nil
		}
		fmt.Fprintf(out, "%s [y/N] ", question)
		answer, err := readLine(in)
		if err != nil {
			if errors.Is(err, io.EOF) {
				// End of input on a terminal is the user pressing Ctrl-D: no.
				fmt.Fprintln(out)
				return false, nil
			}
			return false, fmt.Errorf("reading confirmation: %w", err)
		}
		return isYes(answer), nil
	}
}

// PromptChoose returns a function that asks the user to pick one of options.
// Like confirmation it never guesses: a non-terminal with no answer is an error.
func PromptChoose(in io.Reader, out io.Writer) func(string, []string) (string, error) {
	return func(question string, options []string) (string, error) {
		if len(options) == 0 {
			return "", fmt.Errorf("there is nothing to choose from")
		}
		if !isTerminal(in) {
			answer, err := readLine(in)
			if err != nil {
				if errors.Is(err, io.EOF) {
					return "", fmt.Errorf(
						"the provider choice is ambiguous and stdin provided no answer; pass --provider <name>")
				}
				return "", err
			}
			return pick(answer, options)
		}
		fmt.Fprintf(out, "%s\n", question)
		for i, o := range options {
			fmt.Fprintf(out, "  %d) %s\n", i+1, o)
		}
		fmt.Fprint(out, "choice [1]: ")
		answer, err := readLine(in)
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("reading choice: %w", err)
		}
		return pick(answer, options)
	}
}

// pick resolves an answer to one of options, accepting a number or a name.
func pick(answer string, options []string) (string, error) {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return options[0], nil
	}
	var idx int
	if _, err := fmt.Sscanf(answer, "%d", &idx); err == nil {
		if idx < 1 || idx > len(options) {
			return "", fmt.Errorf("%d is not one of the options", idx)
		}
		return options[idx-1], nil
	}
	for _, o := range options {
		if strings.EqualFold(o, answer) {
			return o, nil
		}
	}
	return "", fmt.Errorf("%q is not one of the options", answer)
}

// readLine reads one line, tolerating a final line without a newline.
func readLine(in io.Reader) (string, error) {
	return bufio.NewReader(in).ReadString('\n')
}

// isYes reads an answer as consent. Everything that is not an explicit yes is a
// no, because a destructive default should never be the permissive one.
func isYes(answer string) bool {
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

// isTerminal reports whether in is an interactive terminal.
//
// It asks the operating system rather than guessing from the file mode, because
// the guess is wrong in exactly the case that matters: /dev/null is a character
// device, so `< /dev/null` would look like a terminal, Starpoint would print a
// prompt into the void, read nothing, and treat end-of-input as an answer.
// Distinguishing those requires an ioctl, so this uses the one library that does
// it correctly on every platform rather than a darwin-only syscall.
func isTerminal(in io.Reader) bool {
	f, ok := in.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// IsTerminal reports whether r is a terminal. The program uses it to decide
// whether a run is interactive.
func IsTerminal(r io.Reader) bool { return isTerminal(r) }
