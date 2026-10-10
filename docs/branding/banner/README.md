# ThothDock terminal banner generator

`mk.py` converts the original icon (`../thothdock-icon-master.png`) to terminal
art (half-blocks, braille or an ASCII ramp); `compose.py` adds the runtime
info; `gen_embed.py` emits the bash arrays embedded in Garden's `thothfetch`
(braille, 256 colours, 22x11 / 30x15 / 60x30 cells). `xterm_shot.py` renders
ANSI output in the xterm.js bundled with ThothTerm for a desktop screenshot.
The shipped designs are the braille ones; the candidates and their phone and
desktop screenshots are in `docs/nextgen/evidence/ux/`.
