# Contributing

Contributions are welcome through pull requests.

By submitting a contribution, you agree that your contribution is licensed under
the Apache License, Version 2.0.

Before opening a pull request, run:

```bash
go test ./...
cd npm
npm ci --ignore-scripts
npm test
npm run pack:check
```
