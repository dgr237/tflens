#!/usr/bin/env python3
"""
Worked example: convert a `tflens export` JSON document into a
Crossplane Composition + CompositeResourceDefinition (XRD) targeting
the Upbound provider-aws managed resources (eks.aws.upbound.io,
iam.aws.upbound.io, ec2.aws.upbound.io, …).

This is a deliberately small POC. It produces:

  - One CompositeResourceDefinition (XRD) describing the input shape
    (Terraform variables → spec.parameters fields).
  - One Composition with one resources[] entry per Terraform resource.
    Each entry's `base` template is the managed-resource shape with
    static literals filled in; `patches` wire variable refs into
    forProvider fields; cross-resource refs and dynamic blocks are
    flagged as TODO so the converter author can fill them in
    deliberately rather than silently producing wrong output.

Run:

    tflens export ./fixture | python3 generator.py > xrd-and-composition.yaml

The output is a multi-document YAML file (XRD + Composition separated
by `---`).

No third-party dependencies — just the Python standard library.
"""

import json
import re
import sys


# ----------------------------------------------------------------------
# Mapping table: Terraform resource type → Upbound provider-aws CRD.
# Production converters would pull this from Upbound's CRD catalogue
# (https://marketplace.upbound.io/providers/upbound/provider-aws);
# curated here so the example stays self-contained.

UPBOUND_MAPPING = {
    "aws_iam_role": {
        "apiVersion": "iam.aws.upbound.io/v1beta1",
        "kind": "Role",
        # Upbound's provider-aws auto-generates from the Terraform
        # provider, so attribute names are the camelCased Terraform
        # names (rather than ACK's AWS-API spellings).
        "attr_renames": {
            "name": "name",
            "assume_role_policy": "assumeRolePolicy",
        },
    },
    "aws_eks_cluster": {
        "apiVersion": "eks.aws.upbound.io/v1beta1",
        "kind": "Cluster",
        "attr_renames": {
            "name": "name",
            "role_arn": "roleArn",
            "version": "version",
        },
        "block_renames": {
            "vpc_config": "vpcConfig",
            "access_config": "accessConfig",
            "encryption_config": "encryptionConfig",
        },
    },
}


# ----------------------------------------------------------------------
# Patch builder: turn an export expression into either a literal
# (set directly in the resource base) or a Crossplane patch entry.
#
# Three cases for every expression:
#
#   1. Pure literal (no AST traversals). Value goes directly into the
#      resource template.
#
#   2. Single var.X reference. Emit one patch:
#         - fromFieldPath: spec.parameters.X
#           toFieldPath: spec.forProvider.<attribute path>
#
#   3. Single var.X reference wrapped in format("%s-suffix"). Emit
#      one patch with a string-transform:
#         - fromFieldPath: spec.parameters.X
#           toFieldPath: spec.forProvider.<attribute path>
#           transforms:
#             - type: string
#               string: { fmt: "%s-suffix" }
#
#   4. Cross-resource refs / complex expressions. Emit a `# TODO`
#      placeholder so the converter author handles them deliberately.

def expr_to_patch_or_literal(expr, to_field_path):
    """Returns (literal_value, patch_dict_or_None).

    literal_value is None when the expression should NOT be inlined
    into the base (e.g. it's a var ref). patch_dict_or_None is None
    when the expression is a pure literal that didn't need a patch.
    """
    if expr is None:
        return (None, None)

    ast = expr.get("ast")

    # Pure literal — no traversals anywhere → set directly.
    if not ast_has_traversal(ast):
        if "value" in expr:
            return (expr["value"]["value"], None)
        return (expr.get("text"), None)

    # var.X by itself → one straight patch.
    var_ref = single_var_ref(ast)
    if var_ref is not None:
        return (None, {
            "fromFieldPath": f"spec.parameters.{var_ref}",
            "toFieldPath": to_field_path,
        })

    # format("template", var.X) → patch with string-transform.
    fmt_patch = format_to_patch(ast, to_field_path)
    if fmt_patch is not None:
        return (None, fmt_patch)

    # Cross-resource ref (resource.foo.attr) → emit a TODO patch with a
    # placeholder. Crossplane has several mechanisms for cross-resource
    # references (named ResourceRef, MatchControllerRef, external-name
    # annotations) — converter author picks per scenario.
    cross_ref = single_cross_resource_ref(ast)
    if cross_ref is not None:
        return (None, {
            "_TODO": (
                f"Cross-resource ref to {cross_ref!r}: pick a Crossplane "
                f"reference style (named ResourceRef on the consuming "
                f"managed resource, or MatchControllerRef, or an external-"
                f"name annotation). The placeholder below sets the field "
                f"to a static empty string so the YAML is valid; replace "
                f"with the appropriate reference."
            ),
            "fromFieldPath": "spec.parameters._TODO_cross_resource_ref",
            "toFieldPath": to_field_path,
        })

    # Unhandled — a complex expression we don't yet translate.
    return (None, {
        "_TODO": (
            f"Complex expression — no patch translation yet. Source: "
            f"{expr.get('text', '<no text>')!r}"
        ),
        "fromFieldPath": "spec.parameters._TODO_complex_expression",
        "toFieldPath": to_field_path,
    })


