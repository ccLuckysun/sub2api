#!/usr/bin/env python3
"""Read-only, stdlib-only checks for Grafana files and their Compose inventory.

Run from any directory; --root selects a checkout (also useful for fixture tests).
The archived dashboards/backup tree is not an active inventory, but is checked if
actually exposed to a provider. macOS metadata is forbidden throughout monitoring,
including ignored/untracked files and backup directories.

Only the relevant YAML block mappings/sequences are read, not arbitrary YAML.
Both short and long Compose volume syntax and quoted scalars are supported.
Unsupported inline collections/aliases in these sections fail closed, with a
request to use block syntax, rather than silently skipping inventory checks.
"""

import argparse
from collections import Counter
import json
import os
from pathlib import Path, PurePosixPath
import sys


class ConfigError(ValueError):
    pass


def scalar(text):
    text = text.strip()
    if not text:
        return ""
    if text.startswith('"'):
        try:
            value = json.loads(text)
        except ValueError as exc:
            raise ConfigError(f"invalid double-quoted scalar: {text}") from exc
        if not isinstance(value, str):
            raise ConfigError(f"expected string scalar: {text}")
        return value
    if text.startswith("'"):
        if len(text) < 2 or not text.endswith("'"):
            raise ConfigError(f"invalid single-quoted scalar: {text}")
        return text[1:-1].replace("''", "'")
    if text[0] in "[{*&!>|":
        raise ConfigError(f"unsupported YAML value {text!r}; use block syntax and literal paths")
    return text


def yaml_lines(path):
    """Tokenize indentation and comments without interpreting unrelated YAML."""
    result = []
    for number, raw in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        prefix = raw[:len(raw) - len(raw.lstrip())]
        if "\t" in prefix:
            raise ConfigError(f"{path}:{number}: YAML indentation must use spaces")
        quote = None
        escaped = False
        end = len(raw)
        for index, char in enumerate(raw):
            if escaped:
                escaped = False
            elif char == "\\" and quote == '"':
                escaped = True
            elif quote:
                if char == quote:
                    quote = None
            elif char in "\"'" and (index == 0 or raw[index - 1].isspace() or raw[index - 1] == ":"):
                quote = char
            elif char == "#" and (index == 0 or raw[index - 1].isspace()):
                end = index
                break
        text = raw[:end].strip()
        if text and text not in ("---", "..."):
            result.append((len(prefix), text))
    return result


def field(lines, name, required=True):
    """Return the scalar and indented block of a direct mapping child."""
    if lines:
        level = min(indent for indent, _ in lines)
        matches = []
        for index, (indent, text) in enumerate(lines):
            if indent != level:
                continue
            key, colon, value = text.partition(":")
            if colon and scalar(key) == name:
                stop = index + 1
                while stop < len(lines) and lines[stop][0] > level:
                    stop += 1
                matches.append((value.strip(), lines[index + 1:stop]))
        if len(matches) > 1:
            raise ConfigError(f"duplicate YAML key: {name}")
        if matches:
            return matches[0]
    if required:
        raise ConfigError(f"missing YAML mapping key: {name}")
    return None


def block(lines, name):
    value, children = field(lines, name)
    if value or not children:
        raise ConfigError(f"{name} must use a nonempty YAML block")
    return children


def sequence(lines):
    if not lines:
        raise ConfigError("expected a nonempty YAML sequence")
    level = min(indent for indent, _ in lines)
    starts = [index for index, (indent, _) in enumerate(lines) if indent == level]
    for pos, start in enumerate(starts):
        text = lines[start][1]
        if text != "-" and not text.startswith("- "):
            raise ConfigError(f"expected YAML sequence item, got {text!r}")
        stop = starts[pos + 1] if pos + 1 < len(starts) else len(lines)
        yield text[1:].strip(), lines[start + 1:stop], level + 2


