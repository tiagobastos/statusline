#!/usr/bin/env python3
"""Regenerate the README screenshots in assets/.

Run with `make assets` from the repo root.

The statusline is ANSI in a terminal, and a terminal cannot be screenshotted
headlessly. So this renders the real `--demo` output to HTML with a Nerd Font
embedded, screenshots that with headless Chrome, and crops to the ink. The output
is a faithful render rather than a photograph of a terminal: same escape sequences,
same glyphs, same column math.

Requirements
  python3 with Pillow      pip install --user Pillow
  Google Chrome            for the headless screenshot
  MesloLGS NF              Regular + Bold, in ~/Library/Fonts or /Library/Fonts
  go                       to build the binary being demoed

Override the font location with STATUSLINE_ASSET_FONT_DIR and the browser with
STATUSLINE_ASSET_CHROME if either lives somewhere unusual.

The one subtlety worth knowing before editing: a Powerline pill cap (U+E0B6/U+E0B4)
is a glyph that fills its whole terminal cell, so it only joins its pill invisibly
when the box painted behind it is exactly the glyph's height. CSS gives that for
free, because an inline span paints its background over the font's content box and
not the taller line box. That is why LINE_HEIGHT below can be tuned purely for
looks without ever pulling a cap away from the pill it belongs to.
"""

import base64
import html
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

from PIL import Image, ImageChops

REPO = Path(__file__).resolve().parent.parent
ASSETS = REPO / "assets"

# --- render settings ---

COLUMNS = 126           # 126 - renderSafetyMargin = the 120-column cap the layout uses
FONT_SIZE = 15          # px
PAD_X, PAD_Y = 16, 14   # px of white around the block, before cropping
MARGIN = 16             # px of white kept around the ink, in captured (2x) pixels
SCALE = 2               # device pixel ratio for the capture
BG = "#ffffff"
FG = "#24292f"          # only used where no escape sequence sets a color

# The originals were shot in a terminal with generous line spacing, measured at
# ~3.5 character-widths per line. Meslo's advance is 0.6em, so 3.5 * 0.6 = 2.1.
LINE_HEIGHT = 2.1

FONT_FILES = ("MesloLGS NF Regular.ttf", "MesloLGS NF Bold.ttf")

# Section title (as printed by renderDemo) -> output file and target width. The
# titles are matched exactly so that renaming one in main.go fails loudly here
# instead of silently writing the wrong screenshot. "worktree layout" is absent on
# purpose: the README does not reference it.
SECTIONS = {
    "git layout — effort levels": ("demo-git-effort.jpeg", 800),
    "non-git layout": ("demo-nongit.jpeg", 800),
    "context window levels (medium effort)": ("demo-context-levels.jpeg", 800),
    "rate-limit window levels (medium effort)": ("demo-ratelimit-levels.jpeg", 800),
}
SKIP_SECTIONS = {"worktree layout (long branch)"}

HERO_FILE, HERO_WIDTH = "hero.jpeg", 1180

# The hero is a single statusline rather than a demo section, so it is produced by
# building a throwaway copy of the package whose main() renders exactly one line.
# Nothing in the checkout is modified and `make demo` output is unaffected.
HERO_MAIN = '''
func main() {
	home, _ := os.UserHomeDir()
	renderStatusLine(
		&GitInfo{Branch: "main", ModCount: 1, Age: "1h"},
		WindowInfo{Pct: 8, TimeLeft: "3h 03m left"},
		18, "maximum", "Claude Sonnet 4.6", home+"/.claude/statusline", false)
}
'''

SGR = re.compile(r"\x1b\[([0-9;]*)m")
# A section header is "  ── <title> ─────...". Capture the title between the runs.
HEADER = re.compile(r"^\s*─+\s*(.*?)\s*─+\s*$")


def die(msg):
    sys.exit(f"gen-assets: {msg}")


# --- environment ---

