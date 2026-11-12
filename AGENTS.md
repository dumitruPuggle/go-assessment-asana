# Agent rules

These rules apply to every agent (and human) working in this repository.

## Keep README.md current

`README.md` is the shared context between agents. It is the single place that
explains how to run the program and how each package is designed. An agent
starting fresh reads it to learn the project, so it must always match the code.

**Read `README.md` before starting work**, and **update it in the same change**
whenever you make a significant design decision or codebase change. That
includes:

- Adding, removing or renaming a package, exported type, function or option.
- Changing command-line flags, defaults, exit codes, environment variables or
  required secrets. Also update `.env.example` when secrets change.
- Changing the output format (`users.json`, `projects.json`) or the files
  written.
- Changing behaviour that the README states as a guarantee. Examples: rate
  limiting, pagination, retries, scheduling, atomic writes, error handling,
  de-duplication.
- Adding or replacing a dependency.
- Any design decision with a non-obvious "why", for example choosing a sliding
  window over a token bucket. Record the reasoning in the relevant package
  section, not just the outcome.

How to update it:

- Edit the existing section that covers the area: the hands-on guide, the
  flags table, the output format, the project layout or the package reference.
  Don't append a changelog.
- Describe how the code works now. Remove statements that are no longer true.
- Before finishing, check that every flag, default, file name and function name
  mentioned in the README still exists in the code.
- Pure refactors, test-only changes and typo fixes don't need a README update
  unless they change something the README describes.

## Privacy

Don't add new personal data (names, emails, tokens) to `README.md` or any
other committed file. Real secrets go only in `.env`, which is git-ignored. The
real output already in the README's "Sample run" section was added at the
owner's request. Don't extend it with more personal data.
