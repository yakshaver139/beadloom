# Contributing to Beadloom

Thanks for your interest in contributing to Beadloom! This guide covers everything you need to get started.

## Getting Started

1. Fork the repository and clone your fork:

   ```bash
   git clone https://github.com/<your-username>/beadloom
   cd beadloom
   ```

2. Make sure you have the prerequisites installed:

   - Go 1.23+
   - [Beads](https://github.com/steveyegge/beads) (`bd` CLI)
   - [Claude Code](https://claude.ai/claude-code) CLI (`claude`) — needed for integration testing

3. Build and run the tests:

   ```bash
   make build
   make test
   ```

## Development Workflow

1. Create a branch for your change:

   ```bash
   git checkout -b my-feature
   ```

2. Make your changes. Follow the existing code style and conventions.

3. Run checks before committing:

   ```bash
   make lint
   make test
   ```

4. Commit with a clear message describing **what** and **why**:

   ```
   Add retry logic for bd worktree create

   bd occasionally fails with a lock contention error when multiple
   worktrees are created in quick succession. Retry up to 3 times
   with exponential backoff.
   ```

5. Push your branch and open a pull request against `main`.

## Project Structure

```
internal/
├── cli/            # Cobra commands, flags, helpers
├── bd/             # bd CLI wrapper
├── claude/         # Claude API client
├── graph/          # Task DAG builder + cycle detection
├── cpm/            # Critical Path Method analyzer
├── planner/        # Execution plan + prompt generation
├── worktree/       # Git worktree lifecycle
├── orchestrator/   # Wave execution engine
├── reporter/       # Terminal status display
├── state/          # Persistent state
├── viewer/         # Embedded HTTP server + SPA
├── ui/             # Terminal styling helpers
└── assets/         # Embedded logo asset
```

## Code Guidelines

- **Keep it simple.** Prefer straightforward code over clever abstractions.
- **Internal packages.** All non-CLI logic lives under `internal/` — nothing is exported as a library.
- **Error handling.** Wrap errors with context using `fmt.Errorf("doing thing: %w", err)`.
- **Tests.** Add tests for new logic. Table-driven tests are preferred where they fit naturally.
- **No new dependencies** without discussion. Open an issue first if you need to add a dependency.

## Reporting Bugs

Open an issue with:

- What you expected to happen
- What actually happened
- Steps to reproduce
- Beadloom version (`beadloom --version`), Go version, and OS

## Suggesting Features

Open an issue describing the use case and why the feature would be useful. For larger changes, open an issue to discuss the approach before writing code.

## License

By contributing, you agree that your contributions will be licensed under the [MIT License](LICENSE).
