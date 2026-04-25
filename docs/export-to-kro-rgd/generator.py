#!/usr/bin/env python3
"""
Worked example: convert a `tflens export` JSON document into a kro
ResourceGraphDefinition (RGD) targeting AWS Controllers for Kubernetes
(ACK) custom resources.

This is a deliberately small POC — it covers the patterns that matter
(variable refs, cross-resource refs, format(), nested blocks, outputs)
for two Terraform resource types (aws_iam_role, aws_eks_cluster) so the
mapping table stays readable. A production converter would extend the
ACK_MAPPING table per resource and broaden the AST walker as needed.

Run:

    tflens export ./fixture | python3 generator.py > rgd.yaml

No third-party dependencies — just the Python standard library.

The translation model:

    Terraform                       kro / ACK
    -----------------------------   ----------------------------------
    variable "X"                →   spec.schema.spec.X (with type + default)
    resource "T" "N" { ... }   →   spec.resources[].id = N
                                   .template = ACK CRD with renamed attrs
    var.X                      →   ${schema.spec.X}
    resource_type.foo.arn      →   ${resources.foo.status.ackResourceMetadata.arn}
    format("%s-y", var.x)      →   "${schema.spec.x + \"-y\"}"
    snake_case_attr            →   camelCaseAttr (ACK convention)
    nested_block { ... }       →   nested_block: { ... } (recursive)
    output "X" { value = ... } →   spec.schema.status.X (best-effort)

Anything we can't resolve becomes an embedded `${...}` CEL expression
(the AST walker emits these). Static literals (the `value` field on
ExportExpression) are emitted directly as YAML/JSON values.
"""

import json
import re
import sys

# ----------------------------------------------------------------------
# Mapping table: Terraform resource type → ACK CRD shape + attr renames.
# Production converters would generate this from the ACK code-generator
# specs (https://github.com/aws-controllers-k8s/code-generator). Curated
# by hand here so the example stays self-contained.

ACK_MAPPING = {
    "aws_iam_role": {
        "apiVersion": "iam.services.k8s.aws/v1alpha1",
        "kind": "Role",
        # Per-attribute renames (snake_case → ACK's preferred camelCase
        # spelling). For ARN-bearing fields, ACK uses ARN not Arn.
        "attr_renames": {
            "name": "name",
            "assume_role_policy": "assumeRolePolicyDocument",
        },
    },
    "aws_eks_cluster": {
        "apiVersion": "eks.services.k8s.aws/v1alpha1",
        "kind": "Cluster",
        "attr_renames": {
            "name": "name",
            "role_arn": "roleARN",
            "version": "version",
        },
        "block_renames": {
            "vpc_config": "resourcesVPCConfig",
            "access_config": "accessConfig",
            "encryption_config": "encryptionConfig",
        },
    },
    "aws_security_group": {
        "apiVersion": "ec2.services.k8s.aws/v1alpha1",
        "kind": "SecurityGroup",
        "attr_renames": {
            "name": "name",
            "description": "description",
            "vpc_id": "vpcID",
        },
        "block_renames": {
            "ingress": "ingressRules",
            "egress": "egressRules",
            "tags": "tags",
        },
    },
}

# Standard ACK convention: every ACK resource exposes its AWS ARN under
# status.ackResourceMetadata.arn. Cross-resource ARN refs in Terraform
# (resource.foo.arn) translate to that path.
ACK_ARN_PATH = "status.ackResourceMetadata.arn"


# ----------------------------------------------------------------------
# Expression walker: AST → CEL string.
#
# The export's `ast` field on every ExportExpression is a tagged tree
# whose node kinds are documented in pkg/render/export_ast.go. We map
# each node to its CEL equivalent, recursing as needed.

