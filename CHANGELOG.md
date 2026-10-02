# Changelog

## 0.4.0

### Added

- Added `lewp info --kind route|alias|port` to filter entries by kind. `eval "$(lewp info --kind route --shell)"` now selects the directory's route without repeating its hostname, even when the route has aliases or the directory has bare ports.

### Changed

- Brought the docs site up to date with the CLI reference: fuller `lewp release` coverage (selector rules, `--forget` with paths, counts, `--pick` details), explicit-host conflict handling and `--auto-suffix` on `lewp lease`, and current version strings.

## 0.3.0

### Added

- Added `lewp release --pick` to choose one or more registered paths from an interactive checklist and release them together. It previews the combined plan and confirms before applying, and supports `--forget`, `--dry-run`, and `--yes`.

## 0.2.0

### Added

- Added global `lewp release` selectors for exact or recursive paths, registered hosts, and current numeric ports. Paths can target moved or deleted worktrees without changing directories.
- Added `--dry-run` previews, guarded recursive confirmation with `--yes`, and structured `--json` results.
- Added route-only and named bare-port scopes with `--route` and `--name`.

### Changed

- Changed `lewp release --port` to accept a numeric port. Use `--name <name>` to release a named bare port in the current or selected path.
- Release and forget operations now build a logical allocation plan and apply it atomically. Lewp refuses the operation if registry ownership or history changes after planning.
- `--forget` removes the complete matching identity and its history, while release without `--forget` keeps that history.

### Fixed

- Preserved plain-release compatibility for older CLI clients talking to the new daemon and added restart guidance when a new CLI reaches an older daemon.
- Made host-conflict cleanup commands safe to paste when paths contain shell metacharacters.
- Prevented release timeouts and output failures from reporting a misleading successful or failed mutation state.
