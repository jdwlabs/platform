package bootstrap

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/jdwlabs/platform/internal/prompt"
)

// stdinPath is the conventional "read from stdin" file argument. A path is the
// only sanctioned way to hand this CLI a secret: a --value flag would put the
// credential in argv, where `ps` and the shell history both keep it long after
// the rotation that was supposed to retire it.
const stdinPath = "-"

// SeedInputError is a refusal the caller can act on directly: Fix holds the
// commands that satisfy it. The CLI surfaces those as the help line, because a
// generic "run --help" sends the caller hunting for a flag the refusal has
// already named.
type SeedInputError struct {
	Reason string
	Fix    []string
}

func (e *SeedInputError) Error() string  { return e.Reason }
func (e *SeedInputError) Help() []string { return e.Fix }

// SeedValueSource supplies one field's value without a terminal, so an
// automated caller facing a bad credential has a sanctioned way to replace it.
type SeedValueSource struct {
	// Path is a file to read, or "-" for stdin.
	Path string
	// Field is the single spec field the value is written to.
	Field string
	// KeepTrailingNewline stores the trailing line terminator instead of
	// removing it.
	KeepTrailingNewline bool
	// Stdin defaults to os.Stdin; a test supplies its own.
	Stdin io.Reader

	value string
	read  bool
}

// NewSeedValueSource resolves the flags into a source, or (nil, nil) when no
// file was named and the interactive path still applies.
//
// The selection is resolved here rather than at write time so a bad
// combination costs one error instead of a Vault port-forward and a partial
// write.
func NewSeedValueSource(path string, keepTrailingNewline bool, tenants, selected, fields []string) (*SeedValueSource, error) {
	if path == "" {
		if keepTrailingNewline {
			// Accepting a flag that cannot do anything is the dropped-flag
			// failure: the caller believes the value was kept and has nothing
			// to read that says otherwise.
			return nil, &SeedInputError{
				Reason: "--keep-trailing-newline applies only to a value read with --from-file",
				Fix:    []string{"platformctl bootstrap seed <spec> --field <name> --from-file <path> --keep-trailing-newline"},
			}
		}
		return nil, nil
	}
	if len(selected) != 1 {
		return nil, &SeedInputError{
			Reason: fmt.Sprintf("--from-file writes one field and needs exactly one spec argument, got %d", len(selected)),
			Fix:    []string{"platformctl bootstrap seed <spec> --field <name> --from-file <path>"},
		}
	}
	if len(fields) > 1 {
		return nil, &SeedInputError{
			Reason: fmt.Sprintf("--from-file writes one field, got %d --field flags", len(fields)),
			Fix:    []string{fmt.Sprintf("platformctl bootstrap seed %s --field <name> --from-file <path>", selected[0])},
		}
	}
	field, err := resolveSeedField(tenants, selected[0], fields)
	if err != nil {
		return nil, err
	}
	return &SeedValueSource{Path: path, Field: field, KeepTrailingNewline: keepTrailingNewline}, nil
}

// resolveSeedField names the field the value lands in, defaulting to the
// spec's only field when it has exactly one.
func resolveSeedField(tenants []string, spec string, fields []string) (string, error) {
	known, err := SeedFieldNames(tenants, spec)
	if err != nil {
		return "", err
	}
	if len(fields) == 1 {
		if !contains(known, fields[0]) {
			return "", fmt.Errorf("spec %s has no field %s; valid: %s", spec, fields[0], strings.Join(known, ", "))
		}
		return fields[0], nil
	}
	if len(known) != 1 {
		return "", &SeedInputError{
			Reason: fmt.Sprintf("spec %s has %d fields, so --from-file needs --field <name>", spec, len(known)),
			Fix: []string{
				fmt.Sprintf("platformctl bootstrap seed %s --field <name> --from-file <path>", spec),
				fmt.Sprintf("fields of %s: %s", spec, strings.Join(known, ", ")),
			},
		}
	}
	return known[0], nil
}

