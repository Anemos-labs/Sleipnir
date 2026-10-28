---
name: landing-page-design
description: A landing page or project website: one primary action, page structure, real copy, a default visual system, every state, a ship checklist.
license: MIT
author: Adapted from the landing-page-design skill of AI Design Skills by Elaya (github.com/elayadesign/ai-design-skills), which credits taste-skill by Leon Lin (github.com/Leonxlnx/taste-skill), both MIT licence
---

# Landing page design

A landing page wins one intent: **one offer, one audience, one primary action.** A homepage serves many; this does not.
Decide what the page says and how it is ordered (part A) before any visual value (part B). Where the project already
has a design system, brand or framework convention, it wins over part B; part B is the default when there is none.

# Part A: what the page says

## A1. Settle these first

- The one primary action (try it, install it, download, star the repository, join the waitlist) and what counts as done.
- Who the page is for, the problem they have, and the three reasons they would not act today.
- The proof that exists: real numbers, benchmarks, screenshots, a demo, users, the licence, the release.

When something is unknown, assume the likeliest answer, write the assumption down in one line, and carry on.

## A2. Structure

Above the fold:

1. Headline: the outcome, for whom.
2. Subheadline: how, with one specific detail.
3. The primary call to action: a verb and what they get ("Install in one command", "Start free", never "Learn more" or "Submit").
4. One proof signal: a number, a quote, a badge that is true.
5. The product itself: a real screenshot, a short recording, or a live demo, not a stock illustration.

Then the argument: the problem and how the product solves it, three to five benefits stated as outcomes, how it works
in three steps, proof beside the claim it supports. Then the objections: a FAQ of six to twelve real questions,
what it costs and what is free, and the same call to action as at the top.

Pick one layout and say why: hero and sections (the product reads from one screenshot, the usual case); long-form
story (the reader must be taught before they trust it); minimal (high-intent visitors, a download or a waitlist);
comparison ("X vs Y" searches, an alternative to a known product).

## A3. Copy

- Benefits before features: a feature is what it does, a benefit is what that means for the reader.
- Be specific: "Cuts a weekly report from 4 hours to 15 minutes", not "Save time and streamline".
- One primary call to action; nothing competes with it above the fold.
- Treat objections as a section, not a footnote; move the FAQ up when the offer asks a lot.
- Sentence case for headings and buttons. No filler: "revolutionize", "supercharge", "cutting edge", "effortless",
  "seamless", "world class", "harness the power". Say what the product does.
- Write real copy in the product's voice. No lorem ipsum, no "Your headline here".
- Sample data reads like a real product's: specific names that fit the audience, believable numbers (not all round),
  varied dates, plausible company names for the industry. Never pass invented testimonials, customers or metrics off
  as real: label sample content as sample, or leave the section out.
- Messages are calm and specific: "Invoice sent to Dana", "Upload failed: the file is over 20 MB." No exclamation marks.

## A4. Build order

Hero, benefits, how it works, proof, FAQ, final call to action: one section at a time, each checked in the browser
before the next. Do not regenerate the whole page on each pass; small diffs stay reviewable.

## A5. Search

Index evergreen pages whose promise matches what people search for: a clear `<title>`, a meta description, the FAQ as
plain questions and answers (FAQ structured data when it fits), links from the rest of the site. `noindex` pages made
for one campaign or a short-lived offer.

# Part B: a default visual system

Every visual value comes from a scale, not from the moment.

## B1. Type

- One typeface for the site (a monospace beside it only for code and numbers). A modern sans with real weights
  (Geist, Manrope and the like) unless the brand says otherwise. Weights up to semibold or bold; no black weights.
- Sizes on one scale, for example Tailwind's: 12, 14, 16, 18, 20, 24, 30, 36, 48, 60, 72 px, each with its line height.
  No one-off sizes (`text-[19px]`, `1.4rem`).
- Body text at 16 px or more. Buttons 16 px semibold (14 px in a header).
- `text-wrap: balance` on headings and `text-wrap: pretty` on body copy, so no word sits alone on a last line.
- Cap the hero heading and subheading at about 680 px wide, and break the heading where the thought breaks.

## B2. Space and shape

- Spacing from one scale: 0, 2, 4, 8, 12, 16, 24, 32, 40, 48, 64, 80, 96 px. Nothing between.
- Radii from one scale. A shape nested in another with a gap under 32 px takes `inner radius = outer radius - gap`
  (square when that is 2 px or less).
- A card's border goes all the way round or not at all.
- Flat backgrounds; a gradient, if any, on hero text only. In dark mode use near-black greys (#000, #181818, #1F1F1F,
  #272727), not pure grey-blue.
- One icon set throughout (Phosphor, Lucide, Solar or similar), at one stroke weight.

## B3. Motion

- Ease out into place: `cubic-bezier(0.22, 1, 0.36, 1)` at about 500 ms for movement, 200 to 300 ms for colour and
  opacity on hover. Never the browser's `ease`, `linear` or `ease-in-out`.
- Animate only `transform` and `opacity`, never `top`, `left`, `width` or `height`.
- Sections rise about 32 px and fade in once as they enter the viewport, triggered by `IntersectionObserver`, never by
  an unthrottled scroll listener.
- Honour `prefers-reduced-motion`: replace movement with a plain fade.
- Navigation: a compact bar, translucent over a background blur; on mobile a full-height panel whose links arrive in
  reading order, 60 ms apart, the whole sequence under 400 ms.

## B4. States

Every interactive element has all of them: hover, active (a slight press), a visible focus ring, loading (a skeleton
shaped like the content, not a spinner), empty (a composed first-use view, never a blank panel) and error (inline and
specific, never `alert()`). No dead links: a control is either wired or visibly disabled. Navigation marks the current page.

# Ship checklist

Check each in a real browser, at phone and desktop widths, in light and dark mode when the site has both:

- [ ] One offer, one audience, one primary action; nothing competes with it above the fold
- [ ] The product is shown (screenshot, recording or demo), and each claim has its proof beside it
- [ ] Specific numbers, real copy, no filler words, no sample content passed off as real
- [ ] Type, spacing and radius values all from their scales; one typeface, one icon set
- [ ] Hover, active, focus, loading, empty and error states; no dead links
- [ ] Motion on transform and opacity only, `prefers-reduced-motion` honoured
- [ ] `<title>`, meta description, `og:image` and social tags, a favicon, alt text on every meaningful image
- [ ] Semantic landmarks (`nav`, `main`, `section`, `footer`), a skip-to-content link, keyboard reachable
- [ ] A branded 404, links to the licence or terms, a way back from every page
- [ ] Client-side validation on every form; nothing in the console
