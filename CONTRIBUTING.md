# Contributing to TTSBuddy CLI

Thanks for your interest in contributing! Here's how you can help.

## Reporting Bugs

Open a [bug report](https://github.com/ngelik/ttsbuddy-cli/issues/new?template=bug_report.yml) with:

- TTSBuddy CLI version (`ttsbuddy version`)
- OS and architecture
- Steps to reproduce
- Expected vs actual behavior

## Suggesting Features

Open a [feature request](https://github.com/ngelik/ttsbuddy-cli/issues/new?template=feature_request.yml) describing the use case and proposed solution.

## Submitting Pull Requests

1. Fork the repo and create a branch from `main`
2. Make your changes
3. Ensure all checks pass:
   ```bash
   make test    # unit tests with race detector
   make lint    # golangci-lint
   ```
4. Open a pull request against `main`

### PR guidelines

- Keep changes focused -- one concern per PR
- Add tests for new functionality
- Follow existing code patterns and conventions
- Update documentation if behavior changes

## Development Setup

```bash
git clone https://github.com/ngelik/ttsbuddy-cli.git
cd ttsbuddy-cli
make build    # build to bin/ttsbuddy
make test     # run tests
make lint     # run linter (install with: make tools)
```

See the [README](README.md) for full development details.
