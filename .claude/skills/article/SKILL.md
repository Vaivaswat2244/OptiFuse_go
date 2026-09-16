---
name: article
description: Write a technical blog post or article in a human voice — earned jokes, real numbers, and an ending that lands rather than summarises. Use when drafting anything for Medium, dev.to, a personal blog, a newsletter, or a long-form Reddit or HN post, or when the user asks to make writing sound less AI-generated.
---

# Writing an article

The goal is a piece a real engineer would write and another engineer would finish. Everything
below serves that.

## Voice

**No em dashes.** They are the single clearest AI tell. Use commas, colons, parentheses, or two
sentences.

Vary sentence length hard. A long explanatory sentence followed by a four-word one is what
natural writing sounds like. Uniform medium-length sentences read as machine output no matter
how good the words are.

Fragments are allowed. Starting a sentence with "And" or "But" is allowed. Contractions are
allowed.

Never write in LinkedIn voice. No "Here's what I learned", no thread emoji, no "Let that sink
in", no rhetorical question as a section opener.

Concrete numbers beat adjectives every time. "8.86e-6 against 9.2e-6" is more persuasive than
"slightly more expensive", and it invites the reader to check you, which is the point.

## Jokes

**Jokes must be load-bearing.** A joke that could be deleted without loss is decoration, and
decoration reads as trying too hard. The good ones do a job: they mark a turn in the story,
puncture the writer's own confidence, or make an uncomfortable admission survivable.

Three that work:

- **Self-deprecation about a real mistake.** "The paper was fine. I was the problem." Short,
  admits the thing, moves on. The reader trusts you more afterwards.
- **Understatement.** After discovering something bad: "Slightly less comfortable." After
  documentation that answers nothing: "Perfect." The gap between the tone and the situation is
  the joke.
- **Setup and payoff.** Plant a line early ("I was very pleased with myself. This is relevant
  later.") and let a later section collect it. Costs one sentence, and readers enjoy noticing.

Rules:

- **Punch at yourself, never at the person who was right.** If someone corrected you, they are
  the competent one in the story. Mocking them makes you look worse than the mistake did.
- **Three or four jokes in fifteen hundred words.** More than that and the piece stops being
  about the thing.
- **No exclamation marks doing the work.** If it needs one to read as funny, it is not funny.
- **Never explain the joke** or signal it in advance. No "funnily enough", no "plot twist".

## Structure

Open with the specific thing that happened, not with context. "My optimizer promised 95%
savings. It was pricing a charge AWS doesn't make." Context can wait two paragraphs; nobody
reads the second paragraph of a piece that opened with background.

Tell it in the order you discovered it, including the wrong turns. A story where you were wrong
and found out is far more readable than a report of what you now know. The reader gets to be
slightly ahead of you, which is enjoyable.

Give each turn its own short section with a plain heading. Headings that describe rather than
tease: "The unravelling" over "You won't believe what happened next".

## Endings

The ending is the part most drafts get wrong. **Do not summarise.** The reader just read it.

Good endings, roughly in order of usefulness:

1. **The reframe.** What you now believe that you did not before, stated flatly. "The tool is
   less impressive now. Which is a worse pitch and a better tool."
2. **The unresolved.** Name what you still do not know. Being honest about open questions makes
   everything before it more credible, and it earns a "to be continued" instead of promising one.
3. **The callback.** Return to an image or number from the opening, now meaning something
   different.
4. **The cost, stated plainly.** What the mistake actually cost in time, money, or credibility.

Never end with:

- A summary of the points above
- "Thanks for reading"
- A moral stated outright. Let the reader draw it; they will draw it harder.
- A promise about future work you have not started

If the piece has a lesson, the strongest place for it is two thirds through, not at the end. End
on the specific, not the general.

## Before delivering

- Search for em dashes. There should be none.
- Read the first and last sentence together. If the last one restates the first, rewrite it.
- Check every joke: delete it and see if the paragraph is worse. If it is not, leave it deleted.
- Check that the numbers are real. Invented precision in a technical post is the fastest way to
  lose a reader who knows the domain.
- Confirm the piece does not claim work is finished when it is not. If the repo will contradict
  the post, fix the post.
