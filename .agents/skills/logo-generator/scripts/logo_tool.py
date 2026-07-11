#!/usr/bin/env python3
"""Deterministic SVG logo workspace, validation, preview, and export tools."""

from __future__ import annotations

import argparse
import html
import json
import re
import shutil
import subprocess
import sys
from pathlib import Path
from xml.etree import ElementTree as ET


SVG_NS = "http://www.w3.org/2000/svg"
FORBIDDEN_TAGS = {"script", "foreignObject", "image"}
FORBIDDEN_LINK = re.compile(r"(?:href|src)\s*=\s*['\"](?:https?:|//|data:)", re.I)


def expand_inputs(values: list[str]) -> list[Path]:
    paths: list[Path] = []
    for value in values:
        path = Path(value)
        if any(char in value for char in "*?["):
            paths.extend(sorted(path.parent.glob(path.name)))
        else:
            paths.append(path)
    return list(dict.fromkeys(path.resolve() for path in paths))


def init_project(args: argparse.Namespace) -> None:
    root = Path(args.output).resolve()
    for child in ("concepts", "final", "exports"):
        (root / child).mkdir(parents=True, exist_ok=True)
    metadata = {
        "brand_name": args.name,
        "brief": {"audience": "", "metaphor": "", "adjectives": [], "avoid": []},
        "variants": [],
    }
    (root / "logo-project.json").write_text(json.dumps(metadata, indent=2), encoding="utf-8")
    print(f"initialized {root}")


def local_name(tag: str) -> str:
    return tag.rsplit("}", 1)[-1]


def validate_one(path: Path) -> list[str]:
    errors: list[str] = []
    if not path.is_file():
        return ["file does not exist"]
    raw = path.read_text(encoding="utf-8")
    try:
        root = ET.fromstring(raw)
    except ET.ParseError as exc:
        return [f"invalid XML: {exc}"]
    if local_name(root.tag) != "svg":
        errors.append("root element is not svg")
    if not root.get("viewBox"):
        errors.append("missing viewBox")
    if root.get("width") and root.get("height") and not root.get("viewBox"):
        errors.append("fixed dimensions require a viewBox")
    names = [local_name(node.tag) for node in root.iter()]
    for tag in sorted(FORBIDDEN_TAGS.intersection(names)):
        errors.append(f"forbidden element: {tag}")
    if FORBIDDEN_LINK.search(raw):
        errors.append("external or embedded resource reference found")
    if "title" not in names:
        errors.append("missing accessible title")
    if "desc" not in names:
        errors.append("missing accessible desc")
    if root.get("role") != "img":
        errors.append('root must set role="img"')
    return errors


def svg_aspect(path: Path) -> float:
    root = ET.fromstring(path.read_text(encoding="utf-8"))
    values = [float(value) for value in root.get("viewBox", "").replace(",", " ").split()]
    if len(values) != 4 or values[2] <= 0 or values[3] <= 0:
        raise SystemExit("viewBox must contain four positive numeric dimensions")
    return values[2] / values[3]


def validate(args: argparse.Namespace) -> None:
    paths = expand_inputs(args.inputs)
    if not paths:
        raise SystemExit("no SVG inputs matched")
    failed = False
    for path in paths:
        errors = validate_one(path)
        if errors:
            failed = True
            print(f"FAIL {path}")
            for error in errors:
                print(f"  - {error}")
        else:
            print(f"PASS {path}")
    if failed:
        raise SystemExit(1)


def sharp_command() -> str:
    command = "npx.cmd" if sys.platform == "win32" else "npx"
    if shutil.which(command) is None:
        raise SystemExit("npx is required for SVG rendering")
    return command


def render_png(source: Path, target: Path, size: int, background: str | None = None) -> None:
    target.parent.mkdir(parents=True, exist_ok=True)
    command = [sharp_command(), "--yes", "sharp-cli", "-i", str(source), "-o", str(target),
               "-f", "png", "resize", str(size)]
    if background:
        command.extend(["--", "flatten", background])
    subprocess.run(command, check=True)


