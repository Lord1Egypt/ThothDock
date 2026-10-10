#!/usr/bin/env python3
"""Convert the ThothDock icon to terminal art. Usage: mk.py MODE COLS [--ansi|--plain]
MODE: half | braille | ramp. Output lines on stdout (ANSI 256-colour or plain)."""
import sys
from PIL import Image
import numpy as np

SRC = "/home/lordegypt/ThothDock/docs/branding/thothdock-icon-master.png"

def symbol():
    im = Image.open(SRC).convert("RGB").crop((140, 70, 1116, 1056))
    a = np.asarray(im).astype(float)
    mx, mn = a.max(2), a.min(2)
    sat = mx - mn
    keep = (mx > 46).astype(float)                       # symbol (incl. dark metal) vs tile and halo
    # soften the mask a little so thin lines survive downsampling
    return Image.fromarray(np.uint8(a * keep[..., None])), Image.fromarray(np.uint8(keep * 255))

def to_256(rgb):
    r, g, b = [int(round(v / 255 * 5)) for v in rgb]
    return 16 + 36 * r + 6 * g + b

def palette(rgb):
    """Brand colours: silver metal, saturated cyan and blue, brightness kept from the logo."""
    r, g, b = rgb
    mx = max(r, g, b)
    lum = 0.3 * r + 0.59 * g + 0.11 * b
    if mx - min(r, g, b) < 45:                              # grey metal -> silver
        v = int(min(225, 110 + mx * 1.5))
        return (v, v, v)
    k = min(1.0, max(0.42, mx / 190.0))                     # facet depth
    t = min(1.0, max(0.0, (g - 70) / 150.0))                # 0 = blue, 1 = cyan
    return (int(10 * k), int((100 + 135 * t) * k), int(255 * k))

def grid(cols, rows_per_cell, px_per_col):
    img, msk = symbol()
    w = cols * px_per_col
    h = int(round(w * img.size[1] / img.size[0]))
    return img.resize((w, h), Image.LANCZOS), msk.resize((w, h), Image.LANCZOS)

def sgr(fg=None, bg=None):
    s = ""
    if fg is not None: s += f"\033[38;5;{to_256(palette(fg))}m"
    if bg is not None: s += f"\033[48;5;{to_256(palette(bg))}m"
    return s

def half(cols):
    img, msk = grid(cols, 2, 1)
    a, m = np.asarray(img).astype(float), np.asarray(msk).astype(float) / 255
    h = a.shape[0] - a.shape[0] % 2
    rows = []
    for y in range(0, h, 2):
        line, cells = "", []
        for x in range(cols):
            top, bot = (a[y, x], m[y, x] > .4), (a[y + 1, x], m[y + 1, x] > .4)
            if not top[1] and not bot[1]: cells.append((" ", None, None))
            elif top[1] and not bot[1]:   cells.append(("▀", tuple(top[0]), None))
            elif bot[1] and not top[1]:   cells.append(("▄", tuple(bot[0]), None))
            else:                         cells.append(("▀", tuple(top[0]), tuple(bot[0])))
        rows.append(cells)
    return rows

def braille(cols):
    img, msk = grid(cols, 4, 2)
    a, m = np.asarray(img).astype(float), np.asarray(msk).astype(float) / 255
    lum = a.mean(2)
    H = a.shape[0] - a.shape[0] % 4
    bits = [(0, 0, 0x01), (0, 1, 0x02), (0, 2, 0x04), (1, 0, 0x08), (1, 1, 0x10), (1, 2, 0x20), (0, 3, 0x40), (1, 3, 0x80)]
    rows = []
    for y in range(0, H, 4):
        cells = []
        for x in range(cols):
            v, cs = 0, []
            for dx, dy, bit in bits:
                p = (y + dy, x * 2 + dx)
                if m[p] > .35 and lum[p] > 55:
                    v |= bit; cs.append(a[p])
            if not v: cells.append((" ", None, None))
            else: cells.append((chr(0x2800 + v), tuple(np.mean(cs, axis=0)), None))
        rows.append(cells)
    return rows

RAMP = " .:-=+*#%@"
def ramp(cols):
    img, msk = grid(cols, 2, 1)
    h = int(round(img.size[1] / 2))
    img, msk = img.resize((cols, h), Image.LANCZOS), msk.resize((cols, h), Image.LANCZOS)
    a, m = np.asarray(img).astype(float), np.asarray(msk).astype(float) / 255
    lum = a.mean(2)
    rows = []
    for y in range(h):
        cells = []
        for x in range(cols):
            if m[y, x] < .3: cells.append((" ", None, None)); continue
            i = int(min(9, max(1, lum[y, x] / 255 * 11)))
            cells.append((RAMP[i], tuple(a[y, x]), None))
        rows.append(cells)
    return rows

def render(rows, ansi):
    out = []
    for cells in rows:
        s = ""
        for ch, fg, bg in cells:
            s += (sgr(fg, bg) + ch + "\033[0m") if ansi and (fg or bg) else ch
        out.append(s.rstrip() if not ansi else s)
    return "\n".join(out)

if __name__ == "__main__":
    mode, cols = sys.argv[1], int(sys.argv[2])
    rows = {"half": half, "braille": braille, "ramp": ramp}[mode](cols)
    print(render(rows, "--plain" not in sys.argv))