def find_fonts():
    candidates = []
    if env := os.environ.get("STATUSLINE_ASSET_FONT_DIR"):
        candidates.append(Path(env))
    candidates += [Path.home() / "Library/Fonts", Path("/Library/Fonts")]
    for directory in candidates:
        if all((directory / name).is_file() for name in FONT_FILES):
            return [directory / name for name in FONT_FILES]
    die(
        "could not find " + " and ".join(FONT_FILES) + ".\n"
        "  Looked in: " + ", ".join(str(c) for c in candidates) + "\n"
        "  Install MesloLGS NF, or set STATUSLINE_ASSET_FONT_DIR to its directory."
    )


def find_chrome():
    candidates = []
    if env := os.environ.get("STATUSLINE_ASSET_CHROME"):
        candidates.append(env)
    candidates += [
        "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
        shutil.which("google-chrome") or "",
        shutil.which("chromium") or "",
    ]
    for path in candidates:
        if path and Path(path).is_file():
            return path
    die("could not find Google Chrome. Set STATUSLINE_ASSET_CHROME to its binary.")


# --- ANSI to HTML ---

def parse(line):
    """Split one ANSI line into (text, fg, bg, bold) runs."""
    runs, pos, fg, bg, bold = [], 0, None, None, False
    for m in SGR.finditer(line):
        if m.start() > pos:
            runs.append((line[pos:m.start()], fg, bg, bold))
        params = m.group(1)
        if params.startswith("38;2;"):      # truecolor foreground
            fg = "rgb(" + ",".join(params.split(";")[2:5]) + ")"
        elif params.startswith("48;2;"):    # truecolor background
            bg = "rgb(" + ",".join(params.split(";")[2:5]) + ")"
        elif params in ("", "0"):
            fg, bg, bold = None, None, False
        elif params == "1":
            bold = True
        pos = m.end()
    if pos < len(line):
        runs.append((line[pos:], fg, bg, bold))
    return runs


def render_line(line):
    out = []
    for text, fg, bg, bold in parse(line):
        if not text:
            continue
        style = []
        if fg:
            style.append(f"color:{fg}")
        if bg:
            style.append(f"background:{bg}")
        if bold:
            style.append("font-weight:700")
        esc = html.escape(text)
        out.append(f'<span style="{";".join(style)}">{esc}</span>' if style else esc)
    return "".join(out) or "&nbsp;"


def build_html(lines, fonts):
    regular, bold = (base64.b64encode(f.read_bytes()).decode() for f in fonts)
    body = "\n".join(render_line(l) for l in lines)
    return f"""<!doctype html><html><head><meta charset="utf-8"><style>
@font-face {{ font-family:"SL"; font-weight:400; font-display:block;
  src:url(data:font/ttf;base64,{regular}) format("truetype"); }}
@font-face {{ font-family:"SL"; font-weight:700; font-display:block;
  src:url(data:font/ttf;base64,{bold}) format("truetype"); }}
* {{ margin:0; padding:0; }}
html, body {{ background:{BG}; }}
pre {{ display:inline-block; font-family:"SL",monospace; font-size:{FONT_SIZE}px;
  line-height:{LINE_HEIGHT}; color:{FG}; background:{BG};
  padding:{PAD_Y}px {PAD_X}px; white-space:pre;
  font-variant-ligatures:none; font-feature-settings:"liga" 0,"calt" 0;
  -webkit-font-smoothing:antialiased; text-rendering:geometricPrecision; }}
</style></head><body><pre>{body}</pre></body></html>"""


# --- capture ---