// Read returns the value, reading the source at most once so a stdin source
// survives a caller that asks twice.
//
// The bytes are stored verbatim but for one trailing line terminator, which is
// dropped unless KeepTrailingNewline says otherwise. Shells, editors and
// `printf` all append one and no credential here ends in a newline by
// intent, so keeping it turns a correct rotation into a credential that
// authenticates as a different string. Nothing else is trimmed: quotes,
// leading whitespace and interior whitespace can all be part of a secret, and
// a guard that stripped them would corrupt exactly the values it cannot
// distinguish from the ones it was meant to protect.
func (s *SeedValueSource) Read() (string, error) {
	if s.read {
		return s.value, nil
	}
	raw, err := s.readAll()
	if err != nil {
		return "", err
	}
	v := string(raw)
	if !s.KeepTrailingNewline {
		v = trimOneTrailingNewline(v)
	}
	if v == "" {
		return "", fmt.Errorf("read an empty value from %s; write the secret's own bytes there, unquoted", s.describe())
	}
	s.value, s.read = v, true
	return s.value, nil
}

func (s *SeedValueSource) readAll() ([]byte, error) {
	if s.Path == stdinPath {
		in := s.Stdin
		if in == nil {
			in = os.Stdin
		}
		raw, err := io.ReadAll(in)
		if err != nil {
			return nil, fmt.Errorf("read stdin: %w", err)
		}
		return raw, nil
	}
	raw, err := os.ReadFile(s.Path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.Path, err)
	}
	return raw, nil
}

func (s *SeedValueSource) describe() string {
	if s.Path == stdinPath {
		return "stdin"
	}
	return s.Path
}

// trimOneTrailingNewline removes one line terminator, and only a terminator: a
// lone trailing \r is not one, and dropping it would corrupt a value that ends
// in a carriage return on purpose.
func trimOneTrailingNewline(v string) string {
	if strings.HasSuffix(v, "\r\n") {
		return strings.TrimSuffix(v, "\r\n")
	}
	return strings.TrimSuffix(v, "\n")
}

// PreflightSeedInput refuses a seed that has no way to obtain a value, before
// anything is connected or port-forwarded.
//
// Without it the refusal arrives inside Apply, after the Vault resolver has
// already stood up a port-forward and authenticated — and for the interactive
// path it arrived as a form failure naming /dev/tty, which says nothing about
// the flag that would have worked.
func PreflightSeedInput(tenants, selected, fields []string, src *SeedValueSource, nonInteractive bool) error {
	if src != nil {
		// Reading the source here rather than in Apply is the whole point: an
		// unreadable path must cost an error, not a port-forward. Read
		// memoizes, so Apply re-reads nothing and a stdin source survives.
		_, err := src.Read()
		return err
	}
	interactive := !nonInteractive && prompt.HasTerminal()
	if interactive {
		return nil
	}
	specs := buildSeedSpecs(tenants)
	keys := selected
	if len(keys) == 0 {
		keys = make([]string, 0, len(specs))
		for k := range specs {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		spec, ok := specs[key]
		if !ok {
			continue
		}
		specFields := spec.Fields
		if len(fields) > 0 {
			specFields = filterFields(specFields, fields)
		}
		for _, f := range specFields {
			if os.Getenv(f.EnvVar) != "" || f.Optional {
				continue
			}
			return noValueSourceError(key, f, nonInteractive)
		}
	}
	return nil
}

func noValueSourceError(spec string, f seedField, nonInteractive bool) error {
	cause := "no terminal is attached"
	if nonInteractive {
		cause = "--non-interactive was passed"
	}
	return &SeedInputError{
		Reason: fmt.Sprintf("seed %s/%s has no value source: %s and %s is unset", spec, f.Name, cause, f.EnvVar),
		Fix: []string{
			fmt.Sprintf("platformctl bootstrap seed %s --field %s --from-file <path>", spec, f.Name),
			fmt.Sprintf("platformctl bootstrap seed %s --field %s --from-file -   # reads the value from stdin", spec, f.Name),
			fmt.Sprintf("or set %s in the environment", f.EnvVar),
		},
	}
}
