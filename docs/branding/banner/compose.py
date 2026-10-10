#!/usr/bin/env python3
"""Compose banner candidates: logo art (half-blocks) + the runtime info. Usage: compose.py NAME"""
import sys, re
import mk
RESET = "\033[0m"
def c256(n): return f"\033[38;5;{n}m"

def art_lines(cols, mode="half"):
    rows = {"half": mk.half, "braille": mk.braille}[mode](cols)
    out = []
    for cells in rows:
        s, cur = "", None
        for ch, fg, bg in cells:
            key = (mk.to_256(mk.palette(fg)) if fg else None, mk.to_256(mk.palette(bg)) if bg else None)
            if ch == " ":
                if cur is not None: s += RESET; cur = None
                s += " "; continue
            if key != cur:
                s += RESET if cur is not None else ""
                if key[0] is not None: s += f"\033[38;5;{key[0]}m"
                if key[1] is not None: s += f"\033[48;5;{key[1]}m"
                cur = key
            s += ch
        if cur is not None: s += RESET
        out.append(s.rstrip())
    return out

def vis(s): return len(re.sub(r"\x1b\[[0-9;]*m", "", s))

def info(label_w, short=False):
    L = lambda t: f"{c256(103)}{t.ljust(label_w)}{RESET}"
    V = lambda t: f"{c256(253)}{t}{RESET}"
    rows = [f"{c256(123)}ThothTerm • ThothDock{RESET}", f"{V('ThothTerm Container Runtime')}", "",
            L("Guest") + V("Debian GNU/Linux 13 (trixie)"), L("Arch" if short else "Architecture") + V("aarch64"),
            L("Shell") + V("Bash"), L("Engine") + V("ThothDock"), L("API" if short else "Docker API") + V("1.41"), L("Runtime") + V("Garden PRoot")]
    return rows

def beside(art, width, info_rows, gap=2):
    n = max(len(art), len(info_rows)); pad_top = max(0, (len(art) - len(info_rows)) // 2)
    out = []
    for i in range(n):
        a = art[i] if i < len(art) else ""
        a += " " * (width - vis(a))
        j = i - pad_top
        t = info_rows[j] if 0 <= j < len(info_rows) else ""
        out.append((a + " " * gap + t).rstrip())
    return out

if __name__ == "__main__":
    name = sys.argv[1]
    if name == "A":      # compact beside (phone landscape-safe, 63 cols)
        art = art_lines(22); lines = beside(art, 22, info(8, True))
    elif name == "B":    # compact stacked
        art = art_lines(30); T = info(13)
        lines = art + ["", T[0], T[1], T[3].replace("Debian GNU/Linux 13 (trixie)", "Debian 13 (trixie)"), T[6]]
    elif name == "D":    # compact beside, braille (4x the detail)
        art = art_lines(22, "braille"); lines = beside(art, 22, info(8, True))
    elif name == "E":    # compact stacked, braille
        art = art_lines(30, "braille"); T = info(13)
        lines = art + ["", T[0], T[1], T[3].replace("Debian GNU/Linux 13 (trixie)", "Debian 13 (trixie)"), T[6]]
    elif name == "F":    # desktop, braille
        art = art_lines(60, "braille"); lines = beside(art, 60, info(14), gap=3)
    elif name == "C":    # desktop
        art = art_lines(56); lines = beside(art, 56, info(14), gap=3)
    print("\n".join(lines))
