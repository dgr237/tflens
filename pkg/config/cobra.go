package config

import "github.com/spf13/cobra"

// FromCommand reads every recognised tflens flag from cmd plus its
// stdout/stderr writers, then applies any Option funcs the caller
// supplied to fold in cmd-derived fields like Path and OnlyName.
// Flags not registered on this subcommand silently default to the
// zero value — Settings is a union; subcommands populate only the
// fields relevant to them.
//
// The flag-name strings live here exactly once. Subcommands that
// register a new flag should add a line here so its value flows into
// Settings rather than being read ad-hoc with cmd.Flags().GetX.
func FromCommand(cmd *cobra.Command, opts ...Option) Settings {
	s := Settings{
		Out:       cmd.OutOrStdout(),
		Err:       cmd.ErrOrStderr(),
		Offline:   getBool(cmd, "offline"),
		JSON:      getString(cmd, "format") == "json",
		BaseRef:   getString(cmd, "ref"),
		StatePath: getString(cmd, "state"),
		Write:     getBool(cmd, "write"),
		Check:     getBool(cmd, "check"),
	}
	for _, opt := range opts {
		opt(&s)
	}
	return s
}

// getBool returns the bool value of the named flag, or false when the
// flag isn't registered on this subcommand. The Lookup guard avoids a
// "flag accessed but not defined" warning that pflag emits otherwise.
func getBool(cmd *cobra.Command, name string) bool {
	if cmd.Flags().Lookup(name) == nil {
		return false
	}
	v, _ := cmd.Flags().GetBool(name)
	return v
}

// getString returns the string value of the named flag, or "" when
// the flag isn't registered on this subcommand.
func getString(cmd *cobra.Command, name string) string {
	if cmd.Flags().Lookup(name) == nil {
		return ""
	}
	v, _ := cmd.Flags().GetString(name)
	return v
}