def ast_has_traversal(ast):
    """Recursive check for any scope_traversal node — same predicate
    the kro POC uses to decide between literal vs CEL emission."""
    if not isinstance(ast, dict):
        return False
    if ast.get("node") in ("scope_traversal", "relative_traversal", "splat"):
        return True
    for v in ast.values():
        if isinstance(v, dict) and ast_has_traversal(v):
            return True
        if isinstance(v, list):
            for item in v:
                if isinstance(item, dict) and ast_has_traversal(item):
                    return True
    return False


def single_var_ref(ast):
    """If ast is exactly `var.X`, return X. Otherwise None."""
    if not isinstance(ast, dict) or ast.get("node") != "scope_traversal":
        return None
    steps = ast.get("traversal") or []
    if (len(steps) == 2 and
            steps[0].get("step") == "root" and steps[0].get("name") == "var" and
            steps[1].get("step") == "attr"):
        return steps[1]["name"]
    return None


def single_cross_resource_ref(ast):
    """If ast is a scope_traversal whose root is a known mapped
    resource type (aws_iam_role, aws_eks_cluster, …), return a
    human-readable reference for the TODO comment. Otherwise None."""
    if not isinstance(ast, dict) or ast.get("node") != "scope_traversal":
        return None
    steps = ast.get("traversal") or []
    if not steps or steps[0].get("step") != "root":
        return None
    root = steps[0]["name"]
    if root in UPBOUND_MAPPING:
        attr_chain = ".".join(s["name"] for s in steps[1:] if s["step"] == "attr")
        return f"{root}.{attr_chain}"
    return None


def format_to_patch(ast, to_field_path):
    """If ast is `format("template", var.X)` with a single arg,
    emit a string-transform patch. Returns None for anything more
    complex (multiple args, non-var args)."""
    if not isinstance(ast, dict) or ast.get("node") != "function_call":
        return None
    if ast.get("name") != "format":
        return None
    args = ast.get("args") or []
    if len(args) != 2:
        # POC: only the simplest "%s" + var.X form. Multi-arg or
        # non-var args fall through to TODO.
        return None
    template = args[0]
    if template.get("node") != "literal_value":
        return None
    tmpl = template["value"]["value"]
    var_ref = single_var_ref(args[1])
    if var_ref is None:
        return None
    return {
        "fromFieldPath": f"spec.parameters.{var_ref}",
        "toFieldPath": to_field_path,
        "transforms": [{
            "type": "string",
            "string": {"fmt": tmpl},
        }],
    }


# ----------------------------------------------------------------------
# Resource emit: walk attributes + nested blocks → (base, patches).

def to_camel(snake):
    parts = snake.split("_")
    return parts[0] + "".join(p.title() for p in parts[1:])


