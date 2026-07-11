---
name: Lewp
description: Real URLs for local dev — calm, precise, handmade marketing + docs site
colors:
  loop-teal: "#0E8C8C"
  loop-teal-text: "#0A6E6E"
  warm-paper: "#F7F4EE"
  ink: "#1A1A1A"
  soft-ink: "#4A4A46"
  faded-ink: "#6A675F"
  faint: "#9A9891"
  faint-2: "#C7C2B6"
  hairline: "#E4E0D6"
  hairline-soft: "#E9E5DB"
  chip: "#ECE8DE"
  raised-paper: "#FBF9F3"
  terminal: "#16211F"
  terminal-ink: "#EDEBE4"
  terminal-dim: "#9A968D"
  terminal-comment: "#908B81"
  ok-green: "#5FB98C"
  warn-red: "#B4433A"
typography:
  display:
    fontFamily: "Geist, system-ui, sans-serif"
    fontSize: "52px"
    fontWeight: 600
    lineHeight: 1.08
    letterSpacing: "-0.035em"
  headline:
    fontFamily: "Geist, system-ui, sans-serif"
    fontSize: "33px"
    fontWeight: 600
    lineHeight: 1.15
    letterSpacing: "-0.03em"
  title:
    fontFamily: "Geist, system-ui, sans-serif"
    fontSize: "20px"
    fontWeight: 600
    lineHeight: 1.3
    letterSpacing: "-0.02em"
  body:
    fontFamily: "Geist, system-ui, sans-serif"
    fontSize: "16px"
    fontWeight: 400
    lineHeight: 1.55
    letterSpacing: "normal"
  label:
    fontFamily: "Geist Mono, ui-monospace, SFMono-Regular, Menlo, monospace"
    fontSize: "13px"
    fontWeight: 400
    lineHeight: 1.4
    letterSpacing: "0.08em"
rounded:
  sm: "4px"
  md: "10px"
  lg: "12px"
  full: "50%"
spacing:
  xs: "8px"
  sm: "16px"
  md: "28px"
  lg: "44px"
  xl: "72px"
components:
  terminal-panel:
    backgroundColor: "{colors.terminal}"
    textColor: "{colors.terminal-ink}"
    rounded: "{rounded.md}"
    padding: "16px 18px"
  paper-card:
    backgroundColor: "{colors.raised-paper}"
    textColor: "{colors.ink}"
    rounded: "{rounded.lg}"
    padding: "22px 24px"
  inline-code:
    backgroundColor: "{colors.chip}"
    textColor: "{colors.ink}"
    rounded: "{rounded.sm}"
    padding: "2px 6px"
  step-number:
    backgroundColor: "#0E8C8C1A"
    textColor: "{colors.loop-teal}"
    rounded: "{rounded.full}"
    size: "44px"
---

# Design System: Lewp

## 1. Overview

**Creative North Star: "The Workshop Bench"**

The Lewp site is a well-kept workbench: a warm paper surface with a small set of exact tools laid out on it. The light world (cream paper, hairline borders, near-black ink) is where the explaining happens; the dark world (deep green-black terminal panels) is where the proving happens. The two never blend — terminals are physical objects placed on the paper, showing real commands and real output. The single teal accent is the one colored tool on the bench: it marks the brand loop, links, and prompts, and nothing else.

The system is calm, precise, and handmade. One sans family (Geist) carries all reading text through weight and size contrast alone; Geist Mono marks everything machine-adjacent — commands, URLs, labels, navigation. The only whimsy on the entire surface is the hand-drawn loop logo and its once-per-visit path-draw animation. Everything else holds still.

This system explicitly rejects the VC-SaaS landing page (gradient heroes, hero metrics, logo walls), dark-mode terminal-core (all-black pages with neon accents), and corporate docs-portal chrome. Dark panels are objects *on* the page, never the page itself.

**Key Characteristics:**
- Two material worlds: warm paper surfaces, dark terminal objects
- One accent (Loop Teal) used sparingly for brand, links, and prompts
- Single sans family + mono for everything machine-adjacent
- Flat by default; hairline borders structure the page
- Motion happens once, at the hero, then the page holds still

## 2. Colors

A restrained warm-paper palette with one committed teal accent and a self-contained dark terminal sub-palette.

