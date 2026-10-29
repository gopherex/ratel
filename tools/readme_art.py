"""Draw the README banner and the benchmark chart as SVG with live text;
make readme-art converts the text to paths with Inkscape.
Usage: readme_art.py <mascot svg> <benchstat table> <out dir>"""
import re
import sys

mascot_path, table_path, out = sys.argv[1], sys.argv[2], sys.argv[3]

RUBY, RUBY_DEEP = "#E8174A", "#9C0A30"
INK, INK_SOFT, INK_MUTED = "#F4F1F2", "#B9B2B5", "#7D7579"
SURFACE, SURFACE_EDGE = "#0F0B0D", "#2A2024"
MONO = "JetBrains Mono"
SANS = "Noto Sans"

# The mascot's paths, nested as a group placed by the banner.
mascot = open(mascot_path).read()
inner = mascot[mascot.index(">", mascot.index("<svg")) + 1:mascot.rindex("</svg>")]


def banner():
    width, height = 1280, 440
    pills = ["code from SQL", "typed query builder", "zero-lock migrations"]
    pill_x, parts = 540, []

    for text in pills:
        pill_width = 36 + len(text) * 17 * 0.6
        parts.append(
            f'<rect x="{pill_x}" y="318" width="{pill_width:.0f}" height="44" rx="22" fill="#1E1418" '
            f'stroke="{RUBY}" stroke-opacity="0.55" stroke-width="1.5"/>'
            f'<text x="{pill_x + pill_width / 2:.0f}" y="346" text-anchor="middle" font-family="{MONO}" '
            f'font-weight="700" font-size="17" fill="{INK}">{text}</text>')
        pill_x += pill_width + 14

    return f'''<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}" viewBox="0 0 {width} {height}">
  <defs>
    <linearGradient id="bg" x1="0" y1="0" x2="1" y2="1">
      <stop offset="0" stop-color="#160D11"/>
      <stop offset="1" stop-color="{SURFACE}"/>
    </linearGradient>
    <radialGradient id="glow" cx="0.5" cy="0.5" r="0.5">
      <stop offset="0" stop-color="{RUBY}" stop-opacity="0.42"/>
      <stop offset="0.55" stop-color="{RUBY_DEEP}" stop-opacity="0.16"/>
      <stop offset="1" stop-color="{RUBY_DEEP}" stop-opacity="0"/>
    </radialGradient>
    <pattern id="grid" width="32" height="32" patternUnits="userSpaceOnUse">
      <path d="M32 0H0V32" fill="none" stroke="#FFFFFF" stroke-opacity="0.035" stroke-width="1"/>
    </pattern>
    <clipPath id="card"><rect width="{width}" height="{height}" rx="24"/></clipPath>
  </defs>
  <g clip-path="url(#card)">
    <rect width="{width}" height="{height}" fill="url(#bg)"/>
    <rect width="{width}" height="{height}" fill="url(#grid)"/>
    <circle cx="270" cy="230" r="250" fill="url(#glow)"/>
    <g transform="translate(70 30) scale(0.39)">{inner}</g>
    <text x="536" y="190" font-family="{MONO}" font-weight="800" font-size="118" letter-spacing="-2"
      fill="{INK}">r<tspan fill="{RUBY}">a</tspan>tel</text>
    <rect x="542" y="214" width="96" height="5" rx="2.5" fill="{RUBY}"/>
    <text x="540" y="268" font-family="{SANS}" font-weight="600" font-size="31" fill="{INK_SOFT}">PostgreSQL for Go, without the footguns</text>
    {"".join(parts)}
  </g>
  <rect x="0.75" y="0.75" width="{width - 1.5}" height="{height - 1.5}" rx="23.5" fill="none" stroke="{SURFACE_EDGE}" stroke-width="1.5"/>
</svg>
'''


