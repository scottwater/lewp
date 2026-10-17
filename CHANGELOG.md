# Changelog

## 0.5.0

### Added

- Added `lewp lease --static <dir>` to serve a directory's files over HTTP and HTTPS with no app process running, for build output such as a compiled JavaScript bundle. Aliases serve the same files, and `--no-static` switches back to proxying on the same port and host. Requests can't leave the directory, dotfiles are never served (even through symlinks), and directories are never listed.
- `lewp list`, `lewp info`, and `--json` output show a route's static directory. `lewp list` and `lewp doctor` report a static route as up while its directory exists.

### Changed

- `lewp system start`, `lewp system restart`, and `lewp setup --start` now wait up to five seconds for the daemon to answer and fail with log and launchd diagnostics if it doesn't.
- `lewp move` keeps a static directory inside the project pointing inside it.
- The registry moves to schema version 3 in place, keeping existing ports, hosts, and custom suffixes.

### Fixed

- `lewp lease --static` against a daemon older than the CLI now fails with a `lewp system restart` hint instead of silently proxying.
- CLI tests run against an isolated home directory so they can never touch a developer's real `suffixes.toml`.

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
