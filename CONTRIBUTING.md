# Contributing to Cotty

Thanks for your interest in making the multiplayer terminal better! Issues
and pull requests are welcome.

## Getting started

```sh
git clone https://github.com/tylerbroqs/cotty
cd cotty
go build -o cotty ./cmd/cotty
```

Requires Go 1.24+. No other toolchain, no code generation, no framework.

## Before you send a pull request

```sh
go vet ./...
go build ./...
```

Both must pass cleanly.

## Guidelines

- **Keep changes small and focused.** One concern per pull request; a
  targeted fix is easier to review and merge than a grab-bag.
- **Keep the codebase compact.** Cotty is a handful of small packages under
  [`internal/`](internal/) with minimal dependencies — think twice before
  adding a new dependency, and say why in the PR description.
- **Match the surrounding style.** Follow the conventions of the file you
  are editing: naming, comment density, error wrapping (`fmt.Errorf` with
  `%w`), and package documentation.
- **Protocol changes need care.** The wire format lives in
  [`internal/protocol`](internal/protocol/protocol.go) and is spoken by the
  CLI client, the relay, *and* the embedded web client
  ([`internal/webui/static/app.js`](internal/webui/static/app.js)) — update
  all of them together.
- **Security-sensitive areas** (E2EE in [`internal/e2ee`](internal/e2ee),
  permission checks in [`internal/session`](internal/session)) get extra
  scrutiny; explain your reasoning in the PR.

## Reporting bugs

Open an issue with:

- what you ran (`cotty host`/`join`/`relay` flags),
- what you expected, and what happened instead,
- OS and `cotty version` output.

For security vulnerabilities, **do not open a public issue** — see
[SECURITY.md](SECURITY.md).

## License

By contributing, you agree that your contributions will be licensed under
the [MIT License](LICENSE).