def compose_mounts(path):
    lines = block(block(block(yaml_lines(path), "services"), "grafana"), "volumes")
    mounts = []
    for text, children, indent in sequence(lines):
        # A long-syntax item begins with a mapping key, not a host:container path.
        key, colon, rest = text.partition(":")
        if (not text and children) or (colon and key in ("type", "source", "target", "read_only")
                                       and (not rest or rest.startswith(" "))):
            mapping = ([(indent, text)] if text else []) + children
            kind = field(mapping, "type", required=False)
            if kind and scalar(kind[0]) != "bind":
                continue
            source = scalar(field(mapping, "source")[0])
            target = scalar(field(mapping, "target")[0])
            readonly = field(mapping, "read_only", required=False)
            readonly = readonly is not None and scalar(readonly[0]).lower() == "true"
        else:
            if children:
                raise ConfigError("unexpected nested short-syntax volume")
            parts = scalar(text).split(":")
            if len(parts) not in (2, 3):
                raise ConfigError(f"unsupported volume: {text!r}; use long syntax")
            source, target = parts[:2]
            readonly = len(parts) == 3 and "ro" in parts[2].split(",")
        if not source.startswith((".", "/")):
            # Named data volumes do not describe repository configuration.
            continue
        if "$" in source or "$" in target:
            raise ConfigError("Grafana config bind paths must be literal, not interpolated")
        if not target.startswith("/"):
            raise ConfigError(f"volume target must be absolute: {target}")
        mounts.append(((path.parent / source).resolve(), PurePosixPath(target), readonly))
    return mounts


def provider_paths(path):
    if path.suffix == ".json":
        data = json.loads(path.read_text(encoding="utf-8"))
        if not isinstance(data, dict) or not isinstance(data.get("providers"), list):
            raise ConfigError("expected an object containing a providers array")
        paths = []
        for item in data["providers"]:
            if not isinstance(item, dict):
                raise ConfigError("each provider must be an object")
            if item.get("type") == "file":
                options = item.get("options")
                if not isinstance(options, dict) or not isinstance(options.get("path"), str):
                    raise ConfigError("file provider options.path must be a string")
                paths.append(PurePosixPath(options["path"]))
        return paths
    paths = []
    for text, children, indent in sequence(block(yaml_lines(path), "providers")):
        mapping = ([(indent, text)] if text else []) + children
        if scalar(field(mapping, "type")[0]) == "file":
            paths.append(PurePosixPath(scalar(field(block(mapping, "options"), "path")[0])))
    return paths


def strings(value):
    if isinstance(value, str):
        yield value
    elif isinstance(value, dict):
        for item in value.values():
            yield from strings(item)
    elif isinstance(value, list):
        for item in value:
            yield from strings(item)


