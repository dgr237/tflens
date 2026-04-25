package config_test

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/config"
)

// newCmd returns a fresh cobra.Command with the supplied flag
// registrations applied. Each registration is a func that takes the
// command and registers one flag — keeps every test self-contained
// (no shared command state).
func newCmd(register ...func(*cobra.Command)) *cobra.Command {
	c := &cobra.Command{Use: "test"}
	for _, r := range register {
		r(c)
	}
	return c
}

func registerOffline(c *cobra.Command) {
	c.Flags().Bool("offline", false, "")
}

func registerFormat(c *cobra.Command) {
	c.Flags().String("format", "text", "")
}

func registerRef(c *cobra.Command) {
	c.Flags().String("ref", config.RefAutoKeyword, "")
}

func registerState(c *cobra.Command) {
	c.Flags().String("state", "", "")
}

func registerWriteCheck(c *cobra.Command) {
	c.Flags().BoolP("write", "w", false, "")
	c.Flags().Bool("check", false, "")
}

// TestFromCommandReadsRegisteredFlags exercises the happy path —
// every registered flag value flows into the matching Settings field
// after the user supplies it on the command line.
func TestFromCommandReadsRegisteredFlags(t *testing.T) {
	c := newCmd(registerOffline, registerFormat, registerRef, registerState, registerWriteCheck)
	c.SetArgs([]string{
		"--offline",
		"--format", "json",
		"--ref", "origin/main",
		"--state", "/tmp/x.tfstate",
		"--write",
		"--check",
	})
	c.RunE = func(cmd *cobra.Command, _ []string) error {
		s := config.FromCommand(cmd)
		if !s.Offline {
			t.Error("Offline = false, want true")
		}
		if !s.JSON {
			t.Error("JSON = false, want true")
		}
		if s.BaseRef != "origin/main" {
			t.Errorf("BaseRef = %q, want %q", s.BaseRef, "origin/main")
		}
		if s.StatePath != "/tmp/x.tfstate" {
			t.Errorf("StatePath = %q", s.StatePath)
		}
		if !s.Write {
			t.Error("Write = false, want true")
		}
		if !s.Check {
			t.Error("Check = false, want true")
		}
		return nil
	}
	if err := c.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

// TestFromCommandUnregisteredFlagsReturnZero confirms the
// Lookup-then-Get pattern: a subcommand that doesn't register --state
// (e.g. diff) must not panic when FromCommand asks for it. The field
// silently stays at its zero value.
func TestFromCommandUnregisteredFlagsReturnZero(t *testing.T) {
	// Only register the global flags — no --ref, --state, --write, --check.
	c := newCmd(registerOffline, registerFormat)
	c.SetArgs([]string{})
	c.RunE = func(cmd *cobra.Command, _ []string) error {
		s := config.FromCommand(cmd)
		if s.BaseRef != "" {
			t.Errorf("unregistered BaseRef = %q, want empty", s.BaseRef)
		}
		if s.StatePath != "" {
			t.Errorf("unregistered StatePath = %q, want empty", s.StatePath)
		}
		if s.Write {
			t.Error("unregistered Write = true, want false")
		}
		if s.Check {
			t.Error("unregistered Check = true, want false")
		}
		return nil
	}
	if err := c.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

// TestFromCommandFormatTextLeavesJSONFalse: --format=text (the
// default) must not be misread as JSON. Guards against an accidental
// "format != empty" check replacing the explicit "format == json" one.
func TestFromCommandFormatTextLeavesJSONFalse(t *testing.T) {
	c := newCmd(registerFormat)
	c.SetArgs([]string{"--format", "text"})
	c.RunE = func(cmd *cobra.Command, _ []string) error {
		if config.FromCommand(cmd).JSON {
			t.Error("JSON = true for --format=text, want false")
		}
		return nil
	}
	if err := c.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

// TestFromCommandDefaultsWhenNoArgs: with no user-supplied args, every
// flag returns its registered default. --ref defaults to RefAutoKeyword;
// --offline defaults to false; --format defaults to "text" → JSON false.
func TestFromCommandDefaultsWhenNoArgs(t *testing.T) {
	c := newCmd(registerOffline, registerFormat, registerRef)
	c.SetArgs([]string{})
	c.RunE = func(cmd *cobra.Command, _ []string) error {
		s := config.FromCommand(cmd)
		if s.Offline {
			t.Error("Offline default = true, want false")
		}
		if s.JSON {
			t.Error("JSON default = true, want false")
		}
		if s.BaseRef != config.RefAutoKeyword {
			t.Errorf("BaseRef default = %q, want %q", s.BaseRef, config.RefAutoKeyword)
		}
		return nil
	}
	if err := c.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

// TestFromCommandCapturesWriters: cobra exposes per-command writers
// via OutOrStdout / ErrOrStderr that tests can swap out. FromCommand
// must propagate both into Settings so render.New picks the right
// destination without the cmd having to thread them separately.
func TestFromCommandCapturesWriters(t *testing.T) {
	c := newCmd()
	var out, errBuf bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&errBuf)
	c.SetArgs([]string{})
	c.RunE = func(cmd *cobra.Command, _ []string) error {
		s := config.FromCommand(cmd)
		if s.Out != &out {
			t.Errorf("Out = %v, want overridden buffer", s.Out)
		}
		if s.Err != &errBuf {
			t.Errorf("Err = %v, want overridden buffer", s.Err)
		}
		return nil
	}
	if err := c.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

// TestFromCommandAppliesOptions: WithPath / WithOnlyName run after
// flag parsing and override (or set, when the field has no flag) the
// matching Settings field. Confirms the options-pattern dispatch.
func TestFromCommandAppliesOptions(t *testing.T) {
	c := newCmd()
	c.SetArgs([]string{})
	c.RunE = func(cmd *cobra.Command, _ []string) error {
		s := config.FromCommand(cmd,
			config.WithPath("/some/path"),
			config.WithOnlyName("vpc"),
		)
		if s.Path != "/some/path" {
			t.Errorf("Path = %q, want /some/path", s.Path)
		}
		if s.OnlyName != "vpc" {
			t.Errorf("OnlyName = %q, want vpc", s.OnlyName)
		}
		return nil
	}
	if err := c.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

// TestOptionsLastWriterWins: when the same field is set by multiple
// options, the later one wins. Pin this so callers composing options
// from helper funcs can rely on left-to-right precedence.
func TestOptionsLastWriterWins(t *testing.T) {
	c := newCmd()
	c.SetArgs([]string{})
	c.RunE = func(cmd *cobra.Command, _ []string) error {
		s := config.FromCommand(cmd,
			config.WithPath("first"),
			config.WithPath("second"),
		)
		if s.Path != "second" {
			t.Errorf("Path = %q, want last-wins 'second'", s.Path)
		}
		return nil
	}
	if err := c.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}
