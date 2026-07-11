from pathlib import Path
import subprocess
from PIL import Image


ROOT = Path(__file__).resolve().parents[1]
BRAND = ROOT / "docs" / "brand"
SOURCE = BRAND / "source"
PNG = BRAND / "png"
PUBLIC = ROOT / "web" / "public"


def render(source: Path, target: Path, width: int, height: int | None = None) -> None:
    target.parent.mkdir(parents=True, exist_ok=True)
    command = [
        "npx.cmd",
        "--yes",
        "sharp-cli",
        "-i",
        str(source),
        "-o",
        str(target),
        "-f",
        "png",
        "resize",
        str(width),
    ]
    if height is not None:
        command.append(str(height))
    subprocess.run(command, check=True)


def main() -> None:
    mark = SOURCE / "gofer-mark.svg"
    combination = SOURCE / "gofer-combination.svg"
    favicon = PUBLIC / "favicon.svg"

    for size in (1024, 512, 256, 128, 64, 48, 32, 16):
        render(mark, PNG / f"gofer-mark-{size}.png", size, size)

    for width in (2048, 1200, 800, 400):
        render(combination, PNG / f"gofer-combination-{width}.png", width)

    favicon_pngs = []
    for size in (16, 32, 48):
        target = PUBLIC / f"favicon-{size}x{size}.png"
        render(favicon, target, size, size)
        favicon_pngs.append(Image.open(target).convert("RGBA"))
    favicon_pngs[-1].save(PUBLIC / "favicon.ico", format="ICO", sizes=[(16, 16), (32, 32), (48, 48)])

    render(favicon, PUBLIC / "apple-touch-icon.png", 180, 180)
    render(favicon, PUBLIC / "android-chrome-192x192.png", 192, 192)
    render(favicon, PUBLIC / "android-chrome-512x512.png", 512, 512)


if __name__ == "__main__":
    main()