def no_duplicate_keys(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON key {key!r}")
        result[key] = value
    return result


def reject_constant(value):
    raise ValueError(f"non-JSON numeric constant {value}")


def lint(root):
    root = Path(root).resolve()
    monitoring = root / "deploy/monitoring"
    grafana = monitoring / "grafana"
    dashboards = grafana / "dashboards"
    provisioning = grafana / "provisioning"
    errors = []

    def error(path, message):
        try:
            label = str(path.relative_to(root))
        except ValueError:
            label = str(path)
        errors.append(f"{label}: {message}")

    if not monitoring.is_dir():
        return [f"{monitoring}: missing monitoring directory"]
    for directory, dirs, files in os.walk(monitoring):
        for name in sorted(dirs + files):
            if name == ".DS_Store" or name.startswith("._"):
                error(Path(directory) / name, "macOS metadata must not be deployed; remove this file/directory")

    def archived(path):
        return path.is_relative_to(dashboards / "backup")

    active = {path.resolve() for path in dashboards.rglob("*.json") if not archived(path)}
    if not active:
        error(dashboards, "no active dashboard JSON files found")
    if not provisioning.is_dir():
        error(provisioning, "missing provisioning directory")

    compose = root / "deploy/docker-compose.monitoring.yml"
    mounts = []
    providers = []
    try:
        mounts = compose_mounts(compose)
    except (OSError, UnicodeError, ConfigError) as exc:
        error(compose, f"cannot inspect Grafana mounts: {exc}")
    configs = sorted(path for path in (provisioning / "dashboards").rglob("*")
                     if path.suffix in (".yml", ".yaml", ".json") and path.is_file())
    if not configs:
        error(provisioning / "dashboards", "no dashboard provider configuration found")
    for config in configs:
        try:
            providers.extend(provider_paths(config))
        except (OSError, UnicodeError, ValueError, KeyError, TypeError) as exc:
            error(config, f"cannot inspect dashboard provider: {exc}")
    if not providers:
        error(provisioning / "dashboards", "no file provider paths found")
    for provider in providers:
        if not provider.is_absolute() or "$" in str(provider):
            error(provisioning, f"provider path must be literal and absolute: {provider}")

    exposed = []
    config_destinations = set()
    for source, target, readonly in mounts:
        if not source.exists():
            error(compose, f"bind source does not exist: {source}")
            continue
        entries = sorted(source.rglob("*")) if source.is_dir() else [source]
        for entry in entries:
            if not entry.is_file():
                continue
            destination = target / entry.relative_to(source) if source.is_dir() else target
            if destination.is_relative_to(PurePosixPath("/etc/grafana/provisioning")):
                config_destinations.add(entry.resolve())
            covered = any(destination.is_relative_to(provider) for provider in providers)
            if entry.suffix == ".json" and covered:
                exposed.append((entry.resolve(), destination))
                if archived(entry):
                    error(compose, f"archived dashboard would be provisioned: {entry}")
            if (entry.resolve() in active or entry.is_relative_to(provisioning)) and not readonly:
                error(compose, f"Grafana configuration bind must be read-only: {source}")
            if entry.resolve() in active and not covered:
                error(compose, f"dashboard target is outside all provider paths: {destination}")
    for config in configs:
        if config.resolve() not in config_destinations:
            error(compose, f"provider configuration is not mounted under /etc/grafana/provisioning: {config}")
    counts = Counter(source for source, _ in exposed)
    for source in sorted(active - counts.keys()):
        error(compose, f"active dashboard is not mounted inside a provider path: {source}")
    for source, count in counts.items():
        if count > 1:
            error(compose, f"dashboard is provisioned {count} times: {source}")
    destinations = Counter(destination for _, destination in exposed)
    for destination, count in destinations.items():
        if count > 1:
            error(compose, f"multiple dashboard mounts share target: {destination}")
    for provider in providers:
        if not any(destination.is_relative_to(provider) for _, destination in exposed):
            error(compose, f"provider path has no mounted dashboard JSON: {provider}")

    json_paths = active | {path.resolve() for path in provisioning.rglob("*.json")}
    json_paths.update(source for source, _ in exposed)
    for path in sorted(json_paths):
        try:
            text = path.read_text(encoding="utf-8")
            if not text.strip():
                raise ValueError("empty JSON file")
            data = json.loads(text, object_pairs_hook=no_duplicate_keys, parse_constant=reject_constant)
            if not isinstance(data, dict) or not data:
                raise ValueError("expected a nonempty JSON object")
        except (OSError, UnicodeError, ValueError) as exc:
            error(path, f"invalid JSON: {exc}")
            continue
        templating = data.get("templating", {})
        variables = templating.get("list", []) if isinstance(templating, dict) else []
        if not isinstance(variables, list):
            error(path, "templating.list must be an array")
            continue
        uses_model_sqlstring = any("${model:sqlstring}" in value for value in strings(data))
        for variable in variables:
            if (isinstance(variable, dict) and variable.get("name") == "model"
                    and variable.get("includeAll") and variable.get("allValue") not in (None, "")
                    and uses_model_sqlstring):
                error(path, "model uses custom All value with ${model:sqlstring}; Grafana does not "
                      "SQL-escape custom All values. Avoid this combination and keep the All sentinel "
                      "safely quoted in SQL")
    return errors


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[3],
                        help="repository checkout to lint (default: script's checkout)")
    args = parser.parse_args(argv)
    errors = lint(args.root)
    if errors:
        for message in errors:
            print(f"ERROR: {message}", file=sys.stderr)
        print(f"FAIL: {len(errors)} Grafana lint error(s)", file=sys.stderr)
        return 1
    print("PASS: Grafana JSON, model SQL All safety, metadata and mount inventory")
    return 0


if __name__ == "__main__":
    sys.exit(main())
