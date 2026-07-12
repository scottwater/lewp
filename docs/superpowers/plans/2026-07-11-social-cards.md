# Social Cards Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add branded Open Graph and Twitter/X images plus complete social metadata to the Lewp site.

**Architecture:** Render one dependency-free HTML fixture at each target viewport with a shared centered composition, then capture the browser output as PNG. Keep the generated PNGs beside the static HTML and reference them with absolute production URLs.

**Tech Stack:** Static HTML/CSS, SVG, Geist/Geist Mono, agent-browser, shell verification.

## Global Constraints

- Open Graph output: `site/og-image.png`, exactly 1200×630 pixels.
- Twitter/X output: `site/twitter-image.png`, exactly 1200×600 pixels.
- Upper image areas: 1200×560 and 1200×530 pixels; attribution strip: 70 pixels.
- Exact headline: `No more port juggling.`
- Exact attribution: `@scottw` and `lewp.ing`.
- Every production URL starts with `https://lewp.ing/`.
- Reuse the existing Lewp SVG path and `DESIGN.md` colors.
- Add no runtime or project dependency.

---

### Task 1: Render the social images

**Files:**
- Create temporarily: `tmp/social-card.html`
- Create: `site/og-image.png`
- Create: `site/twitter-image.png`

**Interfaces:**
- Consumes: existing logo geometry from `site/index.html`; tokens from `DESIGN.md`.
- Produces: two opaque PNG files referenced by Task 2.

- [ ] **Step 1: Confirm the browser capture tool exists**

Run:

```bash
command -v agent-browser
```

Expected: an executable path. If absent, use the in-app browser screenshot workflow; do not add a repository dependency.

- [ ] **Step 2: Create the temporary render fixture**

Create `tmp/social-card.html` with two query-selected canvases:

```html
<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <link rel="preconnect" href="https://fonts.googleapis.com">
  <link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
  <link href="https://fonts.googleapis.com/css2?family=Geist:wght@600&family=Geist+Mono:wght@500&display=swap" rel="stylesheet">
  <style>
    * { box-sizing: border-box; }
    html, body { margin: 0; overflow: hidden; }
    .card { width: 1200px; background: #F7F4EE; color: #1A1A1A; }
    .card.og { height: 630px; }
    .card.twitter { height: 600px; }
    .main { display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 30px; }
    .og .main { height: 560px; }
    .twitter .main { height: 530px; }
    svg { width: 310px; height: 155px; }
    h1 { margin: 0; font: 600 68px/1.05 Geist, sans-serif; letter-spacing: -0.035em; }
    footer { height: 70px; border-top: 1px solid #E4E0D6; padding: 0 72px; display: flex; align-items: center; justify-content: space-between; color: #6A675F; font: 500 20px/1 Geist Mono, monospace; }
  </style>
</head>
<body>
  <article class="card">
    <main class="main">
      <svg viewBox="0 0 240 120" fill="none" aria-hidden="true">
        <path d="M16 62 C 66 62 66 20 100 20 C 142 20 142 100 96 100 C 62 100 76 62 152 62 L 224 62" stroke="#0E8C8C" stroke-width="4.5" stroke-linecap="round"/>
        <circle cx="16" cy="62" r="8" fill="#0E8C8C"/>
        <circle cx="224" cy="62" r="6" fill="#0E8C8C"/>
      </svg>
      <h1>No more port juggling.</h1>
    </main>
    <footer><span>@scottw</span><span>lewp.ing</span></footer>
  </article>
  <script>
    document.querySelector('.card').classList.add(new URLSearchParams(location.search).get('size') === 'twitter' ? 'twitter' : 'og');
  </script>
</body>
</html>
```

- [ ] **Step 3: Capture both target viewports**

Serve the repository locally, wait for `document.fonts.ready`, set the browser viewport to the exact target dimensions, and capture only the viewport:

```bash
python3 -m http.server 4173
agent-browser open 'http://127.0.0.1:4173/tmp/social-card.html?size=og'
agent-browser viewport 1200 630
agent-browser eval 'document.fonts.ready.then(() => true)'
agent-browser screenshot site/og-image.png
agent-browser open 'http://127.0.0.1:4173/tmp/social-card.html?size=twitter'
agent-browser viewport 1200 600
agent-browser eval 'document.fonts.ready.then(() => true)'
agent-browser screenshot site/twitter-image.png
```

Expected: two PNG files with no browser chrome or scrollbars.

- [ ] **Step 4: Verify dimensions and inspect both outputs**

