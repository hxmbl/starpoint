package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// PromptConfirm returns a confirmation function that reads from in and writes the
// question to out. It reports an error when in is not interactive so callers can
// fail loudly instead of silently assuming yes.
func PromptConfirm(in io.Reader, out io.Writer) func(string) (bool, error) {
	return func(question string) (bool, error) {
		interactive, err := isInteractive(in)
		if err != nil {
			return false, err
		}
		if !interactive {
			return false, fmt.Errorf("this operation needs confirmation but stdin is not a terminal; pass --no-confirm")
		}
		fmt.Fprintf(out, "%s [y/N] ", question)
		r := bufio.NewReader(in)
		line, err := r.ReadString('\n')
		if err != nil && line == "" {
			return false, fmt.Errorf("reading confirmation: %w", err)
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			return true, nil
		default:
			return false, nil
		}
	}
}

// PromptChoose returns a function that asks the user to pick one of options.
func PromptChoose(in io.Reader, out io.Writer) func(string, []string) (string, error) {
	return func(question string, options []string) (string, error) {
		interactive, err := isInteractive(in)
		if err != nil {
			return "", err
		}
		if !interactive {
			return "", fmt.Errorf("the provider choice is ambiguous and stdin is not a terminal; pass --provider <name>")
		}
		fmt.Fprintf(out, "%s\n", question)
		for i, o := range options {
			fmt.Fprintf(out, "  %d) %s\n", i+1, o)
		}
		fmt.Fprint(out, "choice [1]: ")
		r := bufio.NewReader(in)
		line, err := r.ReadString('\n')
		if err != nil && line == "" {
			return "", fmt.Errorf("reading choice: %w", err)
		}
		choice := strings.TrimSpace(line)
		if choice == "" {
			return options[0], nil
		}
		var idx int
		if _, err := fmt.Sscanf(choice, "%d", &idx); err != nil {
			// Allow typing the provider name directly.
			for _, o := range options {
				if strings.EqualFold(o, choice) {
					return o, nil
				}
			}
			return "", fmt.Errorf("%q is not one of the options", choice)
		}
		if idx < 1 || idx > len(options) {
			return "", fmt.Errorf("%d is not one of the options", idx)
		}
		return options[idx-1], nil
	}
}

// isInteractive reports whether in is a terminal.
func isInteractive(in io.Reader) (bool, error) {
	f, ok := in.(*os.File)
	if !ok {
		return false, nil
	}
	info, err := f.Stat()
	if err != nil {
		return false, fmt.Errorf("inspecting stdin: %w", err)
	}
	return info.Mode()&os.ModeCharDevice != 0, nil
}
