# Development Guidelines

Use these durable coding practices.
Use `AGENTS.md`, `development-cycle.md`, and `build-release.md` for workflow, validation, and Package.
Treat sections above `## Project Practices` as govna-maintained canon.
Keep repo-specific practices in `## Project Practices`.

## Identifier Strategy

- Choose a primary key strategy early and document it in `arch.md`
- Prefer surrogate keys for internal identity
- Keep external IDs as indexed attributes
- Maintain an explicit mapping layer between external ID systems.
- Prohibit assumptions that external IDs are interchangeable.

## Schema And Data Migrations

- Treat schema changes as first-class events: version them, document them, test the migration path
- Verify old data compatibility with new schemas.
- Write migration logic for old data.
- Fail explicitly when migration logic is unavailable.
- Audit all foreign key references when a migration changes identity or key structure.

## External Integration Patterns

- Validate external data at the boundary
- Treat upstream shape and completeness as untrusted
- Define and document a clear precedence order when reconciling data from multiple sources.
- Cache external data locally with explicit TTL or versioning
- Never silently serve stale data as fresh

## Generated Artifact Propagation

- Propagate source-of-truth fixes to every template and rendered-example copy in the same change.
- Grep the full repo for the pattern being changed before considering a fix complete
- Treat the template as authoritative when it diverges from rendered output.
- Keep `build.sh` self-contained.
- Do not add sourced production helper modules.

## Error Handling And Validation

- Validate at system boundaries (user input, external APIs, file I/O)
- Trust internal code
- Report explicit errors instead of returning wrong output.
- State the failed condition in user-facing errors.
- Name the affected path or option when available.
- Provide a recovery action when the user can recover.
- Treat static analysis and linting errors as build failures.
- Validate installable-target declarations before compiling or installing them.
- Follow the applicable stack guidance for release-prep evidence, validation ordering, and build-state reuse.
- Pass release-prep evidence through the applicable stack's canonical CLI option.

## Testing Expectations

- Test every new function and error path in the implementation pass.
- Document every coverage gap caused by out-of-scope mocking infrastructure.
- Label tests that require live systems or manual verification as `[Manual]`

## Dependency And Import Hygiene

- Prefer standard library over external dependencies when the capability is equivalent
- Justify every added dependency.
- Reject convenience alone as dependency justification.
- Keep import paths consistent after renames or reorganizations

## CLI Usage Formatting

- Apply these rules to every command-line utility's help and version output.
- Accept `-h`, `-?`, and `--help` as help flags in every utility.
- Accept `-v` and `--version` as version flags in every utility.
- Accept `help` and `version` as commands in every multi-command utility.
- Print requested help on stdout.
- Exit 0 after printing requested help.
- Print `<name> v<version>` for `--version`.
- Render help through one shared renderer.
- Render each command's help page through the same renderer with the same header.
- Print the utility name in bold white followed by ` v<version>` in plain text as line one.
- Print a one-line description with no trailing period in gray as line two.
- Print the utility's URL alone, with no scheme, in dark gray as line three.
- Leave line four blank.
- Order sections as `Usage`, `Commands`, `Options`, one optional utility-specific section, `Examples`.
- Keep `Usage` and `Options` in every utility.
- Keep `Commands` only in a multi-command utility.
- Keep `Examples` last when present.
- Move `Overview` and `Notes` content to the README.
- Render each heading as one capitalized word in bold white on its own line with no colon.
- Indent each section body by 2 spaces.
- Separate sections with one blank line.
- Keep every line inside a section.
- Give `Usage` one synopsis per invocation form.
- Give `Usage` one generic synopsis in a multi-command utility.
- Write `Usage` placeholders in uppercase.
- List each command's form and meaning as a `Commands` row.
- Combine short and long flag forms on one line.
- Write option arguments in uppercase.
- Align meanings two spaces past the longest form in the section.
- End `Options` with the `-v, --version` and `-h, -?, --help` rows, appended by the renderer.
- Allow one indented paragraph at the end of a section body.
- Keep a utility README's `### Usage` text block byte-equal to the utility's plain help output.
- Emit one escape sequence per colored span.
- Emit no escape sequence when output is not a color terminal.
- Add every new flag to the shared renderer.
- Do not rely on framework defaults for new flags.
- Describe each command by what it reads or writes.
- State whether each command changes files.

Note: colors are xterm-256 index 231 with bold for the name and headings (one sequence, `ESC[1;38;5;231m`), 245 for the description, and 242 for the URL.

## Documentation Alignment

- Ship behavior docs with code.
- Verify every referenced symbol or path.
- Keep `arch.md` limited to built architecture.

## Go Practices

- Add single-line godoc comments to exported functions in shared Go packages.
- Declare a non-empty `const programVersion` string literal in every installable `cmd/<name>/main.go`.
- Validate every `programVersion` declaration through `build.sh` before compiling installable binaries.
- Pin `staticcheck` to the repository-governed version and invoke the pinned installation path directly.
- Treat `go vet` and `staticcheck` findings as build failures.
- Scan all `.go` and `.go.tmpl` files for stale import paths after a module rename.

## Project Practices

- Follow existing repo patterns unless an approved improvement says otherwise.
- Render attune's help through the `github.com/queone/gkit/help` package.
- Print `github.com/queone/attune` as the third help line.