def shoot(lines, out_name, target_w, ctx):
    stem = Path(out_name).stem
    html_path = ctx["tmp"] / f"{stem}.html"
    png_path = ctx["tmp"] / f"{stem}.png"
    html_path.write_text(build_html(lines, ctx["fonts"]), encoding="utf-8")

    # Deliberately oversized: the crop below finds the real extent, so the window
    # only has to be big enough to avoid clipping.
    win_h = int(len(lines) * FONT_SIZE * LINE_HEIGHT + 2 * PAD_Y + 60)
    subprocess.run(
        [ctx["chrome"], "--headless", "--disable-gpu", "--hide-scrollbars",
         f"--force-device-scale-factor={SCALE}", "--default-background-color=FFFFFFFF",
         f"--screenshot={png_path}", f"--window-size=1400,{win_h}", str(html_path)],
        capture_output=True, check=True,
    )

    im = Image.open(png_path).convert("RGB")
    bbox = ImageChops.difference(im, Image.new("RGB", im.size, (255, 255, 255))).getbbox()
    if bbox is None:
        die(f"{out_name}: rendered blank")
    left, top, right, bottom = bbox
    if right >= im.width - 2:
        die(f"{out_name}: ink reaches the right edge ({right}/{im.width}) — "
            "the window is too narrow and content is clipped")

    im = im.crop((max(0, left - MARGIN), max(0, top - MARGIN),
                  min(im.width, right + MARGIN), min(im.height, bottom + MARGIN)))
    height = round(im.height * target_w / im.width)
    im = im.resize((target_w, height), Image.LANCZOS)
    im.save(ASSETS / out_name, "JPEG", quality=92, optimize=True, progressive=True)
    print(f"  {out_name}  {target_w}x{height}")


# --- drivers ---

def run(cmd, **kw):
    env = {**os.environ, "COLUMNS": str(COLUMNS)}
    return subprocess.run(cmd, capture_output=True, text=True, check=True, env=env, **kw).stdout


def demo_sections(binary):
    """Slice `--demo` output into {title: lines}, keyed by the printed header text."""
    lines = run([str(binary), "--demo"]).split("\n")
    sections, title, buf = {}, None, []
    for line in lines:
        plain = SGR.sub("", line)
        m = HEADER.match(plain) if plain.strip() else None
        if m and m.group(1):
            if title:
                sections[title] = buf
            title, buf = m.group(1), [line]
        elif title:
            buf.append(line)
    if title:
        sections[title] = buf
    # Trailing blanks would inflate the capture window and the crop margin.
    return {t: trim(b) for t, b in sections.items()}


def trim(lines):
    while lines and not SGR.sub("", lines[-1]).strip():
        lines.pop()
    return lines


def hero_lines(tmp):
    """Build a throwaway copy of the package whose main() renders one hero line."""
    src = (REPO / "main.go").read_text(encoding="utf-8")
    if "func main() {" not in src:
        die("main.go has no `func main() {` to redirect — the hero build needs updating")
    # Renaming the real main leaves its imports in use, so nothing goes unreferenced.
    patched = src.replace("func main() {", "func unusedMain() {", 1) + HERO_MAIN

    build_dir = tmp / "hero"
    build_dir.mkdir()
    (build_dir / "main.go").write_text(patched, encoding="utf-8")
    shutil.copy(REPO / "go.mod", build_dir / "go.mod")

    binary = build_dir / "hero-bin"
    subprocess.run(["go", "build", "-o", str(binary), "."],
                   cwd=build_dir, capture_output=True, check=True)
    return trim(run([str(binary)]).split("\n"))


def main():
    if not ASSETS.is_dir():
        die(f"no assets directory at {ASSETS}")

    with tempfile.TemporaryDirectory(prefix="statusline-assets-") as tmpname:
        tmp = Path(tmpname)
        ctx = {"tmp": tmp, "fonts": find_fonts(), "chrome": find_chrome()}

        binary = tmp / "statusline"
        subprocess.run(["go", "build", "-o", str(binary), "."],
                       cwd=REPO, capture_output=True, check=True)

        print("Rendering assets:")
        sections = demo_sections(binary)
        unknown = set(sections) - set(SECTIONS) - SKIP_SECTIONS
        if unknown:
            die("unrecognised demo section(s): " + ", ".join(sorted(unknown)) +
                "\n  Add them to SECTIONS or SKIP_SECTIONS in this script.")
        missing = set(SECTIONS) - set(sections)
        if missing:
            die("demo no longer prints: " + ", ".join(sorted(missing)) +
                "\n  Update SECTIONS in this script to match main.go.")

        for title, (name, width) in SECTIONS.items():
            shoot(sections[title], name, width, ctx)
        shoot(hero_lines(tmp), HERO_FILE, HERO_WIDTH, ctx)

    print("Done. Review with `git diff --stat assets/`.")


if __name__ == "__main__":
    main()