def geomeans(table):
    """The benchstat geomean rows: time first, then allocations."""
    header = re.search(r"│\s+pgx\s+│\s+ratel\s+│.*", table).group(0)
    libraries = re.findall(r"│\s+(\w+)\s+", header)
    rows = re.findall(r"^geomean\s+(.*)$", table, re.M)
    units = {"": 1, "n": 1e-3, "µ": 1, "m": 1e3, "k": 1e3, "Ki": 1024, "Mi": 1024 * 1024}
    result = []
    for row in (rows[0], rows[2]):  # sec/op and allocs/op; B/op is the middle one
        values = []
        for token in row.split():
            match = re.fullmatch(r"([\d.]+)(n|µ|m|k|Ki|Mi)?", token)
            if match:
                values.append(float(match.group(1)) * units[match.group(2) or ""])
        if len(values) != len(libraries):
            raise SystemExit(f"geomean row {row!r} has {len(values)} values for {libraries}")
        result.append(values)
    return libraries, result


def chart(libraries, time, allocs):
    order = sorted(range(len(libraries)), key=lambda index: time[index])
    base_time, base_allocs = time[libraries.index("pgx")], allocs[libraries.index("pgx")]
    names = {"pgx": "pgx (by hand)", "gorm": "GORM", "ent": "ent", "bun": "bun", "sqlc": "sqlc", "ratel": "ratel"}

    width, row, top = 1280, 46, 112
    height = top + row * len(order) + 56
    panels = [("time per operation", time, base_time, 600), ("allocations per operation", allocs, base_allocs, 1010)]
    scale_max = max(max(values) / base for _, values, base, _ in panels)
    bar_space = 300
    parts = []

    for title, values, base, x0 in panels:
        parts.append(f'<text x="{x0 - bar_space + 40}" y="86" font-family="{SANS}" font-weight="600" font-size="20" '
                     f'fill="{INK_SOFT}">{title}, × pgx by hand</text>')
        # The 1× reference line.
        ref = x0 - bar_space + 40 + bar_space * 1 / scale_max
        parts.append(f'<line x1="{ref:.1f}" y1="{top - 8}" x2="{ref:.1f}" y2="{top + row * len(order) - 8}" '
                     f'stroke="{INK_MUTED}" stroke-opacity="0.6" stroke-dasharray="3 5"/>')
        for position, index in enumerate(order):
            ratio = values[index] / base
            y = top + position * row
            bar = bar_space * ratio / scale_max
            ours = libraries[index] == "ratel"
            fill = RUBY if ours else "#4A3F44"
            start = x0 - bar_space + 40
            parts.append(f'<path d="M{start} {y} h{bar - 4:.1f} a4 4 0 0 1 4 4 v20 a4 4 0 0 1 -4 4 h-{bar - 4:.1f} Z" '
                         f'fill="{fill}"/>')
            parts.append(f'<text x="{start + bar + 10:.1f}" y="{y + 20}" font-family="{MONO}" font-weight="700" '
                         f'font-size="18" fill="{INK if ours else INK_SOFT}">{ratio:.2f}×</text>')

    for position, index in enumerate(order):
        ours = libraries[index] == "ratel"
        parts.append(f'<text x="290" y="{top + position * row + 20}" text-anchor="end" font-family="{MONO}" '
                     f'font-weight="{800 if ours else 700}" font-size="20" fill="{INK if ours else INK_SOFT}">'
                     f'{names[libraries[index]]}</text>')

    return f'''<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}" viewBox="0 0 {width} {height}">
  <rect width="{width}" height="{height}" rx="20" fill="{SURFACE}"/>
  <rect x="0.75" y="0.75" width="{width - 1.5}" height="{height - 1.5}" rx="19.5" fill="none" stroke="{SURFACE_EDGE}" stroke-width="1.5"/>
  <text x="40" y="48" font-family="{SANS}" font-weight="700" font-size="26" fill="{INK}">Six scenarios on PostgreSQL 18, geometric mean, lower is better</text>
  {"".join(parts)}
  <text x="40" y="{height - 22}" font-family="{SANS}" font-size="16" fill="{INK_MUTED}">Row by key, page of 100, page with relations, insert, insert of 100, update. Median of 10 runs; make bench-compare.</text>
</svg>
'''


table = open(table_path).read()
libraries, (time, allocs) = geomeans(table)
open(f"{out}/banner.svg", "w").write(banner())
open(f"{out}/bench.svg", "w").write(chart(libraries, time, allocs))
