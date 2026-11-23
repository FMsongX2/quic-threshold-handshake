#!/usr/bin/env bash
# Draws the figures and renders paper.md to paper.pdf.
#
# usage: paper/build.sh
# Needs Python 3 with matplotlib, pandoc 3 and typst. The PDF is a reading
# copy; a submission follows the template of its venue.
set -eu
cd "$(dirname "$0")"
python3 figures.py
pandoc paper.md -f markdown-implicit_figures -o paper.pdf --pdf-engine=typst -H preamble.typ \
  -V mainfont="${PAPER_FONT:-AppleMyungjo}" -V fontsize=10pt -V papersize=a4 -V margin.x=20mm -V margin.y=24mm
