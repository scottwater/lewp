# Product

## Register

brand

## Platform

web

## Users

Developers running parallel local development on macOS — several apps, git worktrees, or AI-agent branches at once, each wanting its own URL. Their context is acute and daily: a browser full of `localhost:3000`, `:3001`, `:5173` tabs, cert warnings to click through, and OAuth callbacks that break when ports shift. They are skeptical, technical readers who will inspect an install script before piping it to bash. The surface (the marketing + docs site in `site/`) speaks to the same people who use the tool.

## Product Purpose

Lewp is a macOS-first local domain router/proxy. From any project-instance directory, one command leases a stable loopback port and a predictable `<instance>.<root>.lewp` hostname, and reverse-proxies browser traffic to a developer-started process — with trusted local HTTPS. It routes traffic; it never starts app processes. Success for the site: a visitor copies the install command and runs `lewp setup` within minutes of landing.

## Positioning

The end of port juggling: predictable `.lewp` domains for parallel local dev — no ports to remember, no cert warnings.

## Conversion & proof

- Primary CTA: copy and run the install command (`curl -fsSL … | bash`).
- Secondary CTA: read the docs (`docs.html`) to see how it actually works first.
- The line a visitor remembers after 10 seconds: "Real URLs for local dev."
- Belief ladder:
  1. My port-juggling pain is real and named (":3001 — which one was the admin again?")
  2. Lewp fixes it simply (one command → stable https URL)
  3. It's safe and non-invasive (routes only, never starts my app; loopback-only; readable source)
  4. Setup is fast and reversible (one install, one setup, uninstall documented)
- Proof on hand: none yet — the tool is the proof. Credibility comes from docs quality, the visible demo, and readable open source. No testimonials, logos, or user counts; do not fabricate them.

## Brand Personality

Calm, precise, handmade. A well-made tool by one person who cares — quiet confidence over hype. The voice explains rather than sells, names the pain in the reader's own words, and lets the demo do the persuading. The hand-drawn loop logo carries the only whimsy; everything around it is exact.

## Anti-references

- The VC-SaaS landing page: gradient heroes, hero metrics, pricing tiers, logo walls, "Trusted by 10,000 developers."
- Dark-mode terminal-core: the all-black, neon-accent, terminal-everywhere dev-tool aesthetic.
- The corporate docs portal: sterile enterprise chrome, sidebar forests, breadcrumbs, search-first austerity.

## Design Principles

- **The demo is the pitch.** Show the command and the URL it yields; never claim what can be shown. Terminal panels are evidence, not decoration.
- **Earn the `curl | bash`.** Every trust hurdle (installer, sudo, local CA) is addressed head-on with plain explanation, not glossed over.
- **Name the pain in their words.** Speak in ports, worktrees, and cert warnings — the reader's daily vocabulary — not in category marketing language.
- **Craft signals care.** The site's precision is itself proof of the tool's quality; sloppy spacing or broken details undermine the entire trust argument.
- **Calm over loud.** No urgency, no hype, no dark patterns. The reader is a skeptic; restraint is what convinces them.

## Accessibility & Inclusion

WCAG AA: body text ≥4.5:1 contrast, large text ≥3:1, keyboard navigable, and a `prefers-reduced-motion` alternative for every animation (the hero path-draw included).