Run:

```bash
sips -g pixelWidth -g pixelHeight site/og-image.png site/twitter-image.png
```

Expected: `1200 × 630` and `1200 × 600`. Inspect both PNGs at full size and at roughly 400px wide; confirm exact text, centered composition, crisp logo, loaded Geist fonts, and the 70px strip.

- [ ] **Step 5: Remove the temporary fixture and commit the images**

```bash
trash tmp/social-card.html
git add site/og-image.png site/twitter-image.png
git commit -m "feat: add social card images"
```

### Task 2: Add social metadata

**Files:**
- Modify: `site/index.html` within `<head>`
- Modify: `site/docs.html` within `<head>`

**Interfaces:**
- Consumes: `https://lewp.ing/og-image.png` and `https://lewp.ing/twitter-image.png` from Task 1.
- Produces: crawler-readable canonical, Open Graph, and Twitter/X metadata.

- [ ] **Step 1: Write a failing metadata check**

Run:

```bash
for file in site/index.html site/docs.html; do
  rg -q 'property="og:image" content="https://lewp.ing/og-image.png"' "$file" &&
  rg -q 'name="twitter:image" content="https://lewp.ing/twitter-image.png"' "$file"
done
```

Expected: non-zero exit status because the tags do not exist.

- [ ] **Step 2: Add page-specific canonical and social tags**

Add this structure after each page's description, preserving its existing title and description as the page-specific social title and description:

```html
<link rel="canonical" href="https://lewp.ing/">
<meta property="og:type" content="website">
<meta property="og:url" content="https://lewp.ing/">
<meta property="og:title" content="lewp — Real URLs for local dev">
<meta property="og:description" content="lewp is a macOS local domain router. Run one command in your project directory and get a stable, trusted-HTTPS URL with no port to remember. It routes traffic; it never starts your app.">
<meta property="og:image" content="https://lewp.ing/og-image.png">
<meta property="og:image:width" content="1200">
<meta property="og:image:height" content="630">
<meta property="og:image:alt" content="Lewp loop logo and the words No more port juggling.">
<meta name="twitter:card" content="summary_large_image">
<meta name="twitter:site" content="@scottw">
<meta name="twitter:creator" content="@scottw">
<meta name="twitter:title" content="lewp — Real URLs for local dev">
<meta name="twitter:description" content="lewp is a macOS local domain router. Run one command in your project directory and get a stable, trusted-HTTPS URL with no port to remember. It routes traffic; it never starts your app.">
<meta name="twitter:image" content="https://lewp.ing/twitter-image.png">
<meta name="twitter:image:alt" content="Lewp loop logo and the words No more port juggling.">
```

For `site/docs.html`, use canonical/OG URL `https://lewp.ing/docs.html`, title `lewp docs — CLI reference`, and its existing description.

- [ ] **Step 3: Run metadata checks**

Run:

```bash
for file in site/index.html site/docs.html; do
  rg -q 'property="og:image" content="https://lewp.ing/og-image.png"' "$file" &&
  rg -q 'name="twitter:image" content="https://lewp.ing/twitter-image.png"' "$file" &&
  rg -q 'name="twitter:site" content="@scottw"' "$file"
done
rg --pcre2 -n 'content="/(?!/)|href="/(?!/)' site/index.html site/docs.html
```

Expected: the loop exits zero; the final search finds no social/canonical URL beginning with a single slash.

- [ ] **Step 4: Inspect both pages and commit**

Serve `site/`, open both pages, and confirm no visible-page regression. Then run:

```bash
git diff --check
git add site/index.html site/docs.html
git commit -m "feat: add social sharing metadata"
```

### Task 3: Final verification

**Files:**
- Verify: `site/og-image.png`
- Verify: `site/twitter-image.png`
- Verify: `site/index.html`
- Verify: `site/docs.html`

**Interfaces:**
- Consumes: Tasks 1 and 2.
- Produces: release-ready static site assets and metadata.

- [ ] **Step 1: Run repository tests**

```bash
go test ./...
```

Expected: all packages pass.

- [ ] **Step 2: Re-run asset and URL verification**

```bash
sips -g pixelWidth -g pixelHeight site/og-image.png site/twitter-image.png
rg -n 'https://lewp.ing/(og-image|twitter-image)\.png|https://lewp.ing/docs\.html|https://lewp.ing/' site/index.html site/docs.html
git diff --check HEAD~2..HEAD
```

Expected: correct dimensions, fully qualified production URLs, and no whitespace errors.