def expr_to_cel(ast):
    """Convert one AST node to a CEL expression string."""
    if ast is None:
        return '""'
    node = ast.get("node")

    if node == "literal_value":
        v = ast["value"]
        if v["type"] == "string":
            return json.dumps(v["value"])  # JSON string is valid CEL string
        if v["type"] in ("number", "bool"):
            return json.dumps(v["value"])
        # Fall through for collections — JSON literal is also valid CEL.
        return json.dumps(v["value"])

    if node == "scope_traversal":
        return traversal_to_cel(ast["traversal"])

    if node == "function_call":
        return call_to_cel(ast["name"], ast["args"])

    if node == "binary_op":
        return f"({expr_to_cel(ast['lhs'])} {ast['op']} {expr_to_cel(ast['rhs'])})"

    if node == "unary_op":
        return f"({ast['op']}{expr_to_cel(ast['value'])})"

    if node == "conditional":
        return (f"({expr_to_cel(ast['condition'])} ? "
                f"{expr_to_cel(ast['true'])} : "
                f"{expr_to_cel(ast['false'])})")

    if node == "tuple_cons":
        elems = ", ".join(expr_to_cel(e) for e in ast["elements"])
        return f"[{elems}]"

    if node == "object_cons":
        items = ", ".join(
            f"{expr_to_cel(item['key'])}: {expr_to_cel(item['value'])}"
            for item in ast["items"]
        )
        return f"{{{items}}}"

    if node == "template":
        # CEL has no template syntax; concatenate parts with +.
        return " + ".join(expr_to_cel(p) for p in ast["parts"])

    if node == "index":
        return f"{expr_to_cel(ast['collection'])}[{expr_to_cel(ast['key'])}]"

    if node == "splat":
        # CEL doesn't have a direct splat; rewrite as a list comprehension.
        # For a converter, [for x in source : x.attr] is the closest.
        return (f"{expr_to_cel(ast['source'])}.map(x, "
                f"{expr_to_cel(ast['each'])})")

    return f'"<unsupported_ast: {node}>"'


def traversal_to_cel(steps):
    """Convert a traversal (root + attr/index chain) to a CEL reference.

    Handles three cases that matter for the converter:
      - var.X        → schema.spec.X
      - local.X      → (we'd need a local lookup table; emit a marker)
      - resource_type.name.attr → resources.<name>.status.<attr>
        with the special case `.arn` → ACK's status.ackResourceMetadata.arn.
    """
    if not steps or steps[0]["step"] != "root":
        return '"<error: traversal must start at root>"'
    root = steps[0]["name"]
    rest = steps[1:]

    if root == "var" and rest and rest[0]["step"] == "attr":
        # Terraform var name → schema.spec key (we leave names snake_case
        # in the schema for round-trip clarity; production converters
        # would camelCase them).
        return f"schema.spec.{rest[0]['name']}"

    if root == "local" and rest and rest[0]["step"] == "attr":
        # POC marker — a real converter would inline locals into their
        # consumers (since RGDs don't have a local concept).
        return f'"<local: {rest[0]["name"]}>"'

    if root in ACK_MAPPING and rest and rest[0]["step"] == "attr":
        res_name = rest[0]["name"]
        attr_chain = [s["name"] for s in rest[1:] if s["step"] == "attr"]
        # The .arn shortcut: every ACK resource exposes ARN at the
        # standard path, so resource_type.foo.arn → ACK_ARN_PATH.
        if attr_chain == ["arn"]:
            return f"resources.{res_name}.{ACK_ARN_PATH}"
        path = ".".join(attr_chain)
        return f"resources.{res_name}.status.{path}"

    return f'"<unknown_ref: {root}>"'


def call_to_cel(name, args):
    """Map a Terraform stdlib function call to its CEL equivalent."""
    if name == "format":
        # format("%s-y", var.x) → "%s-y" with arg substitution → CEL
        # string concat. Production converter: parse the format spec
        # properly. POC: handle %s and %d on literal-string templates.
        if not args or args[0].get("node") != "literal_value":
            return f'"<format_with_dynamic_template>"'
        tmpl = args[0]["value"]["value"]
        out = format_to_cel(tmpl, args[1:])
        return out

    if name == "length":
        return f"size({expr_to_cel(args[0])})"

    if name == "lower":
        return f"({expr_to_cel(args[0])}).lowerAscii()"

    if name == "upper":
        return f"({expr_to_cel(args[0])}).upperAscii()"

    if name == "concat":
        # CEL list concatenation via +.
        return " + ".join(expr_to_cel(a) for a in args)

    if name == "jsonencode":
        # kro ships json.marshal / json.unmarshal as first-class CEL
        # functions in its CEL library, so jsonencode round-trips
        # cleanly. See:
        # https://github.com/kubernetes-sigs/kro/blob/main/pkg/cel/library/json.go
        return f"json.marshal({expr_to_cel(args[0])})"

    if name == "jsondecode":
        return f"json.unmarshal({expr_to_cel(args[0])})"

    return f'"<unsupported_function: {name}>"'


