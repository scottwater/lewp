# Lewp social cards

## Goal

Add Open Graph and Twitter/X cards for the static site. Link previews should
identify Lewp at a glance and stay legible at thumbnail size.

## Assets

Create two PNG files in `site/`:

- `og-image.png`: 1200×630 pixels
- `twitter-image.png`: 1200×600 pixels

Both cards use the same centered composition. The upper image area measures
1200×560 pixels for Open Graph and 1200×530 pixels for Twitter/X. A 70-pixel
attribution strip runs along the bottom.

## Composition

The upper area contains two elements:

1. The existing hand-drawn Lewp loop mark, centered.
2. `No more port juggling.`, centered below the mark.

Use the existing SVG path instead of generating a new mark. Render the cards
deterministically so the logo, text, spacing, and colors remain exact. GPT Image
2 artwork would add variation without helping this sparse composition.

The attribution strip has a Hairline top border. It places `@scottw` on the
left and `lewp.ing` on the right.

## Visual system

Follow `DESIGN.md`:

- Warm Paper (`#F7F4EE`) background
- Ink (`#1A1A1A`) headline
- Loop Teal (`#0E8C8C`) logo
- Hairline (`#E4E0D6`) strip border
- Faded Ink (`#6A675F`) attribution
- Geist 600 headline
- Geist Mono 500 attribution

The cards contain no terminal panel, product diagram, gradient, texture, or
additional copy.

## Metadata

Add social metadata to `site/index.html` and `site/docs.html`:

- canonical URL
- Open Graph type, URL, title, description, image URL, dimensions, and alt text
- Twitter card type, site/creator handle, title, description, image URL, and alt text

Use `summary_large_image` for Twitter/X. Every canonical and image URL starts
with `https://lewp.ing/`. Use `@scottw` for both Twitter handle fields.

## Verification

- Confirm PNG dimensions with an image inspection tool.
- Inspect both rendered cards at full size and thumbnail size.
- Confirm the two HTML files use fully qualified `https://lewp.ing` URLs.
- Run the repository's relevant site checks, if present.