def emit_block_into(out, patches, parent_path, block, mapping):
    """Recursively populate `out` (a dict) and `patches` (a list)
    from a block's attributes + nested blocks. parent_path is the
    field-path prefix for the eventual patch's toFieldPath
    (`spec.forProvider.vpcConfig` for `vpc_config`, etc.)."""
    attr_renames = (mapping or {}).get("attr_renames", {})
    block_renames = (mapping or {}).get("block_renames", {})

    for name, expr in (block.get("attributes") or {}).items():
        renamed = attr_renames.get(name, to_camel(name))
        field_path = f"{parent_path}.{renamed}"
        literal, patch = expr_to_patch_or_literal(expr, field_path)
        if literal is not None:
            out[renamed] = literal
        if patch is not None:
            patches.append(patch)

    for name, instances in (block.get("blocks") or {}).items():
        renamed = block_renames.get(name, to_camel(name))
        if len(instances) == 1:
            sub = {}
            emit_block_into(sub, patches, f"{parent_path}.{renamed}",
                            instances[0], mapping)
            out[renamed] = sub
        else:
            sub_list = []
            for i, inst in enumerate(instances):
                sub = {}
                # Index into the list for the patch path. Crossplane
                # supports list-index patches on the to-side.
                emit_block_into(sub, patches,
                                f"{parent_path}.{renamed}[{i}]",
                                inst, mapping)
                sub_list.append(sub)
            out[renamed] = sub_list


def emit_resource(res):
    """Build one Composition spec.resources[] entry."""
    tf_type = res["type"]
    if tf_type not in UPBOUND_MAPPING:
        return {
            "name": res["name"],
            "_unsupported": (
                f"No Upbound mapping for Terraform resource type {tf_type!r}. "
                f"Add it to UPBOUND_MAPPING (or use a different provider)."
            ),
        }
    mapping = UPBOUND_MAPPING[tf_type]

    # Build the base resource template. Crossplane convention is
    # `spec.forProvider` for user-controlled fields; status comes
    # under spec.providerConfigRef etc. (we don't emit those — they
    # vary per provider configuration choice).
    for_provider = {}
    patches = []

    for name, expr in (res.get("attributes") or {}).items():
        renamed = mapping.get("attr_renames", {}).get(name, to_camel(name))
        field_path = f"spec.forProvider.{renamed}"
        literal, patch = expr_to_patch_or_literal(expr, field_path)
        if literal is not None:
            for_provider[renamed] = literal
        if patch is not None:
            patches.append(patch)

    for name, instances in (res.get("blocks") or {}).items():
        renamed = mapping.get("block_renames", {}).get(name, to_camel(name))
        parent = f"spec.forProvider.{renamed}"
        if len(instances) == 1:
            sub = {}
            emit_block_into(sub, patches, parent, instances[0], mapping)
            for_provider[renamed] = sub
        else:
            sub_list = []
            for i, inst in enumerate(instances):
                sub = {}
                emit_block_into(sub, patches, f"{parent}[{i}]", inst, mapping)
                sub_list.append(sub)
            for_provider[renamed] = sub_list

    if res.get("dynamic_blocks"):
        # POC limitation: classic Compositions can't iterate. Modern
        # Crossplane uses Composition Functions (e.g. function-go-
        # templating, function-patch-and-transform's array-indexed
        # patches) for this. Surface as a TODO so the converter
        # author handles it deliberately.
        for name in (res.get("dynamic_blocks") or {}).keys():
            renamed = mapping.get("block_renames", {}).get(name, to_camel(name))
            for_provider[renamed] = (
                f"# TODO: dynamic block — use a Composition Function "
                f"(function-go-templating or function-patch-and-transform "
                f"with array-indexed patches) to iterate over the source "
                f"list. See README for the recommended approach."
            )

    base = {
        "apiVersion": mapping["apiVersion"],
        "kind": mapping["kind"],
        "spec": {
            "forProvider": for_provider,
        },
    }

    entry = {
        "name": res["name"],
        "base": base,
    }
    if patches:
        entry["patches"] = patches
    return entry


# ----------------------------------------------------------------------
# XRD emit: variables → openAPIV3Schema.properties.spec.properties.parameters.

# Terraform type → openAPI v3 type. Same mapping as the kro POC plus
# array support.
TYPE_MAP = {
    "string": {"type": "string"},
    "number": {"type": "integer"},
    "bool": {"type": "boolean"},
    "list(string)": {"type": "array", "items": {"type": "string"}},
    "set(string)": {"type": "array", "items": {"type": "string"}},
    "list(number)": {"type": "array", "items": {"type": "integer"}},
}


def variable_to_schema(var):
    tf_type = var.get("type", "string")
    schema = dict(TYPE_MAP.get(tf_type, {"type": "string"}))
    if var.get("default") and "value" in var["default"]:
        schema["default"] = var["default"]["value"]["value"]
    return schema