def render(args: argparse.Namespace) -> None:
    source = Path(args.input).resolve()
    errors = validate_one(source)
    if errors:
        raise SystemExit("SVG validation failed: " + "; ".join(errors))
    output = Path(args.output).resolve()
    sizes = sorted({int(value) for value in args.sizes.split(",")}, reverse=True)
    if not sizes or any(size < 16 or size > 8192 for size in sizes):
        raise SystemExit("sizes must be between 16 and 8192")
    generated: dict[int, Path] = {}
    for size in sizes:
        target = output / f"{source.stem}-{size}.png"
        render_png(source, target, size)
        generated[size] = target
        print(target)
    if args.favicon:
        if not 0.95 <= svg_aspect(source) <= 1.05:
            raise SystemExit("--favicon requires a square or near-square SVG; use a simplified icon variant")
        try:
            from PIL import Image
        except ImportError as exc:
            raise SystemExit("Pillow is required for --favicon; run with uv --with pillow") from exc
        required = (16, 32, 48)
        for size in required:
            if size not in generated:
                target = output / f"{source.stem}-{size}.png"
                render_png(source, target, size)
                generated[size] = target
        frames = [Image.open(generated[size]).convert("RGBA") for size in required]
        frames[-1].save(output / "favicon.ico", format="ICO", sizes=[(s, s) for s in required])
        print(output / "favicon.ico")
        shutil.copyfile(source, output / "favicon.svg")
        render_png(source, output / "apple-touch-icon.png", 180, args.background)
        render_png(source, output / "android-chrome-192x192.png", 192)
        render_png(source, output / "android-chrome-512x512.png", 512)
        manifest = {
            "name": args.app_name or source.stem,
            "short_name": args.app_name or source.stem,
            "icons": [
                {"src": "./android-chrome-192x192.png", "sizes": "192x192", "type": "image/png"},
                {"src": "./android-chrome-512x512.png", "sizes": "512x512", "type": "image/png"},
            ],
            "theme_color": args.background,
            "background_color": args.background,
            "display": "standalone",
        }
        (output / "site.webmanifest").write_text(json.dumps(manifest, indent=2), encoding="utf-8")


def showcase(args: argparse.Namespace) -> None:
    paths = expand_inputs(args.inputs)
    if not paths:
        raise SystemExit("no SVG inputs matched")
    cards: list[str] = []
    for path in paths:
        errors = validate_one(path)
        if errors:
            raise SystemExit(f"{path}: " + "; ".join(errors))
        svg = path.read_text(encoding="utf-8")
        cards.append(f'<article><div class="stage">{svg}</div><h2>{html.escape(path.stem)}</h2>'
                     f'<p>{html.escape(str(path))}</p></article>')
    page = """<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<title>Logo concepts</title><style>
:root{color-scheme:light dark;font-family:system-ui,sans-serif}body{margin:0;padding:32px;background:#111827;color:#f8fafc}
main{display:grid;grid-template-columns:repeat(auto-fit,minmax(260px,1fr));gap:20px}article{background:#1f2937;border:1px solid #475569;border-radius:12px;padding:18px}
.stage{aspect-ratio:1;display:grid;place-items:center;background:linear-gradient(135deg,#fff 0 50%,#0f172a 50%);border-radius:8px}.stage svg{width:72%;height:72%}
h2{font-size:16px;margin:14px 0 4px}p{font:12px ui-monospace,monospace;color:#94a3b8;overflow-wrap:anywhere}
</style></head><body><h1>Logo concepts</h1><main>""" + "".join(cards) + "</main></body></html>"
    target = Path(args.output).resolve()
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(page, encoding="utf-8")
    print(target)


def parser() -> argparse.ArgumentParser:
    root = argparse.ArgumentParser(description=__doc__)
    commands = root.add_subparsers(dest="command", required=True)
    init = commands.add_parser("init", help="create a logo project workspace")
    init.add_argument("--output", required=True)
    init.add_argument("--name", required=True)
    init.set_defaults(run=init_project)
    check = commands.add_parser("validate", help="validate safe, portable SVG inputs")
    check.add_argument("inputs", nargs="+")
    check.set_defaults(run=validate)
    export = commands.add_parser("render", help="render square PNGs and optional ICO")
    export.add_argument("input")
    export.add_argument("--output", required=True)
    export.add_argument("--sizes", default="1024,512,256,128,64,48,32,16")
    export.add_argument("--favicon", action="store_true")
    export.add_argument("--background", default="#111827", help="solid Apple/PWA background")
    export.add_argument("--app-name", help="manifest application name")
    export.set_defaults(run=render)
    preview = commands.add_parser("showcase", help="create a local HTML comparison sheet")
    preview.add_argument("inputs", nargs="+")
    preview.add_argument("--output", required=True)
    preview.set_defaults(run=showcase)
    return root


if __name__ == "__main__":
    arguments = parser().parse_args()
    arguments.run(arguments)
