package config

// Option mutates a Settings as part of FromCommand's construction.
// Callers compose options to fold positional args (Path, OnlyName)
// and any other cmd-derived state into the Settings without having
// to mutate it in two steps after FromCommand returns.
//
// Options never fail; they're for trivial field assignment.
// Anything that needs I/O (auto-base-ref resolution, etc.) stays in
// the cmd-side run* method where the error can flow back through
// cobra's RunE.
type Option func(*Settings)

// WithPath sets the Settings.Path field. Used by every subcommand
// whose first positional arg is a workspace path.
func WithPath(p string) Option {
	return func(s *Settings) { s.Path = p }
}

// WithOnlyName sets the Settings.OnlyName field. Used by whatif's
// optional second positional arg to scope simulation to one call.
func WithOnlyName(name string) Option {
	return func(s *Settings) { s.OnlyName = name }
}
