# Contributing

Contributions that improve the application, documentation, or examples are welcome. For larger changes, open an issue first to discuss the approach.

## Development setup

Install Go 1.27 or newer and Docker Compose. Clone the repository and run:

```sh
make test
make lint
make build
```

The Compose stack is needed for the database-backed integration suite:

```sh
make up
make test-integration
make down
```

Run `make fmt` to format Go code. Keep changes focused and update the relevant documentation when behavior changes. Add unit tests for analysis logic and integration coverage for behavior that depends on PostgreSQL or the telemetry stack.

## Pull requests

Describe the user-visible or operational change, its motivation, and any limitations. Include the commands used to validate it. Keep credentials, private keys, personal data, and production telemetry out of commits and test fixtures. Use the supplied pull request template and follow the [Code of Conduct](CODE_OF_CONDUCT.md).