def format_to_cel(tmpl, args):
    """Convert a printf-style template + args into CEL string concat."""
    parts = []
    arg_idx = 0
    i = 0
    buf = ""
    while i < len(tmpl):
        if tmpl[i] == "%" and i + 1 < len(tmpl) and tmpl[i + 1] in "sd":
            if buf:
                parts.append(json.dumps(buf))
                buf = ""
            if arg_idx >= len(args):
                parts.append('"<format_arg_missing>"')
            else:
                parts.append(expr_to_cel(args[arg_idx]))
                arg_idx += 1
            i += 2
        else:
            buf += tmpl[i]
            i += 1
    if buf:
        parts.append(json.dumps(buf))
    return "(" + " + ".join(parts) + ")"


# ----------------------------------------------------------------------
# Expression → emitted value. The {text, value?, ast?} triple from the
# export gives us three options:
#
#   - value present  → emit the literal directly (cleanest for converters)
#   - ast present    → wrap a CEL expression in ${...} (kro syntax)
#   - text only      → fallback (shouldn't happen for known nodes)

def expr_to_emit(expr):
    """Choose between literal value and CEL string for one expression.

    Subtle but important: an expression like `name = var.cluster_name`
    has BOTH a `value` ("demo", because the variable defaults) AND an
    `ast` (a scope_traversal to var.cluster_name). Emitting the value
    would lose the parameterisation — instances of this RGD couldn't
    override `cluster_name` at apply time. So whenever the AST contains
    any scope_traversal, prefer the CEL form. Pure-literal expressions
    (`subnet_ids = ["subnet-aaaa", "subnet-bbbb"]`) keep the clean
    structured value.
    """
    if expr is None:
        return None
    if "ast" in expr and ast_has_traversal(expr["ast"]):
        return "${" + expr_to_cel(expr["ast"]) + "}"
    if "value" in expr:
        return expr["value"]["value"]
    if "ast" in expr:
        return "${" + expr_to_cel(expr["ast"]) + "}"
    return expr.get("text", "")


def ast_has_traversal(ast):
    """Recursively check whether an AST node references anything via
    scope_traversal — a marker that the expression depends on a
    variable, local, or other-resource attribute and therefore needs to
    stay parameterised in the generated RGD."""
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


def to_camel(snake):
    """snake_case → camelCase. Used for attributes the mapping table
    doesn't override explicitly."""
    parts = snake.split("_")
    return parts[0] + "".join(p.title() for p in parts[1:])


# ----------------------------------------------------------------------
# Resource emit: walk a resource's attributes + nested blocks → CRD spec.

def emit_block(block, mapping):
    """Recursively convert an export block into a YAML-ish dict."""
    out = {}
    for name, expr in (block.get("attributes") or {}).items():
        out[to_camel(name)] = expr_to_emit(expr)
    block_renames = (mapping or {}).get("block_renames", {})
    for name, instances in (block.get("blocks") or {}).items():
        renamed = block_renames.get(name, to_camel(name))
        if len(instances) == 1:
            out[renamed] = emit_block(instances[0], mapping)
        else:
            out[renamed] = [emit_block(b, mapping) for b in instances]
    for name, instances in (block.get("dynamic_blocks") or {}).items():
        renamed = block_renames.get(name, to_camel(name))
        # Multiple dynamic blocks with the same name? Concatenate their
        # CEL expressions. The single-instance case is the common one.
        out[renamed] = [emit_dynamic(d, name, mapping) for d in instances]
        if len(out[renamed]) == 1:
            out[renamed] = out[renamed][0]
    return out


def emit_dynamic(d, block_label, mapping):
    """Translate one dynamic-block instance into the value-side CEL
    expression that kro RGDs use for list construction:

        ${ <for_each_source>.map(item, { "k": item.x, ... }) }

    The .map() variant is what real kro examples use for list
    transformations (see examples/aws/aws-accounts-factory/01-network-stack.yaml
    in the kro repo: `publicSubnetIds: ${publicSubnets.map(s, s.status.subnetID)}`).
    We rename the iterator to `item` for readability — the original
    Terraform iterator name (block label or explicit) is shadowed by
    the .map() binding.
    """
    iterator = d.get("iterator") or block_label
    # for_each source as CEL — even if statically resolvable, we want
    # the schema-spec ref so RGD instances can override at apply time.
    for_each = d.get("for_each") or {}
    if for_each.get("ast") and ast_has_traversal(for_each["ast"]):
        source_cel = expr_to_cel(for_each["ast"])
    elif "value" in for_each:
        # Fully static for_each — emit the literal as a CEL list literal.
        source_cel = json.dumps(for_each["value"]["value"])
    elif for_each.get("ast"):
        source_cel = expr_to_cel(for_each["ast"])
    else:
        source_cel = '""'

    # Build the per-iteration object expression from the content body.
    item_expr = content_to_cel_object(d.get("content", {}), iterator, mapping)

    return "${" + f"{source_cel}.map(item, {item_expr})" + "}"


