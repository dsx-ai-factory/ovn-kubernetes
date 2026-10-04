#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Generate values.schema.json from values.yaml plus every subchart's values.yaml.

Mirrors how Helm actually resolves values: each subchart key in the parent
values.yaml is deep-merged over that subchart's own values.yaml defaults
(parent wins on conflicts), then nested under the subchart name - matching
what `helm template`/`helm install` would see.

Descriptions come from `# -- ...` comments immediately preceding a key, the
same convention helm-docs uses. Run from the chart root:

    python3 scripts/gen-values-schema.py > values.schema.json
"""
import json
import re
import sys
from pathlib import Path

import yaml

CHART_ROOT = Path(__file__).resolve().parent.parent
ROOT_VALUES = CHART_ROOT / "values.yaml"
CHARTS_DIR = CHART_ROOT / "charts"

DESC_START_RE = re.compile(r"^(?P<indent>\s*)# -- (?P<text>.+)$")
DESC_CONT_RE = re.compile(r"^(?P<indent>\s*)# (?P<text>.+)$")
KEY_RE = re.compile(r"^(?P<indent>\s*)(?P<key>[^\s:#][^:]*):")


def extract_descriptions(path: Path) -> dict:
    """Map a dotted key path (e.g. "global.enableEgressIp") to its `# -- ` comment,
    joining wrapped continuation lines (`#   more text`) at the same indent."""
    descriptions = {}
    stack = []  # list of (indent, key)
    pending = None  # (indent, [text parts])
    for line in path.read_text().splitlines():
        m = DESC_START_RE.match(line)
        if m:
            pending = (len(m.group("indent")), [m.group("text").strip()])
            continue
        if pending is not None:
            m = DESC_CONT_RE.match(line)
            if m and len(m.group("indent")) == pending[0]:
                pending[1].append(m.group("text").strip())
                continue
        m = KEY_RE.match(line)
        if m:
            indent = len(m.group("indent"))
            key = m.group("key").strip().strip('"').strip("'")
            while stack and stack[-1][0] >= indent:
                stack.pop()
            stack.append((indent, key))
            if pending is not None and pending[0] <= indent:
                dotted = ".".join(k for _, k in stack)
                descriptions[dotted] = " ".join(pending[1])
            pending = None
        elif line.strip() and not line.strip().startswith("#"):
            pending = None
    return descriptions


def deep_merge(base, override):
    if isinstance(base, dict) and isinstance(override, dict):
        merged = dict(base)
        for k, v in override.items():
            merged[k] = deep_merge(base.get(k), v) if k in base else v
        return merged
    return override


def load_yaml(path: Path):
    if not path.exists():
        return {}
    return yaml.safe_load(path.read_text()) or {}


def infer_schema(value, path, descriptions):
    entry = {}
    if isinstance(value, bool):
        entry["type"] = "boolean"
    elif isinstance(value, int):
        entry["type"] = "integer"
    elif isinstance(value, float):
        entry["type"] = "number"
    elif isinstance(value, str):
        entry["type"] = "string"
    elif value is None:
        entry["type"] = ["string", "null"]
    elif isinstance(value, list):
        entry["type"] = ["array", "null"]
    elif isinstance(value, dict):
        entry["type"] = "object"
        props = {}
        for k, v in value.items():
            child_path = f"{path}.{k}" if path else k
            props[k] = infer_schema(v, child_path, descriptions)
        entry["properties"] = props
    else:
        entry["type"] = "string"

    desc = descriptions.get(path)
    if desc:
        entry["description"] = desc
    return entry


def main():
    root_descriptions = extract_descriptions(ROOT_VALUES)
    root_values = load_yaml(ROOT_VALUES)

    all_descriptions = dict(root_descriptions)
    merged = dict(root_values)

    for sub_dir in sorted(CHARTS_DIR.iterdir()):
        name = sub_dir.name
        sub_values_path = sub_dir / "values.yaml"
        if not sub_values_path.exists():
            continue
        sub_values = load_yaml(sub_values_path)
        sub_descriptions = extract_descriptions(sub_values_path)
        for k, v in sub_descriptions.items():
            all_descriptions.setdefault(f"{name}.{k}", v)

        override = root_values.get(name, {}) or {}
        merged[name] = deep_merge(sub_values, override)

    schema = {
        "$schema": "https://json-schema.org/draft-07/schema#",
        "title": "ovn-kubernetes values",
        "type": "object",
        "properties": {
            k: infer_schema(v, k, all_descriptions) for k, v in merged.items()
        },
    }
    json.dump(schema, sys.stdout, indent=2)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()