### Primary
- **Loop Teal** (#0E8C8C): The brand accent. Named for the hand-drawn loop logo. Used for the brand mark and SVG strokes, terminal prompts (`$`), step numbers, and other large or bold elements (≥18px, or ≥14px bold). It signals "interactive or brand" — never decoration.
- **Loop Teal Text** (#0A6E6E): The same voice at reading size. Used wherever teal is set below large-text size — links, the kicker, active nav, scroll-spy states. It clears 4.5:1 on Warm Paper; plain Loop Teal does not (3.72:1), which is why small teal text is prohibited.

### Neutral
- **Warm Paper** (#F7F4EE): The body background. The bench surface everything sits on.
- **Raised Paper** (#FBF9F3): Card backgrounds — one shade lighter than the bench, always with a hairline border.
- **Ink** (#1A1A1A): Headings and primary text.
- **Soft Ink** (#4A4A46): Ledes and hero subtext.
- **Faded Ink** (#6A675F): Supporting notes, feature body copy, sidebar links.
- **Faint** (#9A9891) / **Faint-2** (#C7C2B6): Decorative marks only (arrows, dividers). Never text of any kind — both fail AA even for large type. Small labels use Faded Ink instead.
- **Hairline** (#E4E0D6) / **Hairline Soft** (#E9E5DB): 1px section and list borders — the primary structural device.
- **Chip** (#ECE8DE): Inline-code background.

### Terminal (the dark object's own world)
- **Terminal** (#16211F): Panel background — a deep green-black, not pure black.
- **Terminal Ink** (#EDEBE4): Command output text.
- **Terminal Dim** (#9A968D) / **Terminal Comment** (#908B81): Secondary output and comments.
- **OK Green** (#5FB98C): Success output (`✓ trusted certificate`, https URLs).
- **Warn Red** (#B4433A): Failure output (`⚠ your connection is not private`).

### Named Rules
**The One Teal Rule.** Teal is the only accent on the surface. It appears on well under 10% of any viewport, and only on brand, links, prompts, and labels — Loop Teal at large/bold sizes, Loop Teal Text at reading sizes. A second accent color is prohibited; success/failure greens and reds live only inside terminal panels.

**The Two Worlds Rule.** Paper colors never appear inside terminal panels; terminal colors never leak onto paper. The contrast between the two IS the depth system.

## 3. Typography

**Display Font:** Geist (with system-ui, sans-serif)
**Body Font:** Geist (same family, weight/size contrast only)
**Label/Mono Font:** Geist Mono (with ui-monospace, SFMono-Regular, Menlo)

**Character:** One precise, quietly geometric sans doing all the talking, with mono reserved for anything a machine would say — commands, URLs, ports, labels, and site navigation. The mono isn't a costume; on this site the terminal content is literally the product demo.

### Hierarchy
- **Display** (600, 52px → 38px at ≤720px, 1.08, -0.035em): Hero headline only. Centered, `text-wrap: balance`, max-width 640px.
- **Headline** (600, 33px, 1.15, -0.03em): Section h2s. Left-aligned, max-width ~620px, balanced.
- **Title** (600, 20px, -0.02em): Step and feature h3s.
- **Body** (400, 14.5–19px, 1.55): Ledes at 18–19px in Soft Ink; supporting copy at 14.5–16px in Faded Ink. Max measure ~600–680px, `text-wrap: pretty`.
- **Label** (Geist Mono 400, 11.5–13.5px, 0.04–0.1em tracking): Kickers (uppercase, Loop Teal), card labels, install labels, nav, and footer. Mono at small sizes is the site's connective tissue.

### Named Rules
**The Machine Voice Rule.** If a machine would print it — a command, a URL, a port, a path — it is set in Geist Mono. Prose is never mono; machine text is never sans.

## 4. Elevation

Flat by default: the paper world is structured entirely by 1px hairline borders and background steps (Warm Paper → Raised Paper → Chip), not shadows. The exception is deliberate: dark terminal panels are physical objects on the bench, and the hero command panel casts the site's one soft shadow. The sticky docs header floats via translucency (88% Warm Paper + 10px backdrop blur) rather than a shadow line.

### Shadow Vocabulary
- **Object shadow** (`box-shadow: 0 1px 0 rgba(0,0,0,0.04), 0 10px 30px -12px rgba(22,33,31,0.35)`): Reserved for hero-level terminal panels — the shadow is tinted with the terminal's own green-black, not neutral gray.

### Named Rules
**The Object Rule.** Only dark terminal panels may cast shadows, and only when they're the focal object of the fold. Paper cards never do; they get hairline borders instead.

## 5. Components

There are no conventional buttons on this site — the primary CTA is a command to copy. Components are documented as they exist; don't invent button variants.

### Terminal Panels (signature component)
- **Style:** Terminal (#16211F) background, 10px radius, Geist Mono at 13–14.5px, line-height 1.7–1.8.
- **Titlebar variant:** three 11px dull-gray dots (#5C5751 — never macOS red/yellow/green), a right-aligned faded path label, 1px `rgba(255,255,255,0.08)` bottom border.
- **Content grammar:** teal `$` prompt, Terminal Ink for what matters, Terminal Dim for context, OK Green for success lines. Content is always a real, runnable command with its real output.

### Command CTA (`.hero-cmd` / `.install-cmd`)
- **Style:** A one-line terminal panel: 14–15.5px mono on Terminal background, 10px radius, 14–15px vertical padding, `white-space: nowrap` with horizontal scroll.
- **Hero variant:** carries the object shadow; followed by a mono `→ https://…` result line linking deeper.

### Cards / Containers
- **Corner Style:** 12px.
- **Background:** Raised Paper (#FBF9F3) with 1px Hairline border; the "after" twin uses Terminal background, borderless.
- **Internal Padding:** 22px 24px.
- **Usage:** Before/after comparisons only. This is not a card-grid site — features are borderless rows separated by soft hairlines.

### Inline Code
- **Style:** Chip (#ECE8DE) background, 4px radius, 2px 6px padding, mono at 0.9em, Ink text.

### Kicker
- **Style:** Geist Mono 13px, uppercase, 0.08em tracking, Loop Teal Text, 18px below-margin.
- **Constraint:** One kicker per page, period — it marks the single genuinely sequential section ("How it works" on the landing page). Sections carry plain headlines; an eyebrow above every section is prohibited (it was trimmed back to one in the 2026-07 critique pass).

### Step Numbers
- **Style:** 44px circle, 10% Loop Teal fill, Loop Teal mono 600 numeral. Reserved for the genuinely sequential "How it works" steps — not a generic section marker.

### Navigation
- **Style:** Geist Mono 13.5–14px; Ink links that hover to underline; active page in Loop Teal Text. Docs sidebar: 13.5px Faded Ink links grouped under three mono titles (Start · Commands · Reference), each link with a 2px transparent left rule; hover tints it teal, and a scroll-spy `.active` state (Loop Teal Text + solid left rule) tracks the current section. Sticky below the blurred docs header.

### Copy Button
- **Style:** Small mono button (12px, 7px 9px padding, 6px radius) pinned to the right edge of a command block: hairline `rgba(255,255,255,0.16)` border on the terminal surface, Terminal Dim text, hover to Terminal Ink, focus ring in Loop Teal. On copy it reads "copied ✓" in OK Green for two seconds.
- **Placement:** Every runnable command — the hero command, the three install steps, and docs `<pre>` blocks that contain only commands (output samples and usage syntax never get one). Injected by `site/site.js`; `<pre class="no-copy">` opts out.

### Docs Headings
- **Style:** Section h2s are Geist 600 (prose voice). Only h2s that are literal commands (`lewp setup`, `lewp lease`, …) carry `class="cmd"` and render in Geist Mono, per the Machine Voice Rule.

## 6. Do's and Don'ts

### Do:
- **Do** keep teal the only accent on paper — Loop Teal (#0E8C8C) for large/bold elements, Loop Teal Text (#0A6E6E) below large-text size; success/error color lives inside terminals only.
- **Do** put real, runnable commands and real output in every terminal panel — the demo is the pitch.
- **Do** structure with hairline borders (#E4E0D6) and background steps; reach for a shadow only under a focal terminal object.
- **Do** keep body text at Soft Ink (#4A4A46) or darker on paper — WCAG AA (≥4.5:1) is the floor for all text, labels included; Faint (#9A9891) is decorative-only.
- **Do** provide the `prefers-reduced-motion` alternative for every animation, as the hero path-draw already does (instant, fully-drawn state).
- **Do** keep measures tight: ~640px headlines, ~600–680px prose.

### Don't:
- **Don't** build the "VC-SaaS landing page": no gradient heroes, hero metrics, pricing-tier tables, logo walls, or "Trusted by 10,000 developers" claims — there is no proof yet, and fabricating it breaks the brand.
- **Don't** drift into "dark-mode terminal-core": the page is never dark; dark panels are objects on paper (The Two Worlds Rule).
- **Don't** add "corporate docs portal" chrome: no breadcrumbs, no sidebar forests, no search-first austerity.
- **Don't** introduce buttons where a copyable command will do — the CTA is the command.
- **Don't** color the terminal titlebar dots red/yellow/green; they are deliberately dull (#5C5751).
- **Don't** add new fonts, gradients, gradient text, glassmorphism (the docs-header blur is the single sanctioned use), or side-stripe accent borders thicker than the sidebar's 2px hover rule.
- **Don't** animate anything after first load; the hero's once-only draw is the site's entire motion budget.
