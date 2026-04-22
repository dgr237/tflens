package loader

import (
	"fmt"
	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/ast"
)

// CrossValidateCall checks one specific module call in parent against a
// candidate child module analysis. This is the primitive used by the whatif
// subcommand to answer "would the parent still satisfy this child if I
// upgraded?" without touching the rest of the project.
//
// Returns nil (not an error) if parent has no module call named
// moduleCallName — callers should check ahead of time if they want to treat
// that as an error.
func CrossValidateCall(parent *analysis.Module, moduleCallName string, candidateChild *analysis.Module) []analysis.ValidationError {
	for _, mb := range parent.Filter(analysis.KindModule) {
		if mb.Name != moduleCallName {
			continue
		}
		return checkModuleCall(parent, mb, candidateChild)
	}
	return nil
}

// CrossValidate walks the project tree and reports, for every parent → child
// module call pair that could be resolved locally:
//
//   - required child variables (no default) that the parent does not pass
//   - arguments the parent passes that the child does not declare
//   - arguments whose type is inferable but incompatible with the child's
//     declared type
//
// Remote sources (registry, git) are skipped because the child module is not
// loadable; local path sources (./foo, ../bar) are always followed.
func CrossValidate(project *Project) []analysis.ValidationError {
	if project == nil || project.Root == nil {
		return nil
	}
	var errs []analysis.ValidationError
	project.Walk(func(node *ModuleNode) bool {
		for _, mb := range node.Module.Filter(analysis.KindModule) {
			child, ok := node.Children[mb.Name]
			if !ok || child == nil {
				continue // remote source, or failed-to-load child — skip silently
			}
			errs = append(errs, checkModuleCall(node.Module, mb, child.Module)...)
		}
		return true
	})
	return errs
}

// checkModuleCall compares one module call (mb, a KindModule entity on the
// parent) against its child module's variable declarations.
func checkModuleCall(parent *analysis.Module, mb analysis.Entity, child *analysis.Module) []analysis.ValidationError {
	var errs []analysis.ValidationError

	// Index child variables by name.
	childVars := map[string]analysis.Entity{}
	for _, v := range child.Filter(analysis.KindVariable) {
		childVars[v.Name] = v
	}

	args := mb.ModuleArgs // may be nil if no arguments were passed

	// 1. Required child inputs the parent does not provide.
	for _, v := range child.Filter(analysis.KindVariable) {
		if v.HasDefault {
			continue
		}
		if _, ok := args[v.Name]; ok {
			continue
		}
		errs = append(errs, analysis.ValidationError{
			EntityID: mb.ID(),
			Ref:      v.ID(),
			Pos:      mb.Pos,
			Msg: fmt.Sprintf("%s does not pass required input %q (no default in child module)",
				mb.ID(), v.Name),
		})
	}

	// 2. Arguments passed that the child has no corresponding variable for,
	// and 3. type-mismatched arguments for ones it does.
	for name, expr := range args {
		v, known := childVars[name]
		if !known {
			errs = append(errs, analysis.ValidationError{
				EntityID: mb.ID(),
				Ref:      (analysis.Entity{Kind: analysis.KindVariable, Name: name}).ID(),
				Pos:      ast.NodePos(expr),
				Msg: fmt.Sprintf("%s passes unknown argument %q (child module declares no such variable)",
					mb.ID(), name),
			})
			continue
		}
		if v.DeclaredType == nil {
			continue // child has no type constraint; nothing to check
		}
		inferred := parent.InferExprType(expr)
		if inferred == nil || inferred.Kind == analysis.TypeUnknown {
			continue
		}
		if !analysis.IsTypeCompatible(v.DeclaredType, inferred) {
			errs = append(errs, analysis.ValidationError{
				EntityID: mb.ID(),
				Ref:      v.ID(),
				Pos:      ast.NodePos(expr),
				Msg: fmt.Sprintf("%s passes %q as %s but child variable expects %s",
					mb.ID(), name, inferred, v.DeclaredType),
			})
		}
	}
	return errs
}