def content_to_cel_object(content, iterator, mapping):
    """Render a dynamic-block content body as a CEL object-literal
    expression `{"k": v, ...}`. Attributes use the camelCased name as
    the key. Iterator references inside expressions are rewritten via
    ast_to_cel_with_iterator so `<iterator>.value.field` becomes
    `item.field`. Static (non-iterator) sub-expressions go through
    expr_to_cel verbatim."""
    parts = []
    for name, expr in sorted((content.get("attributes") or {}).items()):
        cel_value = expr_to_cel_with_iterator(expr, iterator)
        parts.append(f'"{to_camel(name)}": {cel_value}')
    # Nested static blocks inside content — emit as a nested CEL object
    # under the camelCased block name. (Repeated nested blocks inside
    # content are uncommon for dynamic; production converters might
    # need to extend this.)
    block_renames = (mapping or {}).get("block_renames", {})
    for name, instances in (content.get("blocks") or {}).items():
        renamed = block_renames.get(name, to_camel(name))
        if len(instances) == 1:
            inner = content_to_cel_object(instances[0], iterator, mapping)
            parts.append(f'"{renamed}": {inner}')
        else:
            inner = "[" + ", ".join(
                content_to_cel_object(b, iterator, mapping) for b in instances
            ) + "]"
            parts.append(f'"{renamed}": {inner}')
    return "{" + ", ".join(parts) + "}"


def expr_to_cel_with_iterator(expr, iterator):
    """Render an expression as a CEL string suitable for use inside
    the .map() body. References through the iterator variable become
    `item.X` refs; everything else uses the standard expr_to_cel."""
    if expr is None:
        return '""'
    if "ast" in expr:
        return ast_to_cel_with_iterator(expr["ast"], iterator)
    if "value" in expr:
        v = expr["value"]
        return json.dumps(v["value"]) if isinstance(v.get("value"), (str, int, float, bool)) \
            else json.dumps(v.get("value"))
    return json.dumps(expr.get("text", ""))


def ast_to_cel_with_iterator(ast, iterator):
    """Custom CEL emit that rewrites <iterator>.value.X → item.X
    (and <iterator>.key → item_key). Falls through to expr_to_cel for
    everything else, walking nested expressions recursively so an
    iterator ref buried inside a function call or template is still
    rewritten correctly."""
    if isinstance(ast, dict) and ast.get("node") == "scope_traversal":
        steps = ast.get("traversal") or []
        if steps and steps[0].get("step") == "root" and steps[0].get("name") == iterator:
            rest = steps[1:]
            if rest and rest[0].get("step") == "attr" and rest[0].get("name") == "value":
                tail = ".".join(s["name"] for s in rest[1:] if s["step"] == "attr")
                return "item" + (("." + tail) if tail else "")
            if rest and rest[0].get("step") == "attr" and rest[0].get("name") == "key":
                return "item_key"
            return "item"
    # Recurse into compound nodes so iterator refs deep inside a
    # function call or binary op also get rewritten.
    if isinstance(ast, dict):
        node = ast.get("node")
        if node == "function_call":
            args = ", ".join(ast_to_cel_with_iterator(a, iterator) for a in ast["args"])
            return f"{ast['name']}({args})"
        if node == "binary_op":
            return (f"({ast_to_cel_with_iterator(ast['lhs'], iterator)} "
                    f"{ast['op']} "
                    f"{ast_to_cel_with_iterator(ast['rhs'], iterator)})")
        if node == "conditional":
            return (f"({ast_to_cel_with_iterator(ast['condition'], iterator)} ? "
                    f"{ast_to_cel_with_iterator(ast['true'], iterator)} : "
                    f"{ast_to_cel_with_iterator(ast['false'], iterator)})")
        if node == "template":
            return " + ".join(ast_to_cel_with_iterator(p, iterator) for p in ast["parts"])
    # Falls through to standard CEL emission for everything else.
    return expr_to_cel(ast)


