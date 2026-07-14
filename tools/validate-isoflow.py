#!/usr/bin/env python3
"""Validate the Isoflow export used by this repository."""

from __future__ import annotations

import json
import sys
from pathlib import Path


MAX_DESCRIPTION_CHARS = 250


def block_label(block: dict[str, object]) -> str:
    name = block.get("name")
    block_id = block.get("id")
    if isinstance(name, str):
        return name
    if isinstance(block_id, str):
        return block_id
    return "<unnamed block>"


def main() -> int:
    path = Path(sys.argv[1]) if len(sys.argv) > 1 else Path("isoflow.json")

    try:
        data = json.loads(path.read_text())
    except FileNotFoundError:
        print(f"isoflow validation failed: {path} does not exist", file=sys.stderr)
        return 1
    except json.JSONDecodeError as err:
        print(f"isoflow validation failed: invalid JSON: {err}", file=sys.stderr)
        return 1

    c4 = data.get("c4")
    if not isinstance(c4, dict):
        print("isoflow validation failed: missing c4 object", file=sys.stderr)
        return 1

    blocks = c4.get("blocks")
    relationships = c4.get("relationships")
    if not isinstance(blocks, list) or not isinstance(relationships, list):
        print("isoflow validation failed: c4.blocks and c4.relationships must be arrays", file=sys.stderr)
        return 1

    block_names = {
        block.get("id"): block_label(block)
        for block in blocks
        if isinstance(block, dict) and isinstance(block.get("id"), str)
    }

    failures: list[str] = []

    for block in blocks:
        if not isinstance(block, dict):
            continue
        description = block.get("description")
        if isinstance(description, str) and len(description) > MAX_DESCRIPTION_CHARS:
            failures.append(
                f"block {block_label(block)!r} description is {len(description)} chars"
            )

    for relationship in relationships:
        if not isinstance(relationship, dict):
            continue
        description = relationship.get("description")
        if not isinstance(description, str) or len(description) <= MAX_DESCRIPTION_CHARS:
            continue
        source = block_names.get(relationship.get("source"), relationship.get("source"))
        target = block_names.get(relationship.get("target"), relationship.get("target"))
        failures.append(
            f"relationship {source!r} -> {target!r} description is {len(description)} chars"
        )

    if failures:
        print("isoflow validation failed:", file=sys.stderr)
        for failure in failures:
            print(f"- {failure}", file=sys.stderr)
        return 1

    print(f"isoflow validation passed: {path}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