def emit_xrd(xr_kind, variables):
    """Build the CompositeResourceDefinition. Group + plural are
    derived from xr_kind; converter authors typically tweak these
    per project convention."""
    properties = {}
    required = []
    for var in variables:
        properties[var["name"]] = variable_to_schema(var)
        if not (var.get("default") and "value" in var["default"]):
            required.append(var["name"])

    parameters_schema = {
        "type": "object",
        "properties": properties,
    }
    if required:
        parameters_schema["required"] = required

    plural = xr_kind.lower() + "s"
    group = "platform.example.org"
    return {
        "apiVersion": "apiextensions.crossplane.io/v1",
        "kind": "CompositeResourceDefinition",
        "metadata": {
            "name": f"{plural}.{group}",
        },
        "spec": {
            "group": group,
            "names": {
                "kind": xr_kind,
                "plural": plural,
            },
            "claimNames": {
                "kind": f"{xr_kind}Claim",
                "plural": f"{plural}claim",
            },
            "versions": [{
                "name": "v1alpha1",
                "served": True,
                "referenceable": True,
                "schema": {
                    "openAPIV3Schema": {
                        "type": "object",
                        "properties": {
                            "spec": {
                                "type": "object",
                                "properties": {
                                    "parameters": parameters_schema,
                                },
                                "required": ["parameters"],
                            },
                        },
                    },
                },
            }],
        },
    }


# ----------------------------------------------------------------------
# Composition emit.

def emit_composition(xr_kind, resources):
    plural = xr_kind.lower() + "s"
    group = "platform.example.org"
    return {
        "apiVersion": "apiextensions.crossplane.io/v1",
        "kind": "Composition",
        "metadata": {
            "name": f"{plural}.{group}",
        },
        "spec": {
            "compositeTypeRef": {
                "apiVersion": f"{group}/v1alpha1",
                "kind": xr_kind,
            },
            "resources": [emit_resource(r) for r in resources],
        },
    }


# ----------------------------------------------------------------------
# YAML emitter — small hand-roll to avoid a PyYAML dependency.
# Outputs the subset we produce (dict / list / str / int / bool /
# None). Multi-document output uses `\n---\n` between docs.

def to_yaml(value, indent=0):
    pad = "  " * indent
    if isinstance(value, dict):
        if not value:
            return "{}\n"
        out = []
        for k, v in value.items():
            if isinstance(v, (dict, list)) and v:
                out.append(f"{pad}{k}:\n{to_yaml(v, indent + 1)}")
            else:
                out.append(f"{pad}{k}: {scalar_yaml(v)}\n")
        return "".join(out)
    if isinstance(value, list):
        if not value:
            return f"{pad}[]\n"
        out = []
        for item in value:
            if isinstance(item, (dict, list)) and item:
                inner = to_yaml(item, indent + 1).lstrip()
                out.append(f"{pad}- {inner}")
            else:
                out.append(f"{pad}- {scalar_yaml(item)}\n")
        return "".join(out)
    return f"{pad}{scalar_yaml(value)}\n"


def scalar_yaml(v):
    if v is None:
        return "null"
    if isinstance(v, bool):
        return "true" if v else "false"
    if isinstance(v, (int, float)):
        return str(v)
    s = str(v)
    if (s in ("true", "false", "null", "yes", "no") or
            re.search(r'[:#\n"\'\[\]{},&*]', s) or
            s.strip() != s):
        return json.dumps(s)
    return s


# ----------------------------------------------------------------------
# Main: read export JSON from stdin, emit XRD --- Composition on stdout.

def main():
    export = json.load(sys.stdin)
    module = export["root"]["module"]
    # XR kind defaults to "Stack" — converter authors typically pick
    # a domain-specific name (e.g. EKSCluster, NetworkStack).
    xr_kind = "Stack"

    xrd = emit_xrd(xr_kind, module.get("variables", []))
    composition = emit_composition(xr_kind, module.get("resources", []))

    sys.stdout.write(to_yaml(xrd))
    sys.stdout.write("---\n")
    sys.stdout.write(to_yaml(composition))


if __name__ == "__main__":
    main()
