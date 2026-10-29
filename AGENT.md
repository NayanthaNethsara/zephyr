# Agent Rules

General rules and technical conventions for working in the Labyrithm codebase.

## Code Style

- Keep proper folder structure.
- Code should be self-explanatory (clear descriptive names, small focused functions).
- Do not comment. Only comment when strictly necessary to explain non-obvious business intent or hardware constraints.
- No unused abstractions, no speculative or future-proofing code. Build only what is asked.
- Use semantic color tokens (`bg-success`, `text-destructive`, etc.), not raw colors.
- Keep one color language per visual signal. Do not reuse the same palette for two different meanings in the same view.
- Strictly no emojis anywhere in responses, comments, or code.

## Git Conventions

- Do not commit or push. The user reviews and commits changes themselves.
- Do not add AI as a co-author in commit messages.
- Commit messages should be short, descriptive, and to the point.