def emit_resource(res):
    """Build one RGD resources[] entry from an export resource."""
    tf_type = res["type"]
    if tf_type not in ACK_MAPPING:
        return {
            "id": res["name"],
            "_unsupported": (
                f"No ACK mapping for Terraform resource type {tf_type!r}. "
                f"Add it to ACK_MAPPING."),
        }
    mapping = ACK_MAPPING[tf_type]

    spec = {}
    attr_renames = mapping.get("attr_renames", {})
    for name, expr in (res.get("attributes") or {}).items():
        renamed = attr_renames.get(name, to_camel(name))
        spec[renamed] = expr_to_emit(expr)

    block_renames = mapping.get("block_renames", {})
    for name, instances in (res.get("blocks") or {}).items():
        renamed = block_renames.get(name, to_camel(name))
        if len(instances) == 1:
            spec[renamed] = emit_block(instances[0], mapping)
        else:
            spec[renamed] = [emit_block(b, mapping) for b in instances]

    for name, instances in (res.get("dynamic_blocks") or {}).items():
        renamed = block_renames.get(name, to_camel(name))
        rendered = [emit_dynamic(d, name, mapping) for d in instances]
        spec[renamed] = rendered if len(rendered) > 1 else rendered[0]

    return {
        "id": res["name"],
        "template": {
            "apiVersion": mapping["apiVersion"],
            "kind": mapping["kind"],
            "metadata": {"name": spec.get("name", res["name"])},
            "spec": spec,
        },
    }


# ----------------------------------------------------------------------
# Schema emit: variables → RGD spec.schema.spec.

# Terraform type → kro schema type. Kro uses simple type strings ("string",
# "integer", "boolean", "[]string", …); production converters need to
# handle the full type-constraint grammar. POC handles the common cases.
TYPE_MAP = {
    "string": "string",
    "number": "integer",
    "bool": "boolean",
    "list(string)": "[]string",
    "set(string)": "[]string",
}


def emit_schema(variables):
    out = {}
    for var in variables:
        tf_type = var.get("type", "string")
        kro_type = TYPE_MAP.get(tf_type, "string")
        if var.get("default") and "value" in var["default"]:
            default = var["default"]["value"]["value"]
            out[var["name"]] = f"{kro_type} | default={json.dumps(default)}"
        else:
            out[var["name"]] = f"{kro_type} | required=true"
    return out


def emit_status(outputs):
    """Outputs become RGD status fields. Best-effort: if the output value
    is a scope_traversal into a known resource, we map it; otherwise we
    emit the CEL expression as the field value."""
    out = {}
    for o in outputs:
        out[o["name"]] = expr_to_emit(o.get("value"))
    return out


# ----------------------------------------------------------------------
# YAML emitter — small hand-roll to avoid a PyYAML dependency. JSON is
# also valid YAML, but a properly-indented YAML output is easier for
# operators to read alongside hand-written kro RGDs.

def to_yaml(value, indent=0):
    """Serialise a Python value as YAML. Handles the subset we produce:
    dict, list, str, int, bool, None. CEL ${...} strings are quoted to
    survive YAML parsing intact."""
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
    # Quote strings that look like CEL refs, special YAML, or contain
    # characters that need escaping. Permissive on purpose — over-
    # quoting is safer than under-quoting.
    if (s.startswith("${") or
            s in ("true", "false", "null", "yes", "no") or
            re.search(r'[:#\n"\'\[\]{},&*]', s) or
            s.strip() != s):
        return json.dumps(s)
    return s


# ----------------------------------------------------------------------
# Main: read export JSON from stdin, emit RGD YAML on stdout.

def main():
    export = json.load(sys.stdin)
    module = export["root"]["module"]

    rgd = {
        "apiVersion": "kro.run/v1alpha1",
        "kind": "ResourceGraphDefinition",
        "metadata": {
            "name": "converted-from-terraform",
        },
        "spec": {
            "schema": {
                "apiVersion": "v1alpha1",
                "kind": "ConvertedFromTerraform",
                "spec": emit_schema(module.get("variables", [])),
                "status": emit_status(module.get("outputs", [])),
            },
            "resources": [emit_resource(r) for r in module.get("resources", [])],
        },
    }

    sys.stdout.write(to_yaml(rgd))


if __name__ == "__main__":
    main()
